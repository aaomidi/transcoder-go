package cmd

import (
	"errors"
	"github.com/Vilsol/transcoder-go/config"
	"github.com/Vilsol/transcoder-go/models"
	"github.com/Vilsol/transcoder-go/notifications"
	"github.com/Vilsol/transcoder-go/transcoder"
	"github.com/Vilsol/transcoder-go/utils"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

var terminated bool

var LogLevel string
var ForceColors bool

var rootCmd = &cobra.Command{
	Use: "transcoder [flags] <path> ...",

	Short: "transcoder is an opinionated wrapper around ffmpeg",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		level, err := log.ParseLevel(LogLevel)

		if err != nil {
			panic(err)
		}

		log.SetFormatter(&log.TextFormatter{
			ForceColors: ForceColors,
		})
		log.SetOutput(os.Stdout)
		log.SetLevel(level)

		config.InitializeConfig()
		notifications.InitializeNotifications()
	},
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return errors.New("must supply at least a single path")
		}

		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		fileList := make([]string, 0)

		for _, arg := range args {
			files, err := filepath.Glob(arg)

			if err != nil {
				log.Fatal(err)
			}

			log.Tracef("Found %s: %d", arg, len(files))

			fileList = append(fileList, files...)
		}

		for _, fileName := range fileList {
			if terminated {
				return
			}

			ext := filepath.Ext(fileName)

			valid := false
			for _, extension := range viper.GetStringSlice("extensions") {
				if ext == extension {
					valid = true
					break
				}
			}

			if !valid {
				continue
			}

			lastDot := strings.LastIndex(fileName, ".")
			extCorrectedOriginal := fileName[:lastDot] + ".mp4"

			processedFileName := filepath.Dir(extCorrectedOriginal) + "/." + filepath.Base(extCorrectedOriginal) + ".processed"

			stat, err := os.Stat(processedFileName)

			if err != nil && !os.IsNotExist(err) {
				log.Errorf("Error reading file %s: %s", processedFileName, err)
				continue
			}

			if stat != nil {
				// File already processed
				continue
			}

			log.Infof("Transcoding: %s", fileName)
			metadata := transcoder.ReadFileMetadata(fileName)

			tempFileName := fileName + ".transcode-temp"

			_, err = os.Stat(tempFileName)

			if err != nil && !os.IsNotExist(err) {
				log.Errorf("Error reading file %s: %s", tempFileName, err)
				continue
			}

			if err == nil {
				log.Warningf("File is already being transcoded: %s", fileName)
				continue
			}

			killed, lastReport := transcoder.TranscodeFile(fileName, tempFileName, metadata)

			if terminated {
				notifications.NotifyEnd(nil, nil, models.ResultError)
				continue
			}

			f, err := os.Create(processedFileName)

			if err != nil {
				log.Errorf("Error writing file %s: %s", processedFileName, err)
				continue
			}

			_ = f.Close()

			if killed {
				// Assume corrupted output file
				err := os.Remove(tempFileName)

				if err != nil && !os.IsNotExist(err) {
					log.Errorf("Error deleting file %s: %s", tempFileName, err)
					continue
				}

				if lastReport != nil {
					if int64(lastReport.TotalSize) > metadata.Format.SizeInt() {

						log.Infof("Kept original %s: %s < %s",
							fileName,
							utils.BytesHumanReadable(metadata.Format.SizeInt()),
							utils.BytesHumanReadable(int64(lastReport.TotalSize)),
						)

						notifications.NotifyEnd(nil, lastReport, models.ResultKeepOriginal)
					}
				}

				continue
			}

			resultMetadata := transcoder.ReadFileMetadata(tempFileName)

			if viper.GetBool("keep-old") && resultMetadata.Format.SizeInt() > metadata.Format.SizeInt() {
				// Transcoded file is bigger than original
				err := os.Remove(tempFileName)

				if err != nil {
					log.Errorf("Error deleting file %s: %s", tempFileName, err)
					continue
				}

				log.Infof("Kept original %s: %s < %s",
					fileName,
					utils.BytesHumanReadable(metadata.Format.SizeInt()),
					utils.BytesHumanReadable(resultMetadata.Format.SizeInt()),
				)

				notifications.NotifyEnd(resultMetadata, nil, models.ResultKeepOriginal)
			} else {
				// Transcoded file is smaller than original
				err := os.Remove(fileName)

				if err != nil {
					log.Errorf("Error deleting file %s: %s", fileName, err)
					continue
				}

				err = os.Rename(tempFileName, extCorrectedOriginal)

				if err != nil {
					log.Errorf("Error renaming file %s to %s: %s", tempFileName, extCorrectedOriginal, err)
					continue
				}

				log.Infof("Replaced %s with transcoded: %s < %s",
					fileName,
					utils.BytesHumanReadable(resultMetadata.Format.SizeInt()),
					utils.BytesHumanReadable(metadata.Format.SizeInt()),
				)

				notifications.NotifyEnd(resultMetadata, nil, models.ResultReplaced)
			}

		}
	},
}

func Execute() {
	terminate := make(chan os.Signal)

	go func() {
		<-terminate
		terminated = true
	}()

	signal.Notify(terminate, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&LogLevel, "log", "info", "The log level to output")
	rootCmd.PersistentFlags().BoolVar(&ForceColors, "colors", false, "Force output with colors")

	rootCmd.PersistentFlags().StringP("flags", "f", "-map 0 -c:v libx265 -preset ultrafast -x265-params crf=16 -c:a aac -strict -2 -b:a 256k", "The base flags used for all transcodes")
	rootCmd.PersistentFlags().StringSliceP("extensions", "e", []string{".mp4", ".mkv", ".flv"}, "Transcoded file extensions")
	rootCmd.PersistentFlags().Int("interval", 5, "How often to output transcoding status")
	rootCmd.PersistentFlags().Bool("stderr", false, "Whether to output ffmpeg stderr stream")
	rootCmd.PersistentFlags().Bool("keep-old", false, "Keep old version of video if transcoded version is larger")
	rootCmd.PersistentFlags().Bool("early-exit", true, "Early exit if transcoded version is larger than original (requires keep-old)")

	rootCmd.PersistentFlags().String("tg-bot-key", "", "Telegram Bot API Key")
	rootCmd.PersistentFlags().Int64("tg-chat-id", 0, "Telegram Bot Chat ID")

	_ = viper.BindPFlag("flags", rootCmd.PersistentFlags().Lookup("flags"))
	_ = viper.BindPFlag("extensions", rootCmd.PersistentFlags().Lookup("extensions"))
	_ = viper.BindPFlag("interval", rootCmd.PersistentFlags().Lookup("interval"))
	_ = viper.BindPFlag("stderr", rootCmd.PersistentFlags().Lookup("stderr"))
	_ = viper.BindPFlag("keep-old", rootCmd.PersistentFlags().Lookup("keep-old"))
	_ = viper.BindPFlag("early-exit", rootCmd.PersistentFlags().Lookup("early-exit"))

	_ = viper.BindPFlag("tg-bot-key", rootCmd.PersistentFlags().Lookup("tg-bot-key"))
	_ = viper.BindPFlag("tg-chat-id", rootCmd.PersistentFlags().Lookup("tg-chat-id"))
}
package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/zapr"
	"github.com/livekit/protocol/logger"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/livekit/livekit-recorder/service/pkg/config"
	"github.com/livekit/livekit-recorder/service/pkg/service"
	"github.com/livekit/livekit-recorder/service/version"
)

func main() {
	app := &cli.App{
		Name:        "livekit-recorder-service",
		Usage:       "LiveKit Recorder Service",
		Description: "runs the recording service",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "path to LiveKit recording config defaults",
			},
			&cli.StringFlag{
				Name:    "config-body",
				Usage:   "Default LiveKit recording config in JSON, typically passed in as an env var in a container",
				EnvVars: []string{"LIVEKIT_RECORDER_SVC_CONFIG"},
			},
			&cli.StringFlag{
				Name:    "redis-host",
				Usage:   "host (incl. port) to redis server",
				EnvVars: []string{"REDIS_HOST"},
			},
		},
		Action: runService,
		Commands: []*cli.Command{
			{
				Name:   "start-recording",
				Usage:  "Starts a recording",
				Action: startRecording,
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "ws-url",
						Usage:    "name of room to join",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "token",
						Usage:    "Recorder room token",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "aws-key",
						Usage:    "AWS access key",
						Required: false,
					},
					&cli.StringFlag{
						Name:     "aws-secret",
						Usage:    "AWS access secret",
						Required: false,
					},
					&cli.StringFlag{
						Name:     "s3",
						Usage:    "s3 path (\"<bucket>/<key>\")",
						Required: false,
					},
				},
			},
			{
				Name:   "stop-recording",
				Usage:  "Stops a recording",
				Action: stopRecording,
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "id",
						Usage:    "Recording ID",
						Required: true,
					},
				},
			},
		},
		Version: version.Version,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
	}
}

func getConfig(c *cli.Context) (*config.Config, error) {
	confString, err := getConfigString(c)
	if err != nil {
		return nil, err
	}

	return config.NewConfig(confString, c)
}

func initLogger(level string) {
	conf := zap.NewProductionConfig()
	if level != "" {
		lvl := zapcore.Level(0)
		if err := lvl.UnmarshalText([]byte(level)); err == nil {
			conf.Level = zap.NewAtomicLevelAt(lvl)
		}
	}

	l, _ := conf.Build()
	logger.SetLogger(zapr.NewLogger(l), "livekit-recorder")
}

func runService(c *cli.Context) error {
	conf, err := getConfig(c)
	if err != nil {
		return err
	}

	initLogger(conf.LogLevel)

	rc, err := service.NewMessageBus(conf)
	if err != nil {
		return err
	}

	worker := service.InitializeWorker(conf, rc)

	if conf.HealthPort != 0 {
		h := &handler{worker: worker}
		go http.ListenAndServe(fmt.Sprintf(":%d", conf.HealthPort), h)
	}

	finishChan := make(chan os.Signal, 1)
	signal.Notify(finishChan, syscall.SIGTERM, syscall.SIGQUIT)

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT)

	go func() {
		select {
		case sig := <-finishChan:
			logger.Infow("Exit requested, finishing recording then shutting down", "signal", sig)
			worker.Stop(false)
		case sig := <-stopChan:
			logger.Infow("Exit requested, stopping recording and shutting down", "signal", sig)
			worker.Stop(true)
		}
	}()

	return worker.Start()
}

func getConfigString(c *cli.Context) (string, error) {
	configFile := c.String("config")
	configBody := c.String("config-body")
	if configBody == "" {
		if configFile != "" {
			content, err := ioutil.ReadFile(configFile)
			if err != nil {
				return "", err
			}
			configBody = string(content)
		}
	}
	return configBody, nil
}

type handler struct {
	worker *service.Worker
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(h.worker.Status()))
}

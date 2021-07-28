package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/livekit/livekit-recorder/worker/pkg/config"
	"github.com/livekit/livekit-recorder/worker/pkg/logger"
	"github.com/livekit/livekit-recorder/worker/pkg/service"
	"github.com/livekit/livekit-recorder/worker/version"
)

func main() {
	app := &cli.App{
		Name:        "livekit-recording-worker",
		Usage:       "LiveKit recording worker",
		Description: "runs the recording worker",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "path to LiveKit recording config defaults",
			},
			&cli.StringFlag{
				Name:    "config-body",
				Usage:   "Default LiveKit recording config in JSON, typically passed in as an env var in a container",
				EnvVars: []string{"LIVEKIT_RECORDER_WORKER_CONFIG"},
			},
			&cli.StringFlag{
				Name:    "redis-host",
				Usage:   "host (incl. port) to redis server",
				EnvVars: []string{"REDIS_HOST"},
			},
		},
		Action:  startWorker,
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

func startWorker(c *cli.Context) error {
	conf, err := getConfig(c)
	if err != nil {
		return err
	}

	logger.Init(conf.LogLevel)

	rc, err := service.StartRedis(conf)
	if err != nil {
		return err
	}
	worker := service.InitializeWorker(conf, rc)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	go func() {
		sig := <-sigChan
		logger.Infow("exit requested, shutting down", "signal", sig)
		worker.Stop()
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

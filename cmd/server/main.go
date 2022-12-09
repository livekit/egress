package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/egress/version"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
)

func main() {
	app := &cli.App{
		Name:        "egress",
		Usage:       "LiveKit Egress",
		Version:     version.Version,
		Description: "runs the recorder in standalone mode or as a service",
		Commands: []*cli.Command{
			{
				Name:        "run-handler",
				Description: "runs a request in a new process",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name: "request",
					},
					&cli.StringFlag{
						Name: "config",
					},
				},
				Action: runHandler,
				Hidden: true,
			},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "LiveKit Egress yaml config file",
				EnvVars: []string{"EGRESS_CONFIG_FILE"},
			},
			&cli.StringFlag{
				Name:    "config-body",
				Usage:   "LiveKit Egress yaml config body",
				EnvVars: []string{"EGRESS_CONFIG_BODY"},
			},
		},
		Action: runService,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
	}
}

func runService(c *cli.Context) error {
	configFile := c.String("config")
	configBody := c.String("config-body")
	if configBody == "" {
		if configFile == "" {
			return errors.ErrNoConfig
		}
		content, err := ioutil.ReadFile(configFile)
		if err != nil {
			return err
		}
		configBody = string(content)
	}

	conf, err := config.NewServiceConfig(configBody)
	if err != nil {
		return err
	}

	rc, err := redis.GetRedisClient(conf.Redis)
	if err != nil {
		return err
	}

	rpcServer := egress.NewRedisRPCServer(rc)
	svc := service.NewService(conf, rpcServer)

	if conf.HealthPort != 0 {
		go func() {
			_ = http.ListenAndServe(fmt.Sprintf(":%d", conf.HealthPort), &httpHandler{svc: svc})
		}()
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGTERM, syscall.SIGQUIT)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	go func() {
		select {
		case sig := <-stopChan:
			logger.Infow("exit requested, finishing recording then shutting down", "signal", sig)
			svc.Stop(false)
		case sig := <-killChan:
			logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
			svc.Stop(true)
		}
	}()

	return svc.Run()
}

func runHandler(c *cli.Context) error {
	configBody := c.String("config")
	if configBody == "" {
		err := errors.ErrNoConfig
		return err
	}

	req := &livekit.StartEgressRequest{}
	reqString := c.String("request")
	err := protojson.Unmarshal([]byte(reqString), req)
	if err != nil {
		return err
	}

	conf, err := config.NewPipelineConfig(configBody, req)
	if err != nil {
		return err
	}

	logger.Debugw("handler launched")

	err = os.MkdirAll(conf.TmpDir, 0755)
	if err != nil {
		return err
	}
	_ = os.Setenv("TMPDIR", conf.TmpDir)

	rc, err := redis.GetRedisClient(conf.Redis)
	if err != nil {
		return err
	}

	rpcHandler := egress.NewRedisRPCServer(rc)
	handler := service.NewHandler(conf, rpcHandler)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	go func() {
		sig := <-killChan
		logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
		handler.Kill()
	}()

	err = handler.Run()
	if err != nil {
		os.Exit(1)
	}
	return nil
}

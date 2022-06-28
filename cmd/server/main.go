package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-redis/redis/v8"
	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/egress/version"
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
						Name: "config-body",
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
	conf, err := getConfig(c)
	if err != nil {
		return err
	}

	rc, err := getRedisClient(conf)
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
	conf, err := getConfig(c)
	if err != nil {
		return err
	}

	ctx, span := tracer.Start(context.Background(), "Handler.New")
	defer span.End()

	logger.Debugw("handler launched")

	rc, err := getRedisClient(conf)
	if err != nil {
		span.RecordError(err)
		return err
	}

	req := &livekit.StartEgressRequest{}
	reqString := c.String("request")
	err = proto.Unmarshal([]byte(reqString), req)
	if err != nil {
		span.RecordError(err)
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

	handler.HandleRequest(ctx, req)
	return nil
}

func getConfig(c *cli.Context) (*config.Config, error) {
	configFile := c.String("config")
	configBody := c.String("config-body")
	if configBody == "" {
		if configFile == "" {
			return nil, errors.ErrNoConfig
		}
		content, err := ioutil.ReadFile(configFile)
		if err != nil {
			return nil, err
		}
		configBody = string(content)
	}

	return config.NewConfig(configBody)
}

func getRedisClient(conf *config.Config) (*redis.Client, error) {
	logger.Infow("connecting to redis", "addr", conf.Redis.Address)

	var tlsConfig *tls.Config
	if conf.Redis.UseTLS {
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
	rc := redis.NewClient(&redis.Options{
		Addr:      conf.Redis.Address,
		Username:  conf.Redis.Username,
		Password:  conf.Redis.Password,
		DB:        conf.Redis.DB,
		TLSConfig: tlsConfig,
	})
	err := rc.Ping(context.Background()).Err()
	return rc, err
}

// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/handler"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/egress/version"
	"github.com/livekit/protocol/logger"
	lkredis "github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

var (
	//go:embed templates
	templateEmbedFs embed.FS
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
		os.Exit(1)
	}
}

func runService(c *cli.Context) error {
	configFile := c.String("config")
	configBody := c.String("config-body")
	if configBody == "" {
		if configFile == "" {
			return errors.ErrNoConfig
		}
		content, err := os.ReadFile(configFile)
		if err != nil {
			return err
		}
		configBody = string(content)
	}

	conf, err := config.NewServiceConfig(configBody)
	if err != nil {
		return err
	}

	rc, err := lkredis.GetRedisClient(conf.Redis)
	if err != nil {
		return err
	}

	bus := psrpc.NewRedisMessageBus(rc)
	ioClient, err := service.NewIOClient(bus)
	if err != nil {
		return err
	}
	svc, err := service.NewService(conf, ioClient)
	if err != nil {
		return err
	}
	psrpcServer, err := rpc.NewEgressInternalServer(svc, bus)
	if err != nil {
		return err
	}
	svc.Register(psrpcServer)

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

	rfs, err := fs.Sub(templateEmbedFs, "templates")
	if err != nil {
		return err
	}

	err = svc.StartTemplatesServer(rfs)
	if err != nil {
		return err
	}

	svc.StartDebugHandlers()

	if err = svc.RegisterListEgress(""); err != nil {
		return err
	}
	err = svc.Run()
	svc.Close()
	return err
}

func runHandler(c *cli.Context) error {
	configBody := c.String("config")
	if configBody == "" {
		return errors.ErrNoConfig
	}

	req := &rpc.StartEgressRequest{}
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

	rc, err := lkredis.GetRedisClient(conf.Redis)
	if err != nil {
		return err
	}

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	bus := psrpc.NewRedisMessageBus(rc)
	ioClient, err := rpc.NewIOInfoClient(bus)
	if err != nil {
		return err
	}
	h, err := handler.NewHandler(conf, bus, ioClient)
	if err != nil {
		if errors.IsFatal(err) {
			// service will send info update and shut down
			logger.Errorw("fatal error", err)
			return err
		} else {
			// update sent by handler
			return nil
		}
	}

	go func() {
		sig := <-killChan
		logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
		h.Kill()
	}()

	return h.Run()
}

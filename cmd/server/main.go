package main

import (
	"fmt"
	"io/ioutil"
	"os"

	"github.com/livekit/protocol/livekit"
	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/version"
)

func main() {
	app := &cli.App{
		Name:        "livekit-egress",
		Usage:       "LiveKit Egress",
		Description: "runs the recorder in standalone mode or as a service",
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
			&cli.StringFlag{
				Name:    "request",
				Usage:   "StartEgressRequest in JSON",
				EnvVars: []string{"RUN_REQUEST"},
			},
		},
		Action:  run,
		Version: version.Version,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
	}
}

func run(c *cli.Context) error {
	conf, err := getConfig(c)
	if err != nil {
		return err
	}

	req, err := getRequest(c)
	if err != nil {
		return err
	}

	if req != nil {
		return runRecorder(conf, req)
	}
	return runService(conf)
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

func getRequest(c *cli.Context) (*livekit.StartEgressRequest, error) {
	reqBody := c.String("request")
	if reqBody == "" {
		return nil, nil
	}

	content := []byte(reqBody)
	req := &livekit.StartEgressRequest{}
	err := protojson.Unmarshal(content, req)
	return req, err
}

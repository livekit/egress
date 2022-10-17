//go:build mage

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/livekit/mageutil"
)

const (
	imageName    = "livekit/egress"
	gstImageName = "livekit/gstreamer"
	gstVersion   = "1.20.4"

	config = "EGRESS_CONFIG_FILE"
)

func Integration(configFile string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	if configFile != "" {
		if strings.HasPrefix(configFile, "test/") {
			configFile = configFile[5:]
		} else {
			oldLocation := configFile
			idx := strings.LastIndex(configFile, "/")
			if idx != -1 {
				configFile = configFile[idx+1:]
			}
			if err = os.Rename(oldLocation, "test/"+configFile); err != nil {
				return err
			}
		}

		configFile = "/out/" + configFile
	}

	defer func() {
		// for some reason, these can't be deleted from within the docker container
		dirs, _ := filepath.Glob("test/output/EG_*")
		for _, dir := range dirs {
			_ = os.Remove(dir)
		}
	}()

	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		"docker build -t egress-test -f build/test/Dockerfile .",
		fmt.Sprintf(
			"docker run --rm -e %s=%s -v %s/test:/out egress-test",
			config, configFile, dir,
		),
	)
}

func Build() error {
	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build --no-cache -t %s:latest -f build/Dockerfile .", imageName),
	)
}

func BuildGStreamer() error {
	return buildGstreamer("docker build --no-cache")
}

func PublishGStreamer() error {
	return buildGstreamer("docker buildx build --no-cache --push --platform linux/amd64,linux/arm64")
}

func buildGstreamer(cmd string) error {
	commands := []string{"docker pull ubuntu:22.04"}
	for _, build := range []string{"base", "dev", "prod"} {
		commands = append(commands, fmt.Sprintf("%s"+
			" --build-arg GSTREAMER_VERSION=%s"+
			" -t %s:%s-%s"+
			" -f build/gstreamer/Dockerfile-%s"+
			" ./build/gstreamer",
			cmd, gstVersion, gstImageName, gstVersion, build, build,
		))
	}

	return mageutil.Run(context.Background(), commands...)
}

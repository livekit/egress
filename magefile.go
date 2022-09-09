//go:build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/livekit/egress/version"
)

const (
	imageName    = "livekit/egress"
	gstImageName = "livekit/gstreamer"
	gstVersion   = "1.20.3"

	config = "EGRESS_CONFIG_FILE"
)

func Integration(configFile string) error {
	return integration(configFile)
}

func integration(configFile string) error {
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

	return run(
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		"docker build -t egress-test -f build/test/Dockerfile .",
		fmt.Sprintf(
			"docker run --rm -e %s=%s -v %s/test:/out egress-test",
			config, configFile, dir,
		),
	)
}

func GStreamer() error {
	commands := []string{"docker pull ubuntu:22.04"}
	for _, build := range []string{"base", "dev", "prod"} {
		commands = append(commands, fmt.Sprintf(
			"docker build"+
				" -t %s:%s-%s"+
				" --build-arg GSTREAMER_VERSION=%s"+
				" -f build/gstreamer/Dockerfile-%s"+
				" ./build/gstreamer",
			gstImageName, gstVersion, build, gstVersion, build,
		))
	}

	return run(commands...)
}

func PublishGStreamer() error {
	commands := []string{"docker pull ubuntu:22.04"}
	for _, build := range []string{"base", "dev", "prod"} {
		commands = append(commands, fmt.Sprintf(
			"docker buildx build --push"+
				" --platform linux/amd64,linux/arm64"+
				" -t %s:%s-%s"+
				" --build-arg GSTREAMER_VERSION=%s"+
				" -f build/gstreamer/Dockerfile-%s"+
				" ./build/gstreamer",
			gstImageName, gstVersion, build, gstVersion, build,
		))
	}

	return run(commands...)
}

func Docker() error {
	return run(
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build -t %s:v%s -f build/Dockerfile .", imageName, version.Version),
	)
}

func PublishDocker() error {
	if !strings.Contains(version.Version, "SNAPSHOT") {
		return errors.New("cannot publish non-snapshot version")
	}

	return run(fmt.Sprintf(
		"docker buildx build --push"+
			" --platform linux/amd64,linux/arm64"+
			" -t %s:v%s -f build/Dockerfile .",
		imageName, version.Version))
}

func run(commands ...string) error {
	for _, command := range commands {
		args := strings.Split(command, " ")
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

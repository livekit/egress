//go:build mage
// +build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/livekit/livekit-egress/version"
)

const (
	imageName          = "livekit/livekit-egress"
	gstImageName       = "livekit/gstreamer"
	multiPlatformBuild = "docker buildx build --push --platform linux/amd64,linux/arm64"

	config   = "EGRESS_CONFIG_FILE"
	roomName = "LIVEKIT_ROOM_NAME"
)

func Test() error {
	return Integration("")
}

func Integration(configFile string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	if configFile != "" {
		if strings.HasPrefix(configFile, "test/") {
			configFile = configFile[5:]
		} else {
			return errors.New("please move config file to livekit-egress/test/")
		}
	}

	return run(
		"docker build -t livekit-egress-test -f build/test/Dockerfile .",
		fmt.Sprintf("docker run --rm -e %s=/out/%s -e %s=%s -v %s/test:/out livekit-egress-test",
			config, configFile, roomName, os.Getenv(roomName), dir),
	)
}

func GStreamer(version string) error {
	return run(
		"docker pull ubuntu:21.04",
		fmt.Sprintf("docker build -t %s:%s-base --build-arg GSTREAMER_VERSION=%s -f build/gstreamer/Dockerfile-base ./build/gstreamer", gstImageName, version, version),
		fmt.Sprintf("docker build -t %s:%s-dev --build-arg GSTREAMER_VERSION=%s -f build/gstreamer/Dockerfile-dev ./build/gstreamer", gstImageName, version, version),
		fmt.Sprintf("docker build -t %s:%s-prod --build-arg GSTREAMER_VERSION=%s -f build/gstreamer/Dockerfile-prod ./build/gstreamer", gstImageName, version, version),
	)
}

func PublishGStreamer(version string) error {
	return run(
		"docker pull ubuntu:21.04",
		fmt.Sprintf("%s -t %s:%s-base --build-arg GSTREAMER_VERSION=%s -f build/gstreamer/Dockerfile-base ./build/gstreamer", multiPlatformBuild, gstImageName, version, version),
		fmt.Sprintf("%s -t %s:%s-dev --build-arg GSTREAMER_VERSION=%s -f build/gstreamer/Dockerfile-dev ./build/gstreamer", multiPlatformBuild, gstImageName, version, version),
		fmt.Sprintf("%s -t %s:%s-prod --build-arg GSTREAMER_VERSION=%s -f build/gstreamer/Dockerfile-prod ./build/gstreamer", multiPlatformBuild, gstImageName, version, version),
	)
}

func Docker() error {
	return run(fmt.Sprintf("docker build -t %s:v%s -f build/Dockerfile .", imageName, version.Version))
}

func PublishDocker() error {
	// don't publish snapshot versions as latest or minor version
	if !strings.Contains(version.Version, "SNAPSHOT") {
		return errors.New("cannot publish non-snapshot versions")
	}

	return run(fmt.Sprintf("%s -t %s:v%s -f build/Dockerfile .", multiPlatformBuild, imageName, version.Version))
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

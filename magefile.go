//go:build mage
// +build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/magefile/mage/mg"

	"github.com/livekit/livekit-recorder/version"
)

const (
	imageName    = "livekit/livekit-recorder"
	gstImageName = "livekit/gstreamer"
)

// Default target to run when none is specified
// If not set, running mage will list available targets
var Default = Test

// run unit tests
func Test() error {
	return run("go test --tags=test ./...")
}

func Integration() error {
	return run(
		"docker build -t livekit-recorder-test -f build/test/Dockerfile .",
		"docker run --rm livekit-recorder-test",
	)
}

func Docker() error {
	return run(fmt.Sprintf("docker build -t %s:v%s -f build/Dockerfile .", imageName, version.Version))
}

func PublishDocker() error {
	mg.Deps(Docker)

	// don't publish snapshot versions as latest or minor version
	if !strings.Contains(version.Version, "SNAPSHOT") {
		return errors.New("cannot publish non-snapshot versions")
	}

	return run(fmt.Sprintf("docker push %s:v%s", imageName, version.Version))
}

func GStreamer(version string) error {
	return run(
		"docker pull ubuntu:21.04",
		fmt.Sprintf("docker build -t %s:base --build-arg GSTREAMER_VERSION=%s -f build/gstreamer/Dockerfile-base ./build/gstreamer", gstImageName, version),
		fmt.Sprintf("docker build -t %s:%s-dev -f build/gstreamer/Dockerfile-dev ./build/gstreamer", gstImageName, version),
		fmt.Sprintf("docker build -t %s:%s-prod -f build/gstreamer/Dockerfile-prod ./build/gstreamer", gstImageName, version),
	)
}

func PublishGStreamer(version string) error {
	return run(
		fmt.Sprintf("docker push %s:%s-dev", gstImageName, version),
		fmt.Sprintf("docker push %s:%s-prod", gstImageName, version),
	)
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

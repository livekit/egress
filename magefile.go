// +build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/magefile/mage/mg"

	"github.com/livekit/livekit-recorder/version"
)

const (
	imageName = "livekit/livekit-recorder"
)

// Default target to run when none is specified
// If not set, running mage will list available targets
var Default = Test

type modInfo struct {
	Path      string
	Version   string
	Time      time.Time
	Dir       string
	GoMod     string
	GoVersion string
}

// run unit tests
func Test() error {
	cmd := exec.Command("go", "test", "--tags=test", "./...")
	connectStd(cmd)
	return cmd.Run()
}

// builds docker images for LiveKit recorder and recorder service
func Docker() error {
	cmd := exec.Command("docker", "build", ".", "-t", fmt.Sprintf("%s:v%s", imageName, version.Version))
	connectStd(cmd)
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

func PublishDocker() error {
	mg.Deps(Docker)

	// don't publish snapshot versions as latest or minor version
	if !strings.Contains(version.Version, "SNAPSHOT") {
		return errors.New("cannot publish non-snapshot versions")
	}

	versionImg := fmt.Sprintf("%s:v%s", imageName, version.Version)
	cmd := exec.Command("docker", "push", versionImg)
	connectStd(cmd)
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

func connectStd(cmd *exec.Cmd) {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
}

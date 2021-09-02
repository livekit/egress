// +build mage

package main

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/magefile/mage/mg"

	"github.com/livekit/livekit-recorder/service/version"
)

const goChecksumFile = ".checksumgo"

var imageNames = []string{"livekit/livekit-recorder", "livekit/livekit-recorder-service"}

// Default target to run when none is specified
// If not set, running mage will list available targets
var Default = Build
var checksummer = NewChecksummer(".", goChecksumFile, ".go", ".mod")

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
	cmd := exec.Command("go", "test", "./...")
	connectStd(cmd)
	return cmd.Run()
}

// builds LiveKit recorder service
func Build() error {
	if !checksummer.IsChanged() {
		fmt.Println("up to date")
		return nil
	}

	fmt.Println("building...")
	if err := os.MkdirAll("bin", 0755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-o", "../../bin/livekit-recorder-service")
	cmd.Dir = "cmd/server"
	connectStd(cmd)
	if err := cmd.Run(); err != nil {
		return err
	}

	_ = checksummer.WriteChecksum()
	return nil
}

// builds docker images for LiveKit recorder and recorder service
func Docker() error {
	dirs := []string{"../recorder", "../"}
	for i, imageName := range imageNames {
		cmd := exec.Command("docker", "build", ".", "-t", fmt.Sprintf("%s:v%s", imageName, version.Version))
		cmd.Dir = dirs[i]
		connectStd(cmd)
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	return nil
}

func PublishDocker() error {
	mg.Deps(Docker)

	// don't publish snapshot versions as latest or minor version
	if !strings.Contains(version.Version, "SNAPSHOT") {
		return errors.New("cannot publish non-snapshot versions")
	}

	var e error
	for _, imageName := range imageNames {
		versionImg := fmt.Sprintf("%s:v%s", imageName, version.Version)
		cmd := exec.Command("docker", "push", versionImg)
		connectStd(cmd)
		if err := cmd.Run(); err != nil {
			e = err
		}
	}

	return e
}

// helpers

func connectStd(cmd *exec.Cmd) {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
}

// A helper checksum library that generates a fast, non-portable checksum over a directory of files
// it's designed as a quick way to bypass
type Checksummer struct {
	dir          string
	file         string
	checksum     string
	allExts      bool
	extMap       map[string]bool
	IgnoredPaths []string
}

func NewChecksummer(dir string, checksumfile string, exts ...string) *Checksummer {
	c := &Checksummer{
		dir:    dir,
		file:   checksumfile,
		extMap: make(map[string]bool),
	}
	if len(exts) == 0 {
		c.allExts = true
	} else {
		for _, ext := range exts {
			c.extMap[ext] = true
		}
	}

	return c
}

func (c *Checksummer) IsChanged() bool {
	// default changed
	if err := c.computeChecksum(); err != nil {
		log.Println("could not compute checksum", err)
		return true
	}
	// read
	existing, err := c.ReadChecksum()
	if err != nil {
		// may not be there
		return true
	}

	return existing != c.checksum
}

func (c *Checksummer) ReadChecksum() (string, error) {
	b, err := ioutil.ReadFile(filepath.Join(c.dir, c.file))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *Checksummer) WriteChecksum() error {
	if err := c.computeChecksum(); err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(c.dir, c.file), []byte(c.checksum), 0644)
}

func (c *Checksummer) computeChecksum() error {
	if c.checksum != "" {
		return nil
	}

	entries := make([]string, 0)
	ignoredMap := make(map[string]bool)
	for _, f := range c.IgnoredPaths {
		ignoredMap[f] = true
	}
	err := filepath.Walk(c.dir, func(path string, info os.FileInfo, err error) error {
		if path == c.dir {
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") || ignoredMap[path] {
			if info.IsDir() {
				return filepath.SkipDir
			} else {
				return nil
			}
		}
		if info.IsDir() {
			entries = append(entries, fmt.Sprintf("%s %d", path, info.ModTime().Unix()))
		} else if c.allExts || c.extMap[filepath.Ext(info.Name())] {
			entries = append(entries, fmt.Sprintf("%s %d %d", path, info.Size(), info.ModTime().Unix()))
		}
		return nil
	})
	if err != nil {
		return err
	}

	sort.Strings(entries)

	h := sha1.New()
	for _, e := range entries {
		h.Write([]byte(e))
	}
	c.checksum = fmt.Sprintf("%x", h.Sum(nil))

	return nil
}

// +build mage

package main

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/target"

	"github.com/livekit/livekit-recorder/service/version"
)

const goChecksumFile = ".checksumgo"

var imageNames = []string{"livekit/recorder", "livekit/recorder-service"}

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

func Deps() error {
	return installTools(false)
}

// regenerate protobuf
func Proto() error {
	mg.Deps(Deps)
	cmd := exec.Command("go", "list", "-m", "-json", "github.com/livekit/protocol")
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	info := modInfo{}
	if err = json.Unmarshal(out, &info); err != nil {
		return err
	}
	protoDir := info.Dir
	updated, err := target.Path(
		"proto/livekit_models.pb.go",
		protoDir+"/livekit_models.proto",
		protoDir+"/livekit_room.proto",
		protoDir+"/livekit_rtc.proto",
		protoDir+"/livekit_internal.proto",
	)
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}

	fmt.Println("generating protobuf")
	goOut := "proto"
	if err := os.MkdirAll(goOut, 0755); err != nil {
		return err
	}

	protoc, err := getToolPath("protoc")
	if err != nil {
		return err
	}
	protocGoPath, err := getToolPath("protoc-gen-go")
	if err != nil {
		return err
	}

	// generate internal
	cmd = exec.Command(protoc,
		"--go_out", goOut,
		"--go_opt=paths=source_relative",
		"--plugin=go="+protocGoPath,
		"-I="+protoDir,
		protoDir+"/livekit_models.proto",
		protoDir+"/livekit_room.proto",
		protoDir+"/livekit_rtc.proto",
		protoDir+"/livekit_internal.proto",
	)
	connectStd(cmd)
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

// run unit tests
func Test() error {
	mg.Deps(Proto)
	cmd := exec.Command("go", "test", "./...")
	connectStd(cmd)
	return cmd.Run()
}

// builds LiveKit recorder service
func Build() error {
	mg.Deps(Proto)
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
	mg.Deps(Proto)

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

func installTools(force bool) error {
	if _, err := getToolPath("protoc"); err != nil {
		return fmt.Errorf("protoc is required but is not found")
	}

	tools := []string{"google.golang.org/protobuf/cmd/protoc-gen-go"}
	for _, t := range tools {
		if err := installTool(t, force); err != nil {
			return err
		}
	}
	return nil
}

func installTool(url string, force bool) error {
	name := filepath.Base(url)
	if !force {
		_, err := getToolPath(name)
		if err == nil {
			// already installed
			return nil
		}
	}

	fmt.Printf("installing %s\n", name)
	cmd := exec.Command("go", "get", "-u", url)
	connectStd(cmd)
	if err := cmd.Run(); err != nil {
		return err
	}

	// check
	_, err := getToolPath(name)
	return err
}

// helpers

func getToolPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	// check under gopath
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = build.Default.GOPATH
	}
	p := filepath.Join(gopath, "bin", name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

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

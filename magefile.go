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

//go:build mage

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/livekit/egress/version"
	"github.com/livekit/mageutil"
)

const (
	gstVersion      = "1.24.12"
	libniceVersion  = "0.1.21"
	chromiumVersion = "125.0.6422.141"
	dockerBuild     = "docker build"
	dockerBuildX    = "docker buildx build --push --platform linux/amd64,linux/arm64"
)

type packageInfo struct {
	Dir string
}

func Proto() error {
	ctx := context.Background()
	fmt.Println("generating protobuf")

	// parse go mod output
	pkgOut, err := mageutil.Out(ctx, "go list -json -m github.com/livekit/protocol")
	if err != nil {
		return err
	}
	pi := packageInfo{}
	if err = json.Unmarshal(pkgOut, &pi); err != nil {
		return err
	}

	_, err = mageutil.GetToolPath("protoc")
	if err != nil {
		return err
	}
	protocGoPath, err := mageutil.GetToolPath("protoc-gen-go")
	if err != nil {
		return err
	}
	protocGrpcGoPath, err := mageutil.GetToolPath("protoc-gen-go-grpc")
	if err != nil {
		return err
	}

	// generate grpc-related protos
	return mageutil.RunDir(ctx, "pkg/ipc", fmt.Sprintf(
		"protoc"+
			" --go_out ."+
			" --go-grpc_out ."+
			" --go_opt=paths=source_relative"+
			" --go-grpc_opt=paths=source_relative"+
			" --plugin=go=%s"+
			" --plugin=go-grpc=%s"+
			" -I%s -I=. ipc.proto",
		protocGoPath, protocGrpcGoPath, pi.Dir+"/protobufs",
	))
}

func EnsureMediaSamples() error {
	ctx := context.Background()

	const script = "build/test/fetch-media-samples.sh"
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("missing %s: %w", script, err)
	}

	if err := mageutil.Run(ctx, script); err != nil {
		return err
	}

	if entries, _ := os.ReadDir("media-samples"); len(entries) == 0 {
		return fmt.Errorf("media-samples is empty after %s", script)
	}
	return nil
}

func Integration(configFile string) error {
	if err := EnsureMediaSamples(); err != nil {
		return err
	}

	ctx := context.Background()
	os.Setenv("DOCKER_BUILDKIT", "1")
	defer os.Unsetenv("DOCKER_BUILDKIT")

	if err := mageutil.Run(ctx,
		fmt.Sprintf("docker build --build-arg TEMPLATE_TAG=%s --build-arg DEADLOCK=1 -t egress-test -f build/test/Dockerfile .", version.TemplateVersion),
	); err != nil {
		return err
	}
	return Retest(configFile)
}

func Retest(configFile string) error {
	if configFile != "" {
		if strings.HasPrefix(configFile, "test/") {
			configFile = configFile[5:]
		} else {
			oldLocation := configFile
			idx := strings.LastIndex(configFile, "/")
			if idx != -1 {
				configFile = configFile[idx+1:]
			}
			if err := os.Rename(oldLocation, "test/"+configFile); err != nil {
				return err
			}
		}

		configFile = "/out/" + configFile
	}

	defer Dotfiles()
	defer func() {
		// for some reason, these can't be deleted from within the docker container
		files, _ := os.ReadDir("test/output")
		for _, file := range files {
			if file.IsDir() {
				d, _ := os.ReadDir(path.Join("test/output", file.Name()))
				if len(d) == 0 {
					_ = os.RemoveAll(path.Join("test/output", file.Name()))
				}
			}
		}
	}()

	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker run --rm -e EGRESS_CONFIG_FILE=%s -v %s/test:/out egress-test", configFile, dir),
	)
}

func Build() error {
	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/chrome-installer:%s", chromiumVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker build -t livekit/egress:latest --build-arg TEMPLATE_TAG=%s -f build/egress/Dockerfile .", version.TemplateVersion),
	)
}

func BuildTemplate() error {
	return mageutil.Run(context.Background(),
		"docker pull ubuntu:24.04",
		"docker build -t livekit/egress-templates -f ./build/template/Dockerfile .",
	)
}

func BuildGStreamer() error {
	return buildGstreamer(dockerBuild)
}

func buildGstreamer(cmd string) error {
	commands := []string{"docker pull ubuntu:23.10"}
	for _, build := range []string{"base", "dev", "prod", "prod-rs"} {
		commands = append(commands, fmt.Sprintf("%s"+
			" --build-arg GSTREAMER_VERSION=%s"+
			" --build-arg LIBNICE_VERSION=%s"+
			" -t livekit/gstreamer:%s-%s"+
			" -t livekit/gstreamer:%s-%s-%s"+
			" -f build/gstreamer/Dockerfile-%s"+
			" ./build/gstreamer",
			cmd, gstVersion, libniceVersion, gstVersion, build, gstVersion, build, runtime.GOARCH, build,
		))
	}

	return mageutil.Run(context.Background(), commands...)
}

func Dotfiles() error {
	files, err := os.ReadDir("test/output")
	if err != nil {
		return err
	}

	dots := make(map[string]bool)
	pngs := make(map[string]bool)
	for _, file := range files {
		name := file.Name()
		if strings.HasSuffix(name, ".dot") {
			dots[name[:len(name)-4]] = true
		} else if strings.HasSuffix(file.Name(), ".png") {
			pngs[name[:len(name)-4]] = true
		}
	}

	for name := range dots {
		if !pngs[name] {
			if err := mageutil.Run(context.Background(), fmt.Sprintf(
				"dot -Tpng test/output/%s.dot -o test/output/%s.png",
				name, name,
			)); err != nil {
				return err
			}
		}
	}

	return nil
}

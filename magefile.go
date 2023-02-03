//go:build mage

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/livekit/mageutil"
)

const gstVersion = "1.20.4"

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
		protocGoPath, protocGrpcGoPath, pi.Dir,
	))
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
		"docker build -t egress-test -f build/test/Dockerfile .",
		fmt.Sprintf("docker run --rm -e EGRESS_CONFIG_FILE=%s -v %s/test:/out egress-test", configFile, dir),
	)
}

func Build() error {
	return mageutil.Run(context.Background(),
		"docker pull livekit/chrome-installer",
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		"docker pull livekit/egress-templates",
		"docker build -t livekit/egress:latest -f build/egress/Dockerfile .",
	)
}

func BuildChrome() error {
	return mageutil.Run(context.Background(),
		"docker pull ubuntu:22.04",
		"docker build -t livekit/chrome-installer ./build/chrome",
	)
}

func PublishChrome() error {
	return mageutil.Run(context.Background(),
		"docker pull ubuntu:22.04",
		"docker buildx build --push --platform linux/amd64,linux/arm64 -t livekit/chrome-installer ./build/chrome",
	)
}

func BuildTemplate() error {
	return mageutil.Run(context.Background(),
		"docker pull ubuntu:22.04",
		"docker build -t livekit/egress-templates -f ./build/template/Dockerfile .",
	)
}

func BuildGStreamer() error {
	return buildGstreamer("docker build")
}

func PublishGStreamer() error {
	return buildGstreamer("docker buildx build --push --platform linux/amd64,linux/arm64")
}

func buildGstreamer(cmd string) error {
	commands := []string{"docker pull ubuntu:22.04"}
	for _, build := range []string{"base", "dev", "prod"} {
		commands = append(commands, fmt.Sprintf("%s"+
			" --build-arg GSTREAMER_VERSION=%s"+
			" -t livekit/gstreamer:%s-%s"+
			" -f build/gstreamer/Dockerfile-%s"+
			" ./build/gstreamer",
			cmd, gstVersion, gstVersion, build, build,
		))
	}

	return mageutil.Run(context.Background(), commands...)
}

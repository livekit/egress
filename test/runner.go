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

//go:build integration

package test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type Runner struct {
	svc             *service.Service         `yaml:"-"`
	client          rpc.EgressClient         `yaml:"-"`
	room            *lksdk.Room              `yaml:"-"`
	updates         chan *livekit.EgressInfo `yaml:"-"`
	sourceFramerate float64                  `yaml:"-"`

	// service config
	*config.ServiceConfig `yaml:",inline"`
	S3Upload              *livekit.S3Upload        `yaml:"-"`
	GCPUpload             *livekit.GCPUpload       `yaml:"-"`
	AzureUpload           *livekit.AzureBlobUpload `yaml:"-"`

	// testing config
	FilePrefix              string `yaml:"file_prefix"`
	RoomName                string `yaml:"room_name"`
	RoomTestsOnly           bool   `yaml:"room_only"`
	WebTestsOnly            bool   `yaml:"web_only"`
	ParticipantTestsOnly    bool   `yaml:"participant_only"`
	TrackCompositeTestsOnly bool   `yaml:"track_composite_only"`
	TrackTestsOnly          bool   `yaml:"track_only"`
	FileTestsOnly           bool   `yaml:"file_only"`
	StreamTestsOnly         bool   `yaml:"stream_only"`
	SegmentTestsOnly        bool   `yaml:"segments_only"`
	ImageTestsOnly          bool   `yaml:"images_only"`
	MultiTestsOnly          bool   `yaml:"multi_only"`
	Muting                  bool   `yaml:"muting"`
	Dotfiles                bool   `yaml:"dot_files"`
	Short                   bool   `yaml:"short"`
}

func NewRunner(t *testing.T) *Runner {
	confString := os.Getenv("EGRESS_CONFIG_STRING")
	if confString == "" {
		confFile := os.Getenv("EGRESS_CONFIG_FILE")
		require.NotEmpty(t, confFile)
		b, err := os.ReadFile(confFile)
		require.NoError(t, err)
		confString = string(b)
	}

	r := &Runner{}
	err := yaml.Unmarshal([]byte(confString), r)
	require.NoError(t, err)

	switch os.Getenv("INTEGRATION_TYPE") {
	case "room":
		r.RoomTestsOnly = true
		r.RoomName = fmt.Sprintf("room-integration-%d", rand.Intn(100))
	case "web":
		r.WebTestsOnly = true
		r.RoomName = fmt.Sprintf("web-integration-%d", rand.Intn(100))
	case "participant":
		r.ParticipantTestsOnly = true
		r.RoomName = fmt.Sprintf("participant-integration-%d", rand.Intn(100))
	case "track_composite":
		r.TrackCompositeTestsOnly = true
		r.RoomName = fmt.Sprintf("track-composite-integration-%d", rand.Intn(100))
	case "track":
		r.TrackTestsOnly = true
		r.RoomName = fmt.Sprintf("track-integration-%d", rand.Intn(100))
	default:
		if r.RoomName == "" {
			r.RoomName = fmt.Sprintf("egress-integration-%d", rand.Intn(100))
		}
	}

	conf, err := config.NewServiceConfig(confString)
	require.NoError(t, err)

	r.ServiceConfig = conf

	if conf.ApiKey == "" || conf.ApiSecret == "" || conf.WsUrl == "" {
		t.Fatal("api key, secret, and ws url required")
	}
	if conf.Redis == nil {
		t.Fatal("redis required")
	}

	if s3 := os.Getenv("S3_UPLOAD"); s3 != "" {
		logger.Infow("using s3 uploads")
		r.S3Upload = &livekit.S3Upload{}
		require.NoError(t, json.Unmarshal([]byte(s3), r.S3Upload))
	} else {
		logger.Infow("no s3 config supplied")
	}

	if gcp := os.Getenv("GCP_UPLOAD"); gcp != "" {
		logger.Infow("using gcp uploads")
		r.GCPUpload = &livekit.GCPUpload{}
		require.NoError(t, json.Unmarshal([]byte(gcp), r.GCPUpload))
	} else {
		logger.Infow("no gcp config supplied")
	}

	if azure := os.Getenv("AZURE_UPLOAD"); azure != "" {
		logger.Infow("using azure uploads")
		r.AzureUpload = &livekit.AzureBlobUpload{}
		require.NoError(t, json.Unmarshal([]byte(azure), r.AzureUpload))
	} else {
		logger.Infow("no azure config supplied")
	}

	return r
}

func (r *Runner) Run(t *testing.T, svc *service.Service, bus psrpc.MessageBus, templateFs fs.FS) {
	lksdk.SetLogger(logger.LogRLogger(logr.Discard()))
	r.svc = svc

	// connect to room
	room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	defer room.Disconnect()

	psrpcClient, err := rpc.NewEgressClient(rpc.ClientParams{Bus: bus})
	require.NoError(t, err)

	// start debug handler
	r.svc.StartDebugHandlers()

	// start templates handler
	err = r.svc.StartTemplatesServer(templateFs)
	require.NoError(t, err)

	go r.svc.Run()
	t.Cleanup(func() {
		r.svc.Stop(true)
		r.svc.Close()
	})
	time.Sleep(time.Second * 3)

	// subscribe to update channel
	psrpcUpdates := make(chan *livekit.EgressInfo, 100)
	_, err = newIOTestServer(bus, psrpcUpdates)
	require.NoError(t, err)

	// update test config
	r.client = psrpcClient
	r.updates = psrpcUpdates
	r.room = room

	// check status
	if r.HealthPort != 0 {
		status := r.getStatus(t)
		require.Len(t, status, 1)
		require.Contains(t, status, "CpuLoad")
	}

	// run tests
	r.testRoomComposite(t)
	r.testWeb(t)
	r.testParticipant(t)
	r.testTrackComposite(t)
	r.testTrack(t)
}

func (r *Runner) runRoomTests() bool {
	return !r.ParticipantTestsOnly && !r.TrackCompositeTestsOnly && !r.TrackTestsOnly && !r.WebTestsOnly
}

func (r *Runner) runWebTests() bool {
	return !r.RoomTestsOnly && !r.ParticipantTestsOnly && !r.TrackCompositeTestsOnly && !r.TrackTestsOnly
}

func (r *Runner) runParticipantTests() bool {
	return !r.RoomTestsOnly && !r.TrackCompositeTestsOnly && !r.TrackTestsOnly && !r.WebTestsOnly
}

func (r *Runner) runTrackCompositeTests() bool {
	return !r.RoomTestsOnly && !r.ParticipantTestsOnly && !r.TrackTestsOnly && !r.WebTestsOnly
}

func (r *Runner) runTrackTests() bool {
	return !r.RoomTestsOnly && !r.ParticipantTestsOnly && !r.TrackCompositeTestsOnly && !r.WebTestsOnly
}

func (r *Runner) runFileTests() bool {
	return !r.StreamTestsOnly && !r.SegmentTestsOnly && !r.MultiTestsOnly && !r.ImageTestsOnly
}

func (r *Runner) runStreamTests() bool {
	return !r.FileTestsOnly && !r.SegmentTestsOnly && !r.MultiTestsOnly && !r.ImageTestsOnly
}

func (r *Runner) runSegmentTests() bool {
	return !r.FileTestsOnly && !r.StreamTestsOnly && !r.MultiTestsOnly && !r.ImageTestsOnly
}

func (r *Runner) runImageTests() bool {
	return !r.FileTestsOnly && !r.StreamTestsOnly && !r.SegmentTestsOnly && !r.MultiTestsOnly
}

func (r *Runner) runMultiTests() bool {
	return !r.FileTestsOnly && !r.StreamTestsOnly && !r.SegmentTestsOnly && !r.ImageTestsOnly
}

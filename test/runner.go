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
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"github.com/livekit/egress/pkg/config"
)

type Runner struct {
	StartEgress func(ctx context.Context, request *rpc.StartEgressRequest) (*livekit.EgressInfo, error) `yaml:"-"`

	svc             Server                   `yaml:"-"`
	client          rpc.EgressClient         `yaml:"-"`
	room            *lksdk.Room              `yaml:"-"`
	p1Room          *lksdk.Room              `yaml:"-"`
	p2Room          *lksdk.Room              `yaml:"-"`
	updates         chan *livekit.EgressInfo `yaml:"-"`
	sourceFramerate float64                  `yaml:"-"`
	testNumber      int                      `yaml:"-"`

	// service config
	*config.ServiceConfig `yaml:",inline"`
	S3Upload              *livekit.S3Upload        `yaml:"-"`
	GCPUpload             *livekit.GCPUpload       `yaml:"-"`
	AzureUpload           *livekit.AzureBlobUpload `yaml:"-"`

	// testing config
	FilePrefix   string `yaml:"file_prefix"`
	RoomName     string `yaml:"room_name"`
	RoomBaseName string `yaml:"-"`
	Muting       bool   `yaml:"muting"`
	Dotfiles     bool   `yaml:"dot_files"`
	Short        bool   `yaml:"short"`

	// Test selection
	filter     string `yaml:"-"`
	shardIndex int    `yaml:"-"`
	shardTotal int    `yaml:"-"`
}

type Server interface {
	StartTemplatesServer(fs.FS) error
	Run() error
	Status() ([]byte, error)
	GetGstPipelineDotFile(string) (string, error)
	IsIdle() bool
	KillAll()
	Shutdown(bool, bool)
	Drain()
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

	if f := os.Getenv("EGRESS_TEST"); f != "" {
		r.filter = f
	}
	if shard := os.Getenv("EGRESS_TEST_SHARD"); shard != "" {
		parts := strings.SplitN(shard, "/", 2)
		if len(parts) == 2 {
			r.shardIndex, _ = strconv.Atoi(parts[0])
			r.shardTotal, _ = strconv.Atoi(parts[1])
		}
	}

	if r.RoomName == "" {
		r.RoomName = fmt.Sprintf("egress-integration-%d", rand.Intn(100))
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

	if r.RoomBaseName == "" {
		r.RoomBaseName = r.RoomName
	}

	return r
}

func (r *Runner) connectMultiParticipants(t *testing.T) {
	p1, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-p1",
		ParticipantIdentity: fmt.Sprintf("p1-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(p1.Disconnect)
	r.p1Room = p1

	p2, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-p2",
		ParticipantIdentity: fmt.Sprintf("p2-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(p2.Disconnect)
	r.p2Room = p2
}

func (r *Runner) disconnectMultiParticipants() {
	if r.p1Room != nil {
		r.p1Room.Disconnect()
		r.p1Room = nil
	}
	if r.p2Room != nil {
		r.p2Room.Disconnect()
		r.p2Room = nil
	}
}

func (r *Runner) connectRoom(t *testing.T, roomName string, codecs []livekit.Codec) {
	if r.room != nil {
		r.room.Disconnect()
	}

	opts := []lksdk.ConnectOption{}
	if len(codecs) > 0 {
		opts = append(opts, lksdk.WithCodecs(codecs))
	}

	room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            roomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback(), opts...)
	require.NoError(t, err)

	r.room = room
	r.RoomName = roomName
}

func (r *Runner) StartServer(t *testing.T, svc Server, bus psrpc.MessageBus, templateFs fs.FS) {
	r.svc = svc
	t.Cleanup(func() {
		if r.room != nil {
			r.room.Disconnect()
		}
		r.svc.Shutdown(false, true)
	})

	r.connectRoom(t, r.RoomName, nil)

	psrpcClient, err := rpc.NewEgressClient(rpc.ClientParams{Bus: bus})
	require.NoError(t, err)
	r.StartEgress = func(ctx context.Context, req *rpc.StartEgressRequest) (*livekit.EgressInfo, error) {
		return psrpcClient.StartEgress(ctx, "", req)
	}

	// start templates handler
	err = r.svc.StartTemplatesServer(templateFs)
	require.NoError(t, err)

	go r.svc.Run()
	time.Sleep(time.Second * 3)

	// subscribe to update channel
	psrpcUpdates := make(chan *livekit.EgressInfo, 100)
	_, err = newIOTestServer(bus, psrpcUpdates)
	require.NoError(t, err)

	// update test config
	r.client = psrpcClient
	r.updates = psrpcUpdates

	// check status
	if r.HealthPort != 0 {
		status := r.getStatus(t)
		require.Len(t, status, 1)
		require.Contains(t, status, "CpuLoad")
	}
}

func (r *Runner) shouldRunTest(index int, name string) bool {
	if r.filter != "" {
		if !strings.Contains(name, r.filter) {
			return false
		}
	}
	if r.shardTotal > 0 && index%r.shardTotal != r.shardIndex {
		return false
	}
	return true
}

func (r *Runner) getUploadConfig() interface{} {
	configs := make([]interface{}, 0)
	if r.S3Upload != nil {
		configs = append(configs, r.S3Upload)
	}
	if r.GCPUpload != nil {
		configs = append(configs, r.GCPUpload)
	}
	if r.AzureUpload != nil {
		configs = append(configs, r.AzureUpload)
	}
	if len(configs) == 0 {
		return nil
	}
	return configs[r.testNumber%len(configs)]
}

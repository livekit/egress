// Copyright 2026 LiveKit, Inc.
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

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

type nopIPCClient struct{}

func (nopIPCClient) HandlerReady(context.Context, *ipc.HandlerReadyRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (nopIPCClient) HandlerUpdate(context.Context, *livekit.EgressInfo, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (nopIPCClient) HandlerFinished(context.Context, *ipc.HandlerFinishedRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (nopIPCClient) ReplayReady(context.Context, *rpc.EgressReadyRequest, ...grpc.CallOption) (*rpc.EgressReadyResponse, error) {
	return &rpc.EgressReadyResponse{}, nil
}
func (nopIPCClient) StorageEvent(context.Context, *ipc.StorageEventRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// staticSource satisfies source.Source without connecting to a room. The
// pipeline input is the audio bin's silence generator (audiotestsrc).
type staticSource struct {
	startedAt int64
}

func (s *staticSource) StartRecording() <-chan struct{} { return nil }
func (s *staticSource) EndRecording() <-chan struct{}   { return nil }
func (s *staticSource) GetStartedAt() int64             { return s.startedAt }
func (s *staticSource) GetEndedAt() int64               { return time.Now().UnixNano() }
func (s *staticSource) Close()                          {}

// TestUnsolicitedEOSBeforeSendEOS reproduces the shutdown race where EOS
// reaches the sinks before the controller requests it — e.g. a source erroring
// at end of stream pushes EOS downstream (see the not-negotiated caps
// rejection on end-of-track placeholder frames). SendEOS arriving after the
// bus EOS must not arm the frozen-pipeline watchdog: the EOS probes it would
// install can never fire, the timer can never be stopped, and a healthy
// shutdown would be reported as "pipeline frozen" after eosTimeout.
func TestUnsolicitedEOSBeforeSendEOS(t *testing.T) {
	prevTimeout := eosTimeout
	eosTimeout = 2 * time.Second
	t.Cleanup(func() { eosTimeout = prevTimeout })

	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "out.ogg")

	// audio-only room composite: SDK source type, silence generator input
	req := &rpc.StartEgressRequest{
		EgressId: "EG_controllerTest",
		Token:    "token",
		WsUrl:    "wss://test",
		Request: &rpc.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName:  "controller-test",
				AudioOnly: true,
				FileOutputs: []*livekit.EncodedFileOutput{{
					Filepath: outputPath,
				}},
			},
		},
	}
	confString := fmt.Sprintf("tmp_dir: %s\ntemplate_base: http://localhost", tmpDir)
	conf, err := config.NewPipelineConfig(confString, req)
	if err != nil {
		t.Fatalf("failed to build pipeline config: %v", err)
	}

	src := &staticSource{startedAt: time.Now().UnixNano()}
	c, err := NewWithSource(context.Background(), conf, nopIPCClient{}, func(*gstreamer.Callbacks) (source.Source, error) {
		return src, nil
	})
	if err != nil {
		t.Fatalf("failed to build controller: %v", err)
	}

	infoCh := make(chan *livekit.EgressInfo, 1)
	go func() {
		infoCh <- c.Run(context.Background())
	}()

	select {
	case <-c.playing.Watch():
	case <-time.After(10 * time.Second):
		t.Fatal("pipeline did not reach PLAYING")
	}

	// As soon as the bus EOS is processed (before Close marks the egress
	// complete), invoke the normal end path — mirroring the source-side
	// shutdown that raced the unsolicited EOS in production.
	sendEOSDone := make(chan struct{})
	go func() {
		defer close(sendEOSDone)
		<-c.eosReceived.Watch()
		c.SendEOS(context.Background(), "test end")
	}()

	// let some media reach the sink, then send EOS through the pipeline,
	// bypassing the controller
	time.Sleep(500 * time.Millisecond)
	c.p.SendEOS()

	select {
	case <-sendEOSDone:
	case <-time.After(10 * time.Second):
		t.Fatal("bus EOS not received")
	}

	var info *livekit.EgressInfo
	select {
	case info = <-infoCh:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	// a mistakenly armed watchdog would misfire within eosTimeout and mark
	// the egress failed with "pipeline frozen"
	time.Sleep(eosTimeout + time.Second)

	if info.Status != livekit.EgressStatus_EGRESS_COMPLETE {
		t.Fatalf("expected status %s, got %s (error: %q)",
			livekit.EgressStatus_EGRESS_COMPLETE, info.Status, info.Error)
	}
	if info.Error != "" {
		t.Fatalf("expected no error, got %q", info.Error)
	}

	stat, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	if stat.Size() == 0 {
		t.Fatal("output file is empty")
	}
}

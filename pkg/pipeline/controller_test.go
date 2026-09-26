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

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/types"
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
	startedAt    int64
	endRecording chan struct{}
}

func (s *staticSource) StartRecording() <-chan struct{} { return nil }
func (s *staticSource) EndRecording() <-chan struct{}   { return s.endRecording }
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
	// shutdown path.
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

// TestOnEOSSentBeforePipelineBuilt: a writer whose track EOFs during the build
// phase reaches onEOSSent() before BuildPipeline() has assigned c.p. It must
// neither dereference the pipeline nor start the EOS sequence.
//
// The config is the one combination that makes onEOSSent() forward to SendEOS():
// passthrough / track composite with no audio.
func TestOnEOSSentBeforePipelineBuilt(t *testing.T) {
	c := &Controller{
		PipelineConfig: &config.PipelineConfig{
			RequestType: types.RequestTypeTrackComposite,
			Passthrough: true,
			AudioConfig: config.AudioConfig{AudioEnabled: false},
		},
		// BuildReady still open and p still nil: the pipeline is mid-build
		callbacks: &gstreamer.Callbacks{BuildReady: make(chan struct{})},
	}

	require.NotPanics(t, c.onEOSSent,
		"cleanup running during the build phase must not dereference the pipeline")
	require.False(t, c.eosSent.IsBroken(),
		"onEOSSent must not start the EOS sequence before the pipeline exists")
	require.Nil(t, c.p, "sanity: the test covers the pre-build window")
}

// a replay small enough to fit the appsrc queues closes before the pipeline plays
func TestWatchEndRecording(t *testing.T) {
	// EGRESS_COMPLETE matches no arm of SendEOS's switch, so it never touches the nil pipeline
	newTestController := func(live bool) (*Controller, chan struct{}) {
		ended := make(chan struct{})
		c := &Controller{
			PipelineConfig: &config.PipelineConfig{
				Live: live,
				Info: &livekit.EgressInfo{Status: livekit.EgressStatus_EGRESS_COMPLETE},
			},
			src: &staticSource{endRecording: ended},
		}
		return c, ended
	}

	watch := func(c *Controller) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.watchEndRecording(context.Background())
		}()
		return done
	}

	// an unbuffered send completes only once the watcher has received
	release := func(t *testing.T, ended chan struct{}) {
		select {
		case ended <- struct{}{}:
		case <-time.After(10 * time.Second):
			t.Fatal("watcher never read EndRecording")
		}
	}

	t.Run("live sends EOS without waiting for playing", func(t *testing.T) {
		c, ended := newTestController(true)
		done := watch(c)
		release(t, ended)

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("live pipeline blocked instead of sending EOS")
		}
		require.True(t, c.eosSent.IsBroken())
	})

	t.Run("non-live waits for playing", func(t *testing.T) {
		c, ended := newTestController(false)
		done := watch(c)
		release(t, ended)

		time.Sleep(100 * time.Millisecond)
		require.False(t, c.eosSent.IsBroken(), "EOS before playing aborts the egress")

		c.playing.Break()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("playing did not release the EOS")
		}
		require.True(t, c.eosSent.IsBroken())
	})

	t.Run("non-live returns without EOS when stopped first", func(t *testing.T) {
		c, ended := newTestController(false)
		c.stopped.Break()
		done := watch(c)
		release(t, ended)

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("stopped pipeline left the watcher blocked")
		}
		require.False(t, c.eosSent.IsBroken())
	})

	t.Run("non-live aborts when the pipeline never plays", func(t *testing.T) {
		prev := prerollTimeout
		prerollTimeout = 50 * time.Millisecond
		t.Cleanup(func() { prerollTimeout = prev })

		c, ended := newTestController(false)
		done := watch(c)
		release(t, ended)

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("preroll timeout did not release the watcher")
		}
		require.True(t, c.eosSent.IsBroken())
	})
}

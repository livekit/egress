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

//go:build integration

package test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rtmp "github.com/livekit/go-rtmp"
	rtmpmsg "github.com/livekit/go-rtmp/message"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

const (
	publishRejectDeadline = 90 * time.Second
	// Long enough for the egress to reach EGRESS_ACTIVE, push real media so the
	// liveness monitor sees OutBytesTotal grow past livenessProgressThreshold,
	// then exit cleanly into the reconnect-loop phase.
	publishRejectHoldDuration = 8 * time.Second
)

// testRtmpPublishReject reproduces a protocol-level publish-reject pattern:
// TCP and the RTMP handshake/connect/createStream complete normally, but the
// publish command is rejected by the server. rtmp2sink reconnects immediately
// with no backoff, so a rapid reject loop ensues — the scenario where, in
// prod, gst-plugins-bad's rtmp2sink can wedge silently and stop emitting
// further errors.
//
// Expected outcomes after this branch:
//   - With the rapid-reject loop emitting errors continuously, the existing
//     30s give-up window in Reset() fails the stream.
//   - If rtmp2sink wedges silently during the burst, the active liveness
//     monitor in pkg/pipeline/builder/stream.go fails it within
//     livenessIdleTimeout.
//
// Either path takes the egress out of EGRESS_ACTIVE rather than letting it
// sit on ACTIVE indefinitely.
func (r *Runner) testRtmpPublishReject(t *testing.T, test *testCase) {
	rejectServer := startRtmpRejectServer(t, publishRejectHoldDuration)

	streamKey := utils.NewGuid("")
	rejectURL := fmt.Sprintf("rtmp://%s/live/%s", rejectServer.addr(), streamKey)
	test.streamUrls = []string{rejectURL}

	req := r.build(test)
	egressID := r.startEgress(t, req)
	rejectRedacted, _ := utils.RedactStreamKey(rejectURL)

	// Poll the latest egress snapshot until the egress stops reporting ACTIVE.
	// r.getUpdate returns the current snapshot held by the test IO server; the
	// inner sleep gives the egress time to publish state changes between polls.
	deadline := time.Now().Add(publishRejectDeadline)
	info := r.getUpdate(t, egressID)
	streamFailed := hasFailedStream(info, rejectRedacted)
	for info.Status == livekit.EgressStatus_EGRESS_ACTIVE || info.Status == livekit.EgressStatus_EGRESS_STARTING {
		if time.Now().After(deadline) {
			t.Fatalf("egress did not leave EGRESS_ACTIVE within %s after publish-reject storm", publishRejectDeadline)
		}
		time.Sleep(100 * time.Millisecond)
		info = r.getUpdate(t, egressID)
		if hasFailedStream(info, rejectRedacted) {
			streamFailed = true
		}
	}

	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status,
		"egress did not transition to FAILED after publish-reject storm")
	require.True(t, streamFailed || hasFailedStream(info, rejectRedacted),
		"stream %s was never reported FAILED in any update", rejectRedacted)
	require.NotEmpty(t, info.Error)

	// Sanity-check that we actually exercised the publish-reject shape: at
	// least one publish must have been accepted (so media flowed and the
	// liveness clock started), and at least one subsequent publish must have
	// been rejected (so the reconnect loop happened). Without these the test
	// would silently regress to the bad-stream-key fast-fail path.
	require.GreaterOrEqual(t, rejectServer.publishAttempts(), int32(2),
		"reject server saw only one publish — reconnect loop never fired")
	require.GreaterOrEqual(t, rejectServer.publishRejects(), int32(1),
		"no publish was rejected — wrong failure path was exercised")
}

// rtmpRejectServer is the in-process RTMP server backing testRtmpPublishReject:
//
//  1. First publish is accepted. The server holds the connection so the egress
//     reaches EGRESS_ACTIVE and the liveness monitor registers real progress
//     on OutBytesTotal (bytes rtmp2sink has written to the socket).
//  2. After holdDuration, the server drops the TCP connection (no FIN warning,
//     just close) — matching Mux's "connection closed remotely" in the prod
//     timeline.
//  3. rtmp2sink reconnects. Every subsequent publish is rejected with a
//     NetStream.Publish.Failed error, reproducing the rapid reject loop.
type rtmpRejectServer struct {
	listener      net.Listener
	server        *rtmp.Server
	wg            sync.WaitGroup
	holdDuration  time.Duration
	acceptedFirst atomic.Bool
	publishCount  atomic.Int32
	rejectCount   atomic.Int32
}

// startRtmpRejectServer binds to a random localhost port and serves until the
// test completes. holdDuration controls how long the first publish is allowed
// to stream before the server kicks the connection.
func startRtmpRejectServer(t *testing.T, holdDuration time.Duration) *rtmpRejectServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to bind reject RTMP server")

	s := &rtmpRejectServer{
		listener:     listener,
		holdDuration: holdDuration,
	}
	s.server = rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			return conn, &rtmp.ConnConfig{
				Handler: &rejectHandler{server: s, conn: conn},
			}
		},
	})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Serve returns on Close; the error there is expected and ignored.
		_ = s.server.Serve(listener)
	}()

	t.Cleanup(func() {
		_ = s.server.Close()
		_ = listener.Close()
		s.wg.Wait()
	})
	return s
}

func (s *rtmpRejectServer) addr() string {
	return s.listener.Addr().String()
}

// publishAttempts returns how many times rtmp2sink got far enough to send the
// publish command.
func (s *rtmpRejectServer) publishAttempts() int32 {
	return s.publishCount.Load()
}

// publishRejects returns how many publish attempts were rejected (i.e. all
// attempts after the first one).
func (s *rtmpRejectServer) publishRejects() int32 {
	return s.rejectCount.Load()
}

type rejectHandler struct {
	rtmp.DefaultHandler
	server *rtmpRejectServer
	conn   net.Conn
}

func (h *rejectHandler) OnPublish(_ *rtmp.StreamContext, _ uint32, _ *rtmpmsg.NetStreamPublish) error {
	h.server.publishCount.Add(1)
	if h.server.acceptedFirst.CompareAndSwap(false, true) {
		// First publish: accept and schedule a TCP drop after holdDuration so
		// the egress's existing reconnect path takes over. Subsequent publishes
		// (from the reconnect loop) hit the reject path below.
		go func() {
			time.Sleep(h.server.holdDuration)
			_ = h.conn.Close()
		}()
		return nil
	}
	h.server.rejectCount.Add(1)
	return errors.New("publish rejected by test server (post-disconnect)")
}

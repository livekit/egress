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
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Two simulcast layers of one 9:16 picture, sized the way ingress sizes a portrait source:
// the same display shape, carried with a different pixel aspect ratio in each SPS.
const (
	aspectSwitchLowSample  = "livekit_par_switch_video_202x360_sar405x404_25fps.h264"
	aspectSwitchHighSample = "livekit_par_switch_video_406x720_sar405x406_25fps.h264"

	aspectSwitchHighLoops = 3
)

// testTrackH264AspectSwitch checks a passthrough H.264 track egress survives a layer switch that changes the pixel aspect ratio
func (r *Runner) testTrackH264AspectSwitch(t *testing.T, test *testCase) {
	// mp4mux refuses a pixel-aspect-ratio change on caps renegotiation, so the egress fails at the switch
	t.Skip("passthrough MP4 does not survive a pixel-aspect-ratio change yet")

	aspectSwitchLow, err := os.ReadFile(path.Join(mediaSamplesDir, aspectSwitchLowSample))
	require.NoError(t, err)
	aspectSwitchHigh, err := os.ReadFile(path.Join(mediaSamplesDir, aspectSwitchHighSample))
	require.NoError(t, err)

	room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-aspect-switch",
		ParticipantIdentity: fmt.Sprintf("aspect-switch-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(room.Disconnect)

	// the low layer loops until the egress is active, then the track switches to the high layer at a keyframe
	pr, pw := io.Pipe()
	switchLayer := make(chan struct{})
	go func() {
		defer pw.Close()
		for {
			select {
			case <-switchLayer:
				for range aspectSwitchHighLoops {
					if _, err := pw.Write(aspectSwitchHigh); err != nil {
						return
					}
				}
				return
			default:
			}
			if _, err := pw.Write(aspectSwitchLow); err != nil {
				return
			}
		}
	}()

	done := make(chan struct{})
	track, err := lksdk.NewLocalReaderTrack(pr, webrtc.MimeTypeH264,
		lksdk.ReaderTrackWithFrameDuration(frameDurations[types.MimeTypeH264]),
		lksdk.ReaderTrackWithOnWriteComplete(func() { close(done) }),
	)
	require.NoError(t, err)
	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "aspect-switch"})
	require.NoError(t, err)
	test.videoTrackID = pub.SID()
	test.videoCodec = types.MimeTypeH264

	req := r.buildRequest(test)
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)
	p.GetFileConfig().OutputType = test.fileOptions.outputType

	egressID := r.startEgress(t, req)
	close(switchLayer)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out publishing the high layer")
	}
	require.NoError(t, room.LocalParticipant.UnpublishTrack(pub.SID()))

	// the track egress ends on its own once the track is unpublished
	var res *livekit.EgressInfo
	deadline := time.Now().Add(60 * time.Second)
	for res == nil && time.Now().Before(deadline) {
		r.updates.Lock()
		info := r.updates.EgressInfo
		r.updates.Unlock()
		if info != nil && info.EgressId == egressID {
			switch info.Status {
			case livekit.EgressStatus_EGRESS_COMPLETE,
				livekit.EgressStatus_EGRESS_FAILED,
				livekit.EgressStatus_EGRESS_ABORTED:
				res = info
			}
		}
		if res == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
	require.NotNil(t, res, "timed out waiting for egress to end")
	require.Equal(t, livekit.EgressStatus_EGRESS_COMPLETE.String(), res.Status.String(),
		"egress did not survive the layer switch: %s", res.Error)

	// the recording covers the high layer, not only the time before the switch
	minDuration := time.Duration(aspectSwitchHighLoops) * 2 * time.Second
	test.contentCheck = func(t *testing.T, _ string, info *FFProbeInfo) {
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)
		require.Greater(t, actual, minDuration.Seconds(), "recording stops at the layer switch")
	}
	r.verifyFile(t, test, p, res)
}

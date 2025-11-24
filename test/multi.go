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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func (r *Runner) testMulti(t *testing.T) {
	if !r.should(runMulti) {
		return
	}

	t.Run("Multi", func(t *testing.T) {
		for _, test := range []*testCase{

			// ---- Room Composite -----

			{
				name:        "RoomComposite",
				requestType: types.RequestTypeRoomComposite, publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
				},
				fileOptions: &fileOptions{
					filename: "rc_multiple_{time}",
				},
				imageOptions: &imageOptions{
					prefix: "rc_image",
				},
				multi: true,
			},

			// ---------- Web ----------

			{
				name:        "Web",
				requestType: types.RequestTypeWeb,
				fileOptions: &fileOptions{
					filename: "web_multiple_{time}",
				},
				segmentOptions: &segmentOptions{
					prefix:   "web_multiple_{time}",
					playlist: "web_multiple_{time}.m3u8",
				},
				multi: true,
			},

			// ------ Participant ------

			{
				name:        "ParticipantComposite",
				requestType: types.RequestTypeParticipant, publishOptions: publishOptions{
					audioCodec:     types.MimeTypeOpus,
					audioUnpublish: time.Second * 20,
					videoCodec:     types.MimeTypeVP8,
					videoDelay:     time.Second * 5,
				},
				fileOptions: &fileOptions{
					filename: "participant_{publisher_identity}_multi_{time}.mp4",
				},
				streamOptions: &streamOptions{
					outputType: types.OutputTypeRTMP,
				},
				segmentOptions: &segmentOptions{
					prefix:   "participant_{publisher_identity}_multi_{time}",
					playlist: "participant_{publisher_identity}_multi_{time}.m3u8",
				},
				multi: true,
			},

			// ---- Track Composite ----

			{
				name:        "TrackComposite",
				requestType: types.RequestTypeTrackComposite, publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
				},
				streamOptions: &streamOptions{
					outputType: types.OutputTypeRTMP,
				},
				segmentOptions: &segmentOptions{
					prefix:   "tc_multiple_{time}",
					playlist: "tc_multiple_{time}.m3u8",
				},
				multi: true,
			},
		} {
			if !r.run(t, test, r.runMultiTest) {
				return
			}
		}
	})
}

func (r *Runner) runMultiTest(t *testing.T, test *testCase) {
	req := r.build(test)

	egressID := r.startEgress(t, req)
	time.Sleep(time.Second * 10)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	if test.streamOptions != nil {
		_, err = r.client.UpdateStream(context.Background(), egressID, &livekit.UpdateStreamRequest{
			EgressId:      egressID,
			AddOutputUrls: []string{rtmpUrl3},
		})
		require.NoError(t, err)

		time.Sleep(time.Second * 10)
		r.verifyStreams(t, nil, p, rtmpUrl3)
		r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
			rtmpUrl3Redacted: livekit.StreamInfo_ACTIVE,
		})
		time.Sleep(time.Second * 10)
	} else {
		time.Sleep(time.Second * 20)
	}

	res := r.stopEgress(t, egressID)
	if test.fileOptions != nil {
		r.verifyFile(t, test, p, res)
	}
	if test.segmentOptions != nil {
		require.Len(t, res.GetSegmentResults(), 1)
		segments := res.GetSegmentResults()[0]
		require.Greater(t, segments.Size, int64(0))
		require.NotContains(t, segments.PlaylistName, "{")
		require.NotContains(t, segments.PlaylistLocation, "{")
		if segments.LivePlaylistName != "" {
			require.NotContains(t, segments.LivePlaylistName, "{")
		}
		if segments.LivePlaylistLocation != "" {
			require.NotContains(t, segments.LivePlaylistLocation, "{")
		}
		r.verifySegments(t, test, p, test.segmentOptions.suffix, res, false)
	}
	if test.imageOptions != nil {
		r.verifyImages(t, p, res)
	}
}

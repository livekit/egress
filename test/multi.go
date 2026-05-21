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
	"testing"
	"time"

	"github.com/livekit/egress/pkg/types"
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
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
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
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
				segmentOptions: &segmentOptions{
					prefix:   "tc_multiple_{time}",
					playlist: "tc_multiple_{time}.m3u8",
				},
				multi: true,
			},
		} {
			if !r.run(t, test) {
				return
			}
		}
	})
}

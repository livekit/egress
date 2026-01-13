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

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

var (
	samples = map[types.MimeType]string{
		types.MimeTypeOpus: "/media-samples/avsync_minmotion_livekit_audio_48k_120s.ogg",
		types.MimeTypeH264: "/media-samples/avsync_minmotion_livekit_video_1080p25_120s.h264",
		types.MimeTypeVP8:  "/media-samples/avsync_minmotion_livekit_1080p24_vp8.ivf",
		types.MimeTypeVP9:  "/media-samples/avsync_minmotion_livekit_1080p24_vp9.ivf",
		types.MimeTypePCMU: "/media-samples/avsync_minmotion_livekit_audio_8k_120s_pcmu.wav",
		types.MimeTypePCMA: "/media-samples/avsync_minmotion_livekit_audio_8k_120s_pcma.wav",
	}

	frameDurations = map[types.MimeType]time.Duration{
		types.MimeTypeH264: time.Microsecond * 41667,
		types.MimeTypeVP8:  time.Microsecond * 41667,
		types.MimeTypeVP9:  time.Microsecond * 41667,
		types.MimeTypePCMU: time.Millisecond * 20,
		types.MimeTypePCMA: time.Millisecond * 20,
	}
)

func (r *Runner) publishSample(t *testing.T, codec types.MimeType, publishAfter, unpublishAfter time.Duration, withMuting bool) string {
	if codec == "" {
		return ""
	}

	trackID := make(chan string, 1)
	time.AfterFunc(publishAfter, func() {
		done := make(chan struct{})
		unpublished := make(chan struct{})

		pub := r.publish(t, r.room.LocalParticipant, codec, done)
		trackID <- pub.SID()

		if withMuting {
			go func() {
				muted := false
				time.Sleep(time.Second * 15)
				for {
					select {
					case <-unpublished:
						return
					case <-done:
						return
					default:
						pub.SetMuted(!muted)
						muted = !muted
						time.Sleep(time.Second * 10)
					}
				}
			}()
		}

		if unpublishAfter != 0 {
			time.AfterFunc(unpublishAfter-publishAfter, func() {
				select {
				case <-done:
					return
				default:
					close(unpublished)
					_ = r.room.LocalParticipant.UnpublishTrack(pub.SID())
				}
			})
		}
	})

	if publishAfter == 0 {
		return <-trackID
	}
	return "TBD"
}

func (r *Runner) publishSampleWithDisconnection(t *testing.T, codec types.MimeType) string {
	pub := r.publish(t, r.room.LocalParticipant, codec, make(chan struct{}))
	trackID := pub.SID()

	time.AfterFunc(time.Second*10, func() {
		pub.SimulateDisconnection(time.Second * 10)
	})

	return trackID
}

func (r *Runner) publish(t *testing.T, p *lksdk.LocalParticipant, codec types.MimeType, done chan struct{}) *lksdk.LocalTrackPublication {
	filename := samples[codec]
	frameDuration := frameDurations[codec]

	var pub *lksdk.LocalTrackPublication
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			close(done)
			if pub != nil {
				_ = p.UnpublishTrack(pub.SID())
			}
		}),
	}

	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
	}

	track, err := lksdk.NewLocalFileTrack(filename, opts...)
	require.NoError(t, err)

	pub, err = p.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
	require.NoError(t, err)

	trackID := pub.SID()
	t.Cleanup(func() {
		_ = p.UnpublishTrack(trackID)
	})

	return pub
}

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
		types.MimeTypeOpus: "/media-samples/SolLevante.ogg",
		types.MimeTypeH264: "/media-samples/SolLevante.h264",
		types.MimeTypeVP8:  "/media-samples/SolLevante-vp8.ivf",
		types.MimeTypeVP9:  "/media-samples/SolLevante-vp9.ivf",
	}

	frameDurations = map[types.MimeType]time.Duration{
		types.MimeTypeH264: time.Microsecond * 41667,
		types.MimeTypeVP8:  time.Microsecond * 41667,
		types.MimeTypeVP9:  time.Microsecond * 41667,
	}
)

func (r *Runner) publishSamples(t *testing.T, audioCodec, videoCodec types.MimeType) (audioTrackID, videoTrackID string) {
	withAudioMuting := false
	if videoCodec != "" {
		videoTrackID = r.publishSample(t, videoCodec, r.Muting)
	} else {
		withAudioMuting = r.Muting
	}
	if audioCodec != "" {
		audioTrackID = r.publishSample(t, audioCodec, withAudioMuting)
	}

	time.Sleep(time.Second)
	return
}

func (r *Runner) publishSample(t *testing.T, codec types.MimeType, withMuting bool) string {
	done := make(chan struct{})
	pub := r.publish(t, codec, done)
	trackID := pub.SID()

	t.Cleanup(func() {
		_ = r.room.LocalParticipant.UnpublishTrack(trackID)
	})

	if withMuting {
		go func() {
			muted := false
			time.Sleep(time.Second * 15)
			for {
				select {
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

	return trackID
}

func (r *Runner) publishSampleOffset(t *testing.T, codec types.MimeType, publishAfter, unpublishAfter time.Duration) {
	if codec == "" {
		return
	}

	time.AfterFunc(publishAfter, func() {
		done := make(chan struct{})
		pub := r.publish(t, codec, done)
		if unpublishAfter != 0 {
			time.AfterFunc(unpublishAfter-publishAfter, func() {
				select {
				case <-done:
					return
				default:
					_ = r.room.LocalParticipant.UnpublishTrack(pub.SID())
				}
			})
		} else {
			t.Cleanup(func() {
				_ = r.room.LocalParticipant.UnpublishTrack(pub.SID())
			})
		}
	})
}

func (r *Runner) publishSampleWithDisconnection(t *testing.T, codec types.MimeType) string {
	done := make(chan struct{})
	pub := r.publish(t, codec, done)
	trackID := pub.SID()

	time.AfterFunc(time.Second*10, func() {
		pub.SimulateDisconnection(time.Second * 10)
	})

	return trackID
}

func (r *Runner) publish(t *testing.T, codec types.MimeType, done chan struct{}) *lksdk.LocalTrackPublication {
	filename := samples[codec]
	frameDuration := frameDurations[codec]

	var pub *lksdk.LocalTrackPublication
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			close(done)
			if pub != nil {
				_ = r.room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}),
	}

	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
	}

	track, err := lksdk.NewLocalFileTrack(filename, opts...)
	require.NoError(t, err)

	pub, err = r.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
	require.NoError(t, err)

	return pub
}

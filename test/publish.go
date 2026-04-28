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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

var participantSamples = map[string]map[types.MimeType]string{
	"p0": {
		types.MimeTypeOpus: "/media-samples/livekit_avsync_p0_audio_523hz_48k.ogg",
		types.MimeTypeH264: "/media-samples/livekit_avsync_p0_video_white_1080p25.h264",
		types.MimeTypeVP8:  "/media-samples/livekit_avsync_p0_video_white_1080p24.vp8.ivf",
		types.MimeTypeVP9:  "/media-samples/livekit_avsync_p0_video_white_1080p24.vp9.ivf",
		types.MimeTypePCMU: "/media-samples/livekit_avsync_p0_audio_523hz_8k.pcmu.wav",
		types.MimeTypePCMA: "/media-samples/livekit_avsync_p0_audio_523hz_8k.pcma.wav",
	},
	"p1": {
		types.MimeTypeOpus: "/media-samples/livekit_avsync_p1_audio_659hz_48k.ogg",
		types.MimeTypeH264: "/media-samples/livekit_avsync_p1_video_cyan_1080p25.h264",
	},
	"p2": {
		types.MimeTypeOpus: "/media-samples/livekit_avsync_p2_audio_784hz_48k.ogg",
		types.MimeTypeH264: "/media-samples/livekit_avsync_p2_video_yellow_1080p25.h264",
	},
}

// samples is the default (p0) sample set — used by existing single-participant publishing.
var samples = participantSamples["p0"]

var (
	frameDurations = map[types.MimeType]time.Duration{
		types.MimeTypeH264: time.Millisecond * 40,
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

func (r *Runner) publishForParticipant(t *testing.T, p *lksdk.LocalParticipant, participantName string, codec types.MimeType) *lksdk.LocalTrackPublication {
	sampleMap, ok := participantSamples[participantName]
	require.True(t, ok, "no samples for participant %s", participantName)

	filename, ok := sampleMap[codec]
	require.True(t, ok, "no %s sample for participant %s", codec, participantName)

	frameDuration := frameDurations[codec]

	done := make(chan struct{})
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

// pendingTrack holds a prepared track ready to be published.
type pendingTrack struct {
	track *lksdk.LocalTrack
	opts  lksdk.TrackPublicationOptions
}

// prepareTrack creates a local file track without publishing it.
func (r *Runner) prepareTrack(t *testing.T, participantName string, codec types.MimeType) *pendingTrack {
	sampleMap, ok := participantSamples[participantName]
	require.True(t, ok, "no samples for participant %s", participantName)

	filename, ok := sampleMap[codec]
	require.True(t, ok, "no %s sample for participant %s", codec, participantName)

	frameDuration := frameDurations[codec]

	var opts []lksdk.ReaderSampleProviderOption
	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
	}

	track, err := lksdk.NewLocalFileTrack(filename, opts...)
	require.NoError(t, err)

	return &pendingTrack{
		track: track,
		opts:  lksdk.TrackPublicationOptions{Name: filename},
	}
}

// multiPubResult holds published track info for multi-participant tests.
type multiPubResult struct {
	audioPubs    [3]*lksdk.LocalTrackPublication // for mute rotation
	audioTrackID string                          // p0 audio track ID
	videoTrackID string                          // p0 video track ID
}

// publishAllParticipants prepares all 6 tracks (h264 + opus per participant),
// then publishes them simultaneously so all participants start at the same time.
func (r *Runner) publishAllParticipants(t *testing.T) multiPubResult {
	r.connectMultiParticipants(t)

	participants := [3]struct {
		name string
		lp   *lksdk.LocalParticipant
	}{
		{"p0", r.room.LocalParticipant},
		{"p1", r.p1Room.LocalParticipant},
		{"p2", r.p2Room.LocalParticipant},
	}

	// Prepare all 6 tracks (no media flowing yet)
	type prepared struct {
		lp    *lksdk.LocalParticipant
		video *pendingTrack
		audio *pendingTrack
	}
	var tracks [3]prepared
	for i, p := range participants {
		tracks[i] = prepared{
			lp:    p.lp,
			video: r.prepareTrack(t, p.name, types.MimeTypeH264),
			audio: r.prepareTrack(t, p.name, types.MimeTypeOpus),
		}
	}

	// Publish all 6 tracks in parallel
	type pubResult struct {
		index int
		pub   *lksdk.LocalTrackPublication
		err   error
	}
	audioCh := make(chan pubResult, 3)
	videoCh := make(chan pubResult, 3)

	for i := range tracks {
		i := i

		go func() {
			pub, err := tracks[i].lp.PublishTrack(tracks[i].video.track, &tracks[i].video.opts)
			if err != nil {
				videoCh <- pubResult{index: i, err: fmt.Errorf("publish video for p%d: %w", i, err)}
				return
			}
			trackID := pub.SID()
			t.Cleanup(func() { _ = tracks[i].lp.UnpublishTrack(trackID) })
			videoCh <- pubResult{index: i, pub: pub}
		}()

		go func() {
			pub, err := tracks[i].lp.PublishTrack(tracks[i].audio.track, &tracks[i].audio.opts)
			if err != nil {
				audioCh <- pubResult{index: i, err: fmt.Errorf("publish audio for p%d: %w", i, err)}
				return
			}
			trackID := pub.SID()
			t.Cleanup(func() { _ = tracks[i].lp.UnpublishTrack(trackID) })
			audioCh <- pubResult{index: i, pub: pub}
		}()
	}

	var audioPubs [3]*lksdk.LocalTrackPublication
	var videoPubs [3]*lksdk.LocalTrackPublication
	var errs []error
	for range 6 {
		select {
		case r := <-videoCh:
			if r.err != nil {
				errs = append(errs, r.err)
			} else {
				videoPubs[r.index] = r.pub
			}
		case r := <-audioCh:
			if r.err != nil {
				errs = append(errs, r.err)
			} else {
				audioPubs[r.index] = r.pub
			}
		}
	}
	require.Empty(t, errs, "failed to publish tracks: %v", errs)

	t.Cleanup(r.disconnectMultiParticipants)

	var p0Audio, p0Video string
	if audioPubs[0] != nil {
		p0Audio = audioPubs[0].SID()
	}
	if videoPubs[0] != nil {
		p0Video = videoPubs[0].SID()
	}

	return multiPubResult{
		audioPubs:    audioPubs,
		audioTrackID: p0Audio,
		videoTrackID: p0Video,
	}
}

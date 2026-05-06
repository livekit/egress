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
	"sync"
	"testing"
	"time"

	"github.com/linkdata/deadlock"
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

// publishAllParticipants prepares all 6 tracks (h264 + opus per participant),
// then publishes them simultaneously so all participants start at the same time.
// Returns the 3 audio publications (for mute rotation).
func (r *Runner) publishAllParticipants(t *testing.T) [3]*lksdk.LocalTrackPublication {
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
	var (
		wg     sync.WaitGroup
		pubs   [3]*lksdk.LocalTrackPublication // audio pubs for mute rotation
		mu     deadlock.Mutex
		errors []error
	)

	for i := range tracks {
		i := i
		wg.Add(2)

		go func() {
			defer wg.Done()
			pub, err := tracks[i].lp.PublishTrack(tracks[i].video.track, &tracks[i].video.opts)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Errorf("publish video for p%d: %w", i, err))
				mu.Unlock()
				return
			}
			trackID := pub.SID()
			t.Cleanup(func() { _ = tracks[i].lp.UnpublishTrack(trackID) })
		}()

		go func() {
			defer wg.Done()
			pub, err := tracks[i].lp.PublishTrack(tracks[i].audio.track, &tracks[i].audio.opts)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Errorf("publish audio for p%d: %w", i, err))
				mu.Unlock()
				return
			}
			mu.Lock()
			pubs[i] = pub
			mu.Unlock()
			trackID := pub.SID()
			t.Cleanup(func() { _ = tracks[i].lp.UnpublishTrack(trackID) })
		}()
	}

	wg.Wait()
	require.Empty(t, errors, "failed to publish tracks: %v", errors)

	t.Cleanup(r.disconnectMultiParticipants)

	return pubs
}

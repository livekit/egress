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
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// participantMedia maps participant index to audio/video file paths and frequency.
type participantMedia struct {
	AudioFile string
	VideoFile string
	Frequency float64 // Hz, for sync analysis
}

var participantMediaDefs = []participantMedia{
	{"/media-samples/participant_0_440hz.ogg", "/media-samples/participant_0_red.h264", 440},
	{"/media-samples/participant_1_880hz.ogg", "/media-samples/participant_1_green.h264", 880},
	{"/media-samples/participant_2_1320hz.ogg", "/media-samples/participant_2_blue.h264", 1320},
	{"/media-samples/participant_3_1760hz.ogg", "/media-samples/participant_3_yellow.h264", 1760},
	{"/media-samples/participant_4_2200hz.ogg", "/media-samples/participant_4_cyan.h264", 2200},
	{"/media-samples/participant_5_2640hz.ogg", "/media-samples/participant_5_magenta.h264", 2640},
}

// MultiPublisher manages N participants in a room, each publishing
// pre-generated media with a unique audio frequency and video color.
type MultiPublisher struct {
	t     *testing.T
	rooms []*lksdk.Room

	audioPubs []*lksdk.LocalTrackPublication
	videoPubs []*lksdk.LocalTrackPublication
	media     []participantMedia

	// Track IDs set after publishing (for legacy compat with testCase.audioTrackID/videoTrackID)
	AudioTrackID string
	VideoTrackID string

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewMultiPublisher connects n participants to the runner's room and publishes
// audio and video tracks for each using per-participant test media (unique frequency/color).
// Use audioCodec="" or videoCodec="" to skip that media type. Maximum 6 participants.
func NewMultiPublisher(t *testing.T, r *Runner, n int, audioCodec, videoCodec types.MimeType) *MultiPublisher {
	t.Helper()
	require.LessOrEqual(t, n, len(participantMediaDefs), "max %d participants supported", len(participantMediaDefs))

	mp := &MultiPublisher{
		t:      t,
		rooms:  make([]*lksdk.Room, n),
		media:  participantMediaDefs[:n],
		stopCh: make(chan struct{}),
	}

	for i := 0; i < n; i++ {
		room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
			APIKey:              r.ApiKey,
			APISecret:           r.ApiSecret,
			RoomName:            r.RoomName,
			ParticipantName:     fmt.Sprintf("mp-participant-%d", i),
			ParticipantIdentity: fmt.Sprintf("mp-participant-%d-%d", i, rand.Intn(1000)),
		}, lksdk.NewRoomCallback())
		require.NoError(t, err)
		mp.rooms[i] = room
	}

	// Publish tracks
	for i := 0; i < n; i++ {
		lp := mp.rooms[i].LocalParticipant
		done := make(chan struct{})

		if audioCodec != "" {
			audioFile := mp.media[i].AudioFile
			audioTrack, err := lksdk.NewLocalFileTrack(audioFile,
				lksdk.ReaderTrackWithOnWriteComplete(func() {}),
			)
			require.NoError(t, err)
			pub, err := lp.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{Name: audioFile})
			require.NoError(t, err)
			mp.audioPubs = append(mp.audioPubs, pub)
			if i == 0 {
				mp.AudioTrackID = pub.SID()
			}
		}

		if videoCodec != "" {
			videoFile := mp.media[i].VideoFile
			videoTrack, err := lksdk.NewLocalFileTrack(videoFile,
				lksdk.ReaderTrackWithOnWriteComplete(func() { close(done) }),
				lksdk.ReaderTrackWithFrameDuration(time.Microsecond*41667), // ~24fps
			)
			require.NoError(t, err)
			pub, err := lp.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{Name: videoFile})
			require.NoError(t, err)
			mp.videoPubs = append(mp.videoPubs, pub)
			if i == 0 {
				mp.VideoTrackID = pub.SID()
			}
		}
	}

	t.Cleanup(func() {
		mp.StopRotation()
		for _, room := range mp.rooms {
			room.Disconnect()
		}
	})

	return mp
}

// NewLegacyPublisher creates a single-participant MultiPublisher using the existing
// test media files (avsync_minmotion_livekit*) and the runner's primary room. This
// provides backward compatibility for tests that use publishOptions with timing features.
// The participant is the runner's own room local participant — no new room is connected.
func NewLegacyPublisher(t *testing.T, r *Runner, opts publishOptions) *MultiPublisher {
	t.Helper()

	mp := &MultiPublisher{
		t:      t,
		rooms:  []*lksdk.Room{r.room},
		stopCh: make(chan struct{}),
	}

	lp := r.room.LocalParticipant
	audioMuting := r.Muting
	videoMuting := r.Muting && opts.audioCodec == ""

	mp.AudioTrackID = mp.publishWithTiming(t, lp, opts.audioCodec, opts.audioDelay, opts.audioUnpublish, audioMuting)
	if opts.audioRepublish != 0 {
		mp.publishWithTiming(t, lp, opts.audioCodec, opts.audioRepublish, 0, audioMuting)
	}
	mp.VideoTrackID = mp.publishWithTiming(t, lp, opts.videoCodec, opts.videoDelay, opts.videoUnpublish, videoMuting)
	if opts.videoRepublish != 0 {
		mp.publishWithTiming(t, lp, opts.videoCodec, opts.videoRepublish, 0, videoMuting)
	}

	// Don't disconnect r.room on cleanup — it's owned by the Runner
	return mp
}

// publishWithTiming publishes a track from the old test media with delay/unpublish/muting support.
// This is the MultiPublisher equivalent of the old Runner.publishSample.
func (mp *MultiPublisher) publishWithTiming(t *testing.T, lp *lksdk.LocalParticipant, codec types.MimeType, publishAfter, unpublishAfter time.Duration, withMuting bool) string {
	if codec == "" {
		return ""
	}

	trackID := make(chan string, 1)
	time.AfterFunc(publishAfter, func() {
		done := make(chan struct{})
		unpublished := make(chan struct{})

		pub := publishLegacyTrack(t, lp, codec, done)
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
					_ = lp.UnpublishTrack(pub.SID())
				}
			})
		}
	})

	if publishAfter == 0 {
		return <-trackID
	}
	return "TBD"
}

// publishLegacyTrack publishes a track using the old test media files (samples map).
func publishLegacyTrack(t *testing.T, p *lksdk.LocalParticipant, codec types.MimeType, done chan struct{}) *lksdk.LocalTrackPublication {
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

// Participants returns the connected rooms.
func (mp *MultiPublisher) Participants() []*lksdk.Room {
	return mp.rooms
}

// ParticipantMedia returns the media definitions (frequencies, file paths) for each participant.
func (mp *MultiPublisher) ParticipantMedia() []participantMedia {
	return mp.media
}

// N returns the number of participants.
func (mp *MultiPublisher) N() int {
	return len(mp.rooms)
}

// StartRotation begins round-robin audio muting. Participant 0 starts unmuted,
// all others start muted. Every turnDuration, the active participant is muted
// and the next one is unmuted. Only audio is affected; video stays unmuted.
func (mp *MultiPublisher) StartRotation(turnDuration time.Duration) {
	if len(mp.audioPubs) == 0 {
		return
	}

	// Start with all muted except participant 0
	for i, pub := range mp.audioPubs {
		pub.SetMuted(i != 0)
	}

	go func() {
		active := 0
		ticker := time.NewTicker(turnDuration)
		defer ticker.Stop()

		for {
			select {
			case <-mp.stopCh:
				return
			case <-ticker.C:
				mp.audioPubs[active].SetMuted(true)
				active = (active + 1) % len(mp.audioPubs)
				mp.audioPubs[active].SetMuted(false)
			}
		}
	}()
}

// StopRotation stops the mute rotation goroutine.
func (mp *MultiPublisher) StopRotation() {
	mp.stopOnce.Do(func() {
		close(mp.stopCh)
	})
}

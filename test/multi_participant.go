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

// participantMedia defines a participant's unique audio frequency and video color.
type participantMedia struct {
	Frequency float64 // Hz, for sync analysis
	Color     string  // color name, for video file selection
}

// Per-participant definitions: unique frequency + color for sync analysis.
var participantMediaDefs = []participantMedia{
	{Frequency: 440, Color: "red"},
	{Frequency: 880, Color: "green"},
	{Frequency: 1320, Color: "blue"},
	{Frequency: 1760, Color: "yellow"},
	{Frequency: 2200, Color: "cyan"},
	{Frequency: 2640, Color: "magenta"},
}

// Codec rotation: participant i uses codec at index i%3.
// Audio: 0,3=Opus  1,4=PCMU  2,5=PCMA
// Video: 0,3=H264  1,4=VP8   2,5=VP9
var participantAudioCodecs = []types.MimeType{types.MimeTypeOpus, types.MimeTypePCMU, types.MimeTypePCMA}
var participantVideoCodecs = []types.MimeType{types.MimeTypeH264, types.MimeTypeVP8, types.MimeTypeVP9}

// audioFile returns the audio file and frame duration for participant i.
func audioFile(i int) (string, time.Duration) {
	freq := participantMediaDefs[i].Frequency
	switch participantAudioCodecs[i%3] {
	case types.MimeTypeOpus:
		return fmt.Sprintf("/media-samples/participant_%d_%.0fhz.ogg", i, freq), 0
	case types.MimeTypePCMU:
		return fmt.Sprintf("/media-samples/participant_%d_%.0fhz_pcmu.wav", i, freq), time.Millisecond * 20
	case types.MimeTypePCMA:
		return fmt.Sprintf("/media-samples/participant_%d_%.0fhz_pcma.wav", i, freq), time.Millisecond * 20
	}
	return "", 0
}

// audioCodecFor returns the audio codec assigned to participant i.
func audioCodecFor(i int) types.MimeType {
	return participantAudioCodecs[i%3]
}

// videoFile returns the video file and frame duration for participant i.
func videoFile(i int) (string, time.Duration) {
	color := participantMediaDefs[i].Color
	switch participantVideoCodecs[i%3] {
	case types.MimeTypeH264:
		return fmt.Sprintf("/media-samples/participant_%d_%s.h264", i, color), time.Microsecond * 41667
	case types.MimeTypeVP8:
		return fmt.Sprintf("/media-samples/participant_%d_%s_vp8.ivf", i, color), time.Microsecond * 41667
	case types.MimeTypeVP9:
		return fmt.Sprintf("/media-samples/participant_%d_%s_vp9.ivf", i, color), time.Microsecond * 41667
	}
	return "", 0
}

// videoCodecFor returns the video codec assigned to participant i.
func videoCodecFor(i int) types.MimeType {
	return participantVideoCodecs[i%3]
}

// MultiPublisher manages N participants in a room, each publishing
// pre-generated media with a unique audio frequency and video color.
type MultiPublisher struct {
	t     *testing.T
	rooms []*lksdk.Room

	audioPubs []*lksdk.LocalTrackPublication
	videoPubs []*lksdk.LocalTrackPublication
	media     []participantMedia

	// Track IDs for the first participant (backward compat with testCase fields).
	AudioTrackID string
	VideoTrackID string

	ownsRooms bool // false when using runner's room
	stopOnce  sync.Once
	stopCh    chan struct{}
}

// NewMultiPublisher connects n new participants to the runner's room and publishes
// audio and video tracks for each. Each participant uses its assigned codec from the
// rotation (audio: Opus/PCMU/PCMA, video: H264/VP8/VP9). Set publishAudio/publishVideo
// to false to skip that media type. Maximum 6 participants.
func NewMultiPublisher(t *testing.T, r *Runner, n int, publishAudio, publishVideo bool) *MultiPublisher {
	t.Helper()
	require.LessOrEqual(t, n, len(participantMediaDefs), "max %d participants supported", len(participantMediaDefs))

	mp := &MultiPublisher{
		t:         t,
		rooms:     make([]*lksdk.Room, n),
		media:     participantMediaDefs[:n],
		ownsRooms: true,
		stopCh:    make(chan struct{}),
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

	mp.publishAll(t, n, publishAudio, publishVideo)

	t.Cleanup(func() {
		mp.StopRotation()
		if mp.ownsRooms {
			for _, room := range mp.rooms {
				room.Disconnect()
			}
		}
	})

	return mp
}

// NewRunnerPublisher creates a single-participant MultiPublisher on the runner's
// existing room. Used by r.run() so the runner's primary participant publishes.
func NewRunnerPublisher(t *testing.T, r *Runner, publishAudio, publishVideo bool) *MultiPublisher {
	t.Helper()

	mp := &MultiPublisher{
		t:         t,
		rooms:     []*lksdk.Room{r.room},
		media:     participantMediaDefs[:1],
		ownsRooms: false,
		stopCh:    make(chan struct{}),
	}

	mp.publishAll(t, 1, publishAudio, publishVideo)
	return mp
}

func (mp *MultiPublisher) publishAll(t *testing.T, n int, publishAudio, publishVideo bool) {
	for i := 0; i < n; i++ {
		lp := mp.rooms[i].LocalParticipant

		if publishAudio {
			f, fd := audioFile(i)
			pub := publishFileTrack(t, lp, f, fd)
			mp.audioPubs = append(mp.audioPubs, pub)
			if i == 0 {
				mp.AudioTrackID = pub.SID()
			}
		}

		if publishVideo {
			f, fd := videoFile(i)
			pub := publishFileTrack(t, lp, f, fd)
			mp.videoPubs = append(mp.videoPubs, pub)
			if i == 0 {
				mp.VideoTrackID = pub.SID()
			}
		}
	}
}

// publishFileTrack publishes a single track from a file with an optional frame duration.
func publishFileTrack(t *testing.T, lp *lksdk.LocalParticipant, file string, frameDuration time.Duration) *lksdk.LocalTrackPublication {
	require.NotEmpty(t, file)

	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {}),
	}
	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
	}

	track, err := lksdk.NewLocalFileTrack(file, opts...)
	require.NoError(t, err)

	pub, err := lp.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: file})
	require.NoError(t, err)

	trackID := pub.SID()
	t.Cleanup(func() {
		_ = lp.UnpublishTrack(trackID)
	})

	return pub
}

// PublishOnRoom publishes audio and/or video on an externally-connected participant.
// Uses the participant index to select the right files from the rotation.
func PublishOnRoom(t *testing.T, room *lksdk.Room, participantIdx int, publishAudio, publishVideo bool) (audioPub, videoPub *lksdk.LocalTrackPublication) {
	t.Helper()
	lp := room.LocalParticipant

	if publishAudio {
		f, fd := audioFile(participantIdx)
		audioPub = publishFileTrack(t, lp, f, fd)
	}
	if publishVideo {
		f, fd := videoFile(participantIdx)
		videoPub = publishFileTrack(t, lp, f, fd)
	}
	return
}

// ScheduleUnpublish unpublishes a track after a delay.
func ScheduleUnpublish(lp *lksdk.LocalParticipant, trackID string, after time.Duration) {
	time.AfterFunc(after, func() {
		_ = lp.UnpublishTrack(trackID)
	})
}

// ScheduleMuting starts periodic muting on a publication (15s delay, then toggle every 10s).
func ScheduleMuting(pub *lksdk.LocalTrackPublication, stop <-chan struct{}) {
	go func() {
		muted := false
		time.Sleep(time.Second * 15)
		for {
			select {
			case <-stop:
				return
			default:
				pub.SetMuted(!muted)
				muted = !muted
				time.Sleep(time.Second * 10)
			}
		}
	}()
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

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

// ParticipantMediaDef defines a participant's unique audio frequency and video color.
type ParticipantMediaDef struct {
	Frequency float64 // Hz, for sync analysis
	Color     string  // color name, for video file selection
}

// ParticipantMediaDefs defines each participant's unique frequency and color for sync analysis.
var ParticipantMediaDefs = []ParticipantMediaDef{
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
	freq := ParticipantMediaDefs[i].Frequency
	switch participantAudioCodecs[i%3] {
	case types.MimeTypeOpus:
		return fmt.Sprintf("/workspace/test/samples/participant_%d_%.0fhz.ogg", i, freq), 0
	case types.MimeTypePCMU:
		return fmt.Sprintf("/workspace/test/samples/participant_%d_%.0fhz_pcmu.wav", i, freq), time.Millisecond * 20
	case types.MimeTypePCMA:
		return fmt.Sprintf("/workspace/test/samples/participant_%d_%.0fhz_pcma.wav", i, freq), time.Millisecond * 20
	}
	return "", 0
}

// audioCodecFor returns the audio codec assigned to participant i.
func audioCodecFor(i int) types.MimeType {
	return participantAudioCodecs[i%3]
}

// videoFile returns the video file and frame duration for participant i.
func videoFile(i int) (string, time.Duration) {
	color := ParticipantMediaDefs[i].Color
	fd := time.Microsecond * 40000 // 25fps matching generated media
	switch participantVideoCodecs[i%3] {
	case types.MimeTypeH264:
		return fmt.Sprintf("/workspace/test/samples/participant_%d_%s.h264", i, color), fd
	case types.MimeTypeVP8:
		return fmt.Sprintf("/workspace/test/samples/participant_%d_%s_vp8.ivf", i, color), fd
	case types.MimeTypeVP9:
		return fmt.Sprintf("/workspace/test/samples/participant_%d_%s_vp9.ivf", i, color), fd
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
	media     []ParticipantMediaDef

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
	require.LessOrEqual(t, n, len(ParticipantMediaDefs), "max %d participants supported", len(ParticipantMediaDefs))

	mp := &MultiPublisher{
		t:         t,
		rooms:     make([]*lksdk.Room, n),
		media:     ParticipantMediaDefs[:n],
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
		media:     ParticipantMediaDefs[:1],
		ownsRooms: false,
		stopCh:    make(chan struct{}),
	}

	mp.publishAll(t, 1, publishAudio, publishVideo)
	return mp
}

func (mp *MultiPublisher) publishAll(t *testing.T, n int, publishAudio, publishVideo bool) {
	// Create all tracks first, then publish them all as close together as possible.
	type trackPair struct {
		lp         *lksdk.LocalParticipant
		idx        int
		audioTrack *lksdk.LocalSampleTrack
		videoTrack *lksdk.LocalSampleTrack
		audioFile  string
		videoFile  string
	}

	pairs := make([]trackPair, n)
	for i := 0; i < n; i++ {
		pairs[i].lp = mp.rooms[i].LocalParticipant
		pairs[i].idx = i

		if publishAudio {
			f, fd := audioFile(i)
			pairs[i].audioFile = f
			opts := []lksdk.ReaderSampleProviderOption{
				lksdk.ReaderTrackWithOnWriteComplete(func() {}),
			}
			if fd != 0 {
				opts = append(opts, lksdk.ReaderTrackWithFrameDuration(fd))
			}
			track, err := lksdk.NewLocalFileTrack(f, opts...)
			require.NoError(t, err)
			pairs[i].audioTrack = track
		}

		if publishVideo {
			f, fd := videoFile(i)
			pairs[i].videoFile = f
			opts := []lksdk.ReaderSampleProviderOption{
				lksdk.ReaderTrackWithOnWriteComplete(func() {}),
			}
			if fd != 0 {
				opts = append(opts, lksdk.ReaderTrackWithFrameDuration(fd))
			}
			track, err := lksdk.NewLocalFileTrack(f, opts...)
			require.NoError(t, err)
			pairs[i].videoTrack = track
		}
	}

	// Publish every track in its own goroutine — all audio and video
	// tracks across all participants fire simultaneously.
	type pubResult struct {
		idx     int
		isAudio bool
		pub     *lksdk.LocalTrackPublication
	}

	total := 0
	results := make(chan pubResult, n*2)

	for _, p := range pairs {
		if p.audioTrack != nil {
			total++
			go func(p trackPair) {
				pub, err := p.lp.PublishTrack(p.audioTrack, &lksdk.TrackPublicationOptions{Name: p.audioFile})
				require.NoError(t, err)
				sid := pub.SID()
				t.Cleanup(func() { _ = p.lp.UnpublishTrack(sid) })
				results <- pubResult{idx: p.idx, isAudio: true, pub: pub}
			}(p)
		}
		if p.videoTrack != nil {
			total++
			go func(p trackPair) {
				pub, err := p.lp.PublishTrack(p.videoTrack, &lksdk.TrackPublicationOptions{Name: p.videoFile})
				require.NoError(t, err)
				sid := pub.SID()
				t.Cleanup(func() { _ = p.lp.UnpublishTrack(sid) })
				results <- pubResult{idx: p.idx, isAudio: false, pub: pub}
			}(p)
		}
	}

	for range total {
		r := <-results
		if r.isAudio {
			mp.audioPubs = append(mp.audioPubs, r.pub)
			if r.idx == 0 {
				mp.AudioTrackID = r.pub.SID()
			}
		} else {
			mp.videoPubs = append(mp.videoPubs, r.pub)
			if r.idx == 0 {
				mp.VideoTrackID = r.pub.SID()
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
func (mp *MultiPublisher) ParticipantMedia() []ParticipantMediaDef {
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

	// Mute all participants first, then unmute participant 0.
	// This ensures participant 0 goes through the same mute→unmute
	// propagation path as all other participants, giving consistent timing.
	for _, pub := range mp.audioPubs {
		pub.SetMuted(true)
	}
	mp.audioPubs[0].SetMuted(false)

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

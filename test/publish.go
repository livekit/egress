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
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

var (
	participantSamples = map[string]map[types.MimeType]string{
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

	frameDurations = map[types.MimeType]time.Duration{
		types.MimeTypeH264: time.Millisecond * 40,
		types.MimeTypeVP8:  time.Microsecond * 41667,
		types.MimeTypeVP9:  time.Microsecond * 41667,
		types.MimeTypePCMU: time.Millisecond * 20,
		types.MimeTypePCMA: time.Millisecond * 20,
	}
)

type trackKind int

const (
	trackAudio trackKind = iota
	trackVideo
)

type publisherErr struct {
	participant string
	err         error
}

type publisherState struct {
	name      string
	identity  string
	room      *lksdk.Room
	lp        *lksdk.LocalParticipant
	audioPub  atomic.Pointer[lksdk.LocalTrackPublication]
	videoPub  atomic.Pointer[lksdk.LocalTrackPublication]
	connected chan struct{}
}

func (r *Runner) executePlan(t *testing.T, test *testCase) {
	plan := test.plan
	if plan == nil {
		return
	}

	states := make(map[string]*publisherState, len(plan.publishers))
	streamCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	errCh := make(chan publisherErr, errChCapacity(plan))

	// register cleanup
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		close(errCh)
		for e := range errCh {
			t.Errorf("publisher %s failed: %v", e.participant, e.err)
		}
		for _, s := range states {
			if s.room != nil {
				s.room.Disconnect()
			}
		}
	})

	// generate identities
	for _, pp := range plan.publishers {
		s := &publisherState{
			name:      pp.participant,
			identity:  fmt.Sprintf("%s-%d", pp.participant, rand.Intn(100)),
			connected: make(chan struct{}),
		}
		states[pp.participant] = s
	}
	test.publishers = states
	if s, ok := states["p0"]; ok {
		test.p0Identity = s.identity
	}

	// connect publishers
	for _, pp := range plan.publishers {
		s := states[pp.participant]
		if pp.delayConnection != 0 {
			continue
		}
		rm, err := r.connectAs(pp.participant, s.identity, connectCodecs(pp))
		require.NoError(t, err)
		s.room = rm
		s.lp = rm.LocalParticipant
		close(s.connected)
	}

	// start per-track goroutines
	start := time.Now()
	audioTrackIDp0 := make(chan string, 1)
	videoTrackIDp0 := make(chan string, 1)

	for _, pp := range plan.publishers {
		pp := pp
		s := states[pp.participant]

		if pp.delayConnection > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer close(s.connected)
				if !sleepUntilCtx(streamCtx, start, pp.delayConnection) {
					return
				}
				rm, err := r.connectAs(pp.participant, s.identity, connectCodecs(pp))
				if err != nil {
					errCh <- publisherErr{pp.participant, fmt.Errorf("connect: %w", err)}
					return
				}
				s.room = rm
				s.lp = rm.LocalParticipant
			}()
		}

		if hasDisconnects(pp.events) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if !waitConnected(streamCtx, s) {
					return
				}
				runParticipantTimeline(streamCtx, pp, start, s)
			}()
		}
		if len(pp.audioEvents) > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if !waitConnected(streamCtx, s) {
					return
				}
				if err := r.runTrackTimeline(streamCtx, pp, start, s, trackAudio, audioTrackIDp0); err != nil {
					errCh <- publisherErr{pp.participant, fmt.Errorf("audio: %w", err)}
				}
			}()
		}
		if len(pp.videoEvents) > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if !waitConnected(streamCtx, s) {
					return
				}
				if err := r.runTrackTimeline(streamCtx, pp, start, s, trackVideo, videoTrackIDp0); err != nil {
					errCh <- publisherErr{pp.participant, fmt.Errorf("video: %w", err)}
				}
			}()
		}
	}

	// Block until p0's immediate-publish track IDs are live
	needAudio, audioImmediate := needsP0TrackID(plan, trackAudio)
	needVideo, videoImmediate := needsP0TrackID(plan, trackVideo)
	for (needAudio && audioImmediate) || (needVideo && videoImmediate) {
		select {
		case id := <-audioTrackIDp0:
			test.audioTrackID = id
			needAudio = false
		case id := <-videoTrackIDp0:
			test.videoTrackID = id
			needVideo = false
		case e := <-errCh:
			require.NoError(t, e.err, "publisher %s failed", e.participant)
		}
	}
	if needAudio {
		test.audioTrackID = "TBD"
	}
	if needVideo {
		test.videoTrackID = "TBD"
	}
}

func (r *Runner) runTrackTimeline(ctx context.Context, pp *publisherPlan, start time.Time, s *publisherState, kind trackKind, idChan chan<- string) error {
	var events []event
	var pubAtom *atomic.Pointer[lksdk.LocalTrackPublication]
	switch kind {
	case trackAudio:
		events = pp.audioEvents
		pubAtom = &s.audioPub
	case trackVideo:
		events = pp.videoEvents
		pubAtom = &s.videoPub
	default:
		return fmt.Errorf("invalid track kind %v", kind)
	}

	defer func() {
		if pub := pubAtom.Load(); pub != nil {
			_ = s.lp.UnpublishTrack(pub.SID())
		}
	}()

	var nilPub *lksdk.LocalTrackPublication

	for _, e := range events {
		if !sleepUntilCtx(ctx, start, e.pts) {
			return nil
		}
		switch e.eventType {
		case eventTypePublish:
			pub, err := r.publishTrack(s.lp, pp.participant, e.codec)
			if err != nil {
				return fmt.Errorf("publish %s: %w", e.codec, err)
			}
			pubAtom.Store(pub)
			if pp.participant == "p0" {
				select {
				case idChan <- pub.SID():
				default:
				}
			}
		case eventTypeUnpublish:
			if pub := pubAtom.Load(); pub != nil {
				_ = s.lp.UnpublishTrack(pub.SID())
				pubAtom.Store(nilPub)
			}
		case eventTypeMute:
			if pub := pubAtom.Load(); pub != nil {
				pub.SetMuted(true)
			}
		case eventTypeUnmute:
			if pub := pubAtom.Load(); pub != nil {
				pub.SetMuted(false)
			}
		}
	}

	// Hold the track open until the test ends; defer above tears it down.
	<-ctx.Done()
	return nil
}

func runParticipantTimeline(ctx context.Context, pp *publisherPlan, start time.Time, s *publisherState) {
	for _, e := range pp.events {
		if !sleepUntilCtx(ctx, start, e.pts) {
			return
		}
		if e.eventType != eventTypeDisconnect {
			continue
		}
		if pub := s.audioPub.Load(); pub != nil {
			pub.SimulateDisconnection(e.duration)
		}
		if pub := s.videoPub.Load(); pub != nil {
			pub.SimulateDisconnection(e.duration)
		}
	}
}

// needsP0TrackID returns whether p0 has a publish event for this track,
// and whether it's immediate (pts == 0). Delayed publishes use "TBD"
// rather than blocking executePlan.
func needsP0TrackID(plan *publishPlan, track trackKind) (need, immediate bool) {
	for _, pp := range plan.publishers {
		if pp.participant != "p0" {
			continue
		}
		var events []event
		switch track {
		case trackAudio:
			events = pp.audioEvents
		case trackVideo:
			events = pp.videoEvents
		}
		for _, e := range events {
			if e.eventType == eventTypePublish {
				return true, e.pts == 0 && pp.delayConnection == 0
			}
		}
	}
	return false, false
}

// errChCapacity sizes errCh so no goroutine blocks on send. Only
// publishTrack and the delayed connector return errors.
func errChCapacity(plan *publishPlan) int {
	n := 0
	for _, pp := range plan.publishers {
		if pp.delayConnection > 0 {
			n++
		}
		for _, e := range pp.audioEvents {
			if e.eventType == eventTypePublish {
				n++
			}
		}
		for _, e := range pp.videoEvents {
			if e.eventType == eventTypePublish {
				n++
			}
		}
	}
	return n
}

// waitConnected blocks until the publisher's connect has completed (or
// failed). Returns false if ctx is canceled or connect failed.
func waitConnected(ctx context.Context, s *publisherState) bool {
	select {
	case <-s.connected:
	case <-ctx.Done():
		return false
	}
	return s.lp != nil
}

func hasDisconnects(events []event) bool {
	for _, e := range events {
		if e.eventType == eventTypeDisconnect {
			return true
		}
	}
	return false
}

// connectCodecs derives SDK codec preferences from a publisher's
// events. Only PCMU/PCMA need explicit preferences.
func connectCodecs(pp *publisherPlan) []livekit.Codec {
	for _, e := range pp.audioEvents {
		if e.eventType != eventTypePublish {
			continue
		}
		switch e.codec {
		case types.MimeTypePCMU:
			return []livekit.Codec{{Mime: string(types.MimeTypePCMU)}}
		case types.MimeTypePCMA:
			return []livekit.Codec{{Mime: string(types.MimeTypePCMA)}}
		}
		return nil
	}
	return nil
}

func (r *Runner) connectAs(name, identity string, codecs []livekit.Codec) (*lksdk.Room, error) {
	opts := []lksdk.ConnectOption{}
	if len(codecs) > 0 {
		opts = append(opts, lksdk.WithCodecs(codecs))
	}
	return lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     fmt.Sprintf("egress-%s", name),
		ParticipantIdentity: identity,
	}, lksdk.NewRoomCallback(), opts...)
}

// publishTrack publishes a sample on lp. Caller owns track lifetime.
func (r *Runner) publishTrack(lp *lksdk.LocalParticipant, participantName string, codec types.MimeType) (*lksdk.LocalTrackPublication, error) {
	sampleMap, ok := participantSamples[participantName]
	if !ok {
		return nil, fmt.Errorf("no samples for participant %s", participantName)
	}
	filename, ok := sampleMap[codec]
	if !ok {
		return nil, fmt.Errorf("no %s sample for participant %s", codec, participantName)
	}

	frameDuration := frameDurations[codec]

	done := make(chan struct{})
	var pub *lksdk.LocalTrackPublication
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			close(done)
			if pub != nil {
				_ = lp.UnpublishTrack(pub.SID())
			}
		}),
	}
	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
	}

	track, err := lksdk.NewLocalFileTrack(filename, opts...)
	if err != nil {
		return nil, err
	}

	pub, err = lp.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
	if err != nil {
		return nil, err
	}
	return pub, nil
}

// publish publishes a p0 sample for custom test functions that manage their own SDK connection (e.g. late-track tests).
func (r *Runner) publish(t *testing.T, lp *lksdk.LocalParticipant, codec types.MimeType) *lksdk.LocalTrackPublication {
	pub, err := r.publishTrack(lp, "p0", codec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lp.UnpublishTrack(pub.SID()) })
	return pub
}

// sleepUntilCtx sleeps until from+d. Returns false if ctx canceled first.
func sleepUntilCtx(ctx context.Context, from time.Time, d time.Duration) bool {
	delay := d - time.Since(from)
	if delay <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

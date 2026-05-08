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
	"sync"
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

var (
	frameDurations = map[types.MimeType]time.Duration{
		types.MimeTypeH264: time.Millisecond * 40,
		types.MimeTypeVP8:  time.Microsecond * 41667,
		types.MimeTypeVP9:  time.Microsecond * 41667,
		types.MimeTypePCMU: time.Millisecond * 20,
		types.MimeTypePCMA: time.Millisecond * 20,
	}
)

// streamErr carries goroutine failures back to the test goroutine.
// require.X / FailNow from non-test goroutines is unsafe.
type streamErr struct {
	stream *streamPlan
	err    error
}

func (r *Runner) executePlan(t *testing.T, test *testCase) {
	plan := test.plan
	if plan == nil {
		return
	}

	needsRemotes := false
	for _, s := range append(append([]*streamPlan{}, plan.audio...), plan.video...) {
		if s.participant != "p0" {
			needsRemotes = true
			break
		}
	}
	if needsRemotes {
		r.connectMultiParticipants(t)
		t.Cleanup(r.disconnectMultiParticipants)
	}

	start := time.Now()

	audioTrackIDp0 := make(chan string, 1)
	videoTrackIDp0 := make(chan string, 1)
	// Sized so no goroutine blocks on send and stalls wg.Wait below.
	errCh := make(chan streamErr, streamSendCount(plan.audio)+streamSendCount(plan.video))

	streamCtx, cancelStreams := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	// Drain errCh after wg.Wait so runtime errors past the startup select
	// surface as t.Errorf rather than blocked sends.
	t.Cleanup(func() {
		cancelStreams()
		wg.Wait()
		close(errCh)
		for e := range errCh {
			t.Errorf("publish stream %s/%s failed: %v",
				e.stream.participant, e.stream.codec, e.err)
		}
	})

	launch := func(s *streamPlan, idCh chan<- string) {
		captureID := s.participant == "p0"
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e, ok := r.runStream(streamCtx, s, start, idCh, captureID); !ok {
				errCh <- e
			}
		}()
	}
	for _, s := range plan.audio {
		launch(s, audioTrackIDp0)
	}
	for _, s := range plan.video {
		launch(s, videoTrackIDp0)
	}

	// Block until p0's trackIDs are live so the test can build its egress
	// request. Delayed-publish p0 streams use a "TBD" sentinel.
	needAudio := hasP0Stream(plan.audio) && firstP0Publish(plan.audio) == 0
	needVideo := hasP0Stream(plan.video) && firstP0Publish(plan.video) == 0
	for needAudio || needVideo {
		select {
		case id := <-audioTrackIDp0:
			test.audioTrackID = id
			needAudio = false
		case id := <-videoTrackIDp0:
			test.videoTrackID = id
			needVideo = false
		case e := <-errCh:
			require.NoError(t, e.err, "publish stream %s/%s failed", e.stream.participant, e.stream.codec)
		}
	}
	if hasP0Stream(plan.audio) && firstP0Publish(plan.audio) > 0 {
		test.audioTrackID = "TBD"
	}
	if hasP0Stream(plan.video) && firstP0Publish(plan.video) > 0 {
		test.videoTrackID = "TBD"
	}
}

// streamSendCount counts the maximum streamErrs runStream can emit per
// stream — one per publishTrack call (initial + each gapUnpublishRepublish).
func streamSendCount(streams []*streamPlan) int {
	n := 0
	for _, s := range streams {
		n++
		for _, g := range s.gaps {
			if g.method == gapUnpublishRepublish {
				n++
			}
		}
	}
	return n
}

func hasP0Stream(streams []*streamPlan) bool {
	for _, s := range streams {
		if s.participant == "p0" {
			return true
		}
	}
	return false
}

func firstP0Publish(streams []*streamPlan) time.Duration {
	for _, s := range streams {
		if s.participant == "p0" {
			return s.publish
		}
	}
	return 0
}

// runStream returns (zero, true) on clean exit; (err, false) on failure.
func (r *Runner) runStream(ctx context.Context, s *streamPlan, start time.Time, idCh chan<- string, captureID bool) (streamErr, bool) {
	if !sleepUntilCtx(ctx, start, s.publish) {
		return streamErr{}, true
	}

	p, err := r.localParticipant(s.participant)
	if err != nil {
		return streamErr{s, err}, false
	}
	pub, err := r.publishTrack(p, s.participant, s.codec)
	if err != nil {
		return streamErr{s, err}, false
	}
	// pub is captured by reference so gapUnpublishRepublish's reassignment
	// is picked up at exit. Don't t.Cleanup here — keeps cleanup
	// registrations on the test goroutine where LIFO ordering is sane.
	defer func() { _ = p.UnpublishTrack(pub.SID()) }()

	if captureID {
		select {
		case idCh <- pub.SID():
		default:
		}
	}

	for _, g := range s.gaps {
		if !sleepUntilCtx(ctx, start, g.start) {
			return streamErr{}, true
		}
		switch g.method {
		case gapMute:
			pub.SetMuted(true)
			if !sleepUntilCtx(ctx, start, g.end) {
				return streamErr{}, true
			}
			pub.SetMuted(false)
		case gapUnpublishRepublish:
			_ = p.UnpublishTrack(pub.SID())
			if !sleepUntilCtx(ctx, start, g.end) {
				return streamErr{}, true
			}
			newPub, err := r.publishTrack(p, s.participant, s.codec)
			if err != nil {
				return streamErr{s, err}, false
			}
			pub = newPub
		case gapDisconnect:
			pub.SimulateDisconnection(g.end - g.start)
			if !sleepUntilCtx(ctx, start, g.end) {
				return streamErr{}, true
			}
		}
	}

	if s.unpublish > 0 {
		if !sleepUntilCtx(ctx, start, s.unpublish) {
			return streamErr{}, true
		}
		_ = p.UnpublishTrack(pub.SID())
	}

	// Keep the track alive until the test ends; defer unpublishes on exit.
	<-ctx.Done()
	return streamErr{}, true
}

// publishTrack publishes a sample on lp. Caller owns track lifetime
// (no t.Cleanup registered here).
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

// publish publishes a p0 sample for custom test functions that manage
// their own SDK connection (e.g. late-track tests).
func (r *Runner) publish(t *testing.T, lp *lksdk.LocalParticipant, codec types.MimeType) *lksdk.LocalTrackPublication {
	pub, err := r.publishTrack(lp, "p0", codec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lp.UnpublishTrack(pub.SID()) })
	return pub
}

func (r *Runner) publishForParticipant(t *testing.T, lp *lksdk.LocalParticipant, participantName string, codec types.MimeType) *lksdk.LocalTrackPublication {
	pub, err := r.publishTrack(lp, participantName, codec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lp.UnpublishTrack(pub.SID()) })
	return pub
}

func (r *Runner) localParticipant(name string) (*lksdk.LocalParticipant, error) {
	switch name {
	case "p0":
		return r.room.LocalParticipant, nil
	case "p1":
		return r.p1Room.LocalParticipant, nil
	case "p2":
		return r.p2Room.LocalParticipant, nil
	}
	return nil, fmt.Errorf("unknown participant: %s", name)
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

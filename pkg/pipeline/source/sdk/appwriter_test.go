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

package sdk

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"
)

// TestShouldSendEOS pins the cleanup decision in AppWriter.start(): EOS is owed
// by any appsrc linked into the pipeline, reported PLAYING or not. The two differ
// during shutdown, when the notification is dropped (CS-1547).
func TestShouldSendEOS(t *testing.T) {
	t.Run("never added to the pipeline", func(t *testing.T) {
		w := &AppWriter{}

		require.False(t, w.shouldSendEOS(),
			"no appsrc in the pipeline means nothing downstream is waiting for EOS")
	})

	t.Run("added to the pipeline but PLAYING never delivered", func(t *testing.T) {
		w := &AppWriter{}
		w.MarkAddedToPipeline()

		require.False(t, w.playing.IsBroken(),
			"precondition: this is the race - the PLAYING notification was dropped")
		require.True(t, w.shouldSendEOS(),
			"CS-1547: the appsrc is linked into the pipeline, so cleanup must send EOS "+
				"even though we were never told it reached PLAYING")

		// this state is what AppWriter.start() logs as
		// "appsrc never reported PLAYING, sending EOS anyway"
	})

	t.Run("PLAYING implies added to the pipeline", func(t *testing.T) {
		w := &AppWriter{}
		w.Playing()

		require.True(t, w.addedToPipeline.IsBroken(),
			"an appsrc cannot reach PLAYING without being linked into the pipeline")
		require.True(t, w.shouldSendEOS(),
			"the normal path must keep sending EOS exactly as before")
	})
}

const (
	testAudioCaps     = "audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=1"
	testFrameSize     = 1920 // 20ms of 48kHz mono S16LE
	testFrameDuration = 20 * time.Millisecond
	eosWaitTimeout    = 5 * time.Second
)

type mixerTestPipeline struct {
	pipeline *gst.Pipeline
	mixer    *gst.Element
}

func newMixerTestPipeline(t *testing.T) *mixerTestPipeline {
	t.Helper()
	gst.Init(nil)

	pipeline, err := gst.NewPipeline("cs-1547")
	require.NoError(t, err)

	mixer, err := gst.NewElement("audiomixer")
	require.NoError(t, err)
	sink, err := gst.NewElement("fakesink")
	require.NoError(t, err)
	require.NoError(t, sink.SetProperty("sync", false))

	require.NoError(t, pipeline.AddMany(mixer, sink))
	require.NoError(t, mixer.Link(sink))

	p := &mixerTestPipeline{pipeline: pipeline, mixer: mixer}
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })
	return p
}

// addTrack links a new appsrc into the pipeline, exactly like OnTrackAdded().
func (p *mixerTestPipeline) addTrack(t *testing.T, name string) *app.Source {
	t.Helper()

	elem, err := gst.NewElementWithName("appsrc", "app_"+name)
	require.NoError(t, err)
	conv, err := gst.NewElement("audioconvert")
	require.NoError(t, err)

	src := app.SrcFromElement(elem)
	src.SetCaps(gst.NewCapsFromString(testAudioCaps))
	src.SetArg("format", "time")
	require.NoError(t, elem.SetProperty("is-live", false))

	require.NoError(t, p.pipeline.AddMany(elem, conv))
	require.NoError(t, gst.ElementLinkMany(elem, conv, p.mixer))

	// dynamic add: bring the new branch up to the pipeline's state
	require.True(t, elem.SyncStateWithParent())
	require.True(t, conv.SyncStateWithParent())

	return src
}

func pushSilence(t *testing.T, src *app.Source, frames int, startPTS time.Duration) time.Duration {
	t.Helper()

	pts := startPTS
	silence := make([]byte, testFrameSize)
	for i := 0; i < frames; i++ {
		b := gst.NewBufferFromBytes(silence)
		b.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))
		b.SetDuration(gst.ClockTime(uint64(testFrameDuration)))
		require.Equal(t, gst.FlowOK, src.PushBuffer(b))
		pts += testFrameDuration
	}
	return pts
}

// waitForEOS returns true if the pipeline reached EOS within eosWaitTimeout.
func (p *mixerTestPipeline) waitForEOS(t *testing.T) bool {
	t.Helper()

	bus := p.pipeline.GetPipelineBus()
	deadline := time.Now().Add(eosWaitTimeout)
	for time.Now().Before(deadline) {
		msg := bus.TimedPopFiltered(gst.ClockTime(uint64(500*time.Millisecond)), gst.MessageEOS|gst.MessageError)
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			return true
		case gst.MessageError:
			t.Fatalf("pipeline error: %s", msg.String())
		}
	}
	return false
}

// TestMissingEOSFreezesPipeline: an appsrc linked into the mixer that never sends
// EOS blocks EOS aggregation, and shutdown hangs.
func TestMissingEOSFreezesPipeline(t *testing.T) {
	p := newMixerTestPipeline(t)

	srcA := p.addTrack(t, "A")
	require.NoError(t, p.pipeline.SetState(gst.StatePlaying))
	pts := pushSilence(t, srcA, 10, 0)

	// a late subscription: the appsrc is linked into the running pipeline
	srcB := p.addTrack(t, "B")
	pushSilence(t, srcB, 2, pts)

	// shutdown: A sends EOS, B never does
	require.Equal(t, gst.FlowOK, srcA.EndStream())

	require.False(t, p.waitForEOS(t),
		"CS-1547: expected the pipeline to hang - it should NOT reach EOS while app_B never sent EOS")
}

// TestEOSFromAllWritersCompletes is the control: with EndStream() on every linked
// appsrc, EOS propagates immediately.
func TestEOSFromAllWritersCompletes(t *testing.T) {
	p := newMixerTestPipeline(t)

	srcA := p.addTrack(t, "A")
	require.NoError(t, p.pipeline.SetState(gst.StatePlaying))
	pts := pushSilence(t, srcA, 10, 0)

	srcB := p.addTrack(t, "B")
	pushSilence(t, srcB, 2, pts)

	require.Equal(t, gst.FlowOK, srcA.EndStream())
	require.Equal(t, gst.FlowOK, srcB.EndStream())

	require.True(t, p.waitForEOS(t), "pipeline should reach EOS once every appsrc sent EOS")
}

// TestEOSFromNotYetPlayingAppsrc: an appsrc that is linked and started, but not
// yet reported PLAYING, still delivers EOS to the mixer. AddSourceBin calls
// SyncStateWithParent, so this is the state a track added during shutdown is in.
//
// An appsrc left in NULL behaves differently: EndStream() returns FlowOK but
// nothing propagates and the pipeline hangs. Linking a bin without syncing its
// state would need more than EOS to shut down cleanly.
func TestEOSFromNotYetPlayingAppsrc(t *testing.T) {
	p := newMixerTestPipeline(t)

	srcA := p.addTrack(t, "A")
	require.NoError(t, p.pipeline.SetState(gst.StatePlaying))
	pts := pushSilence(t, srcA, 10, 0)

	// linked and started, but only as far as PAUSED
	elem, err := gst.NewElementWithName("appsrc", "app_B")
	require.NoError(t, err)
	conv, err := gst.NewElement("audioconvert")
	require.NoError(t, err)

	srcB := app.SrcFromElement(elem)
	srcB.SetCaps(gst.NewCapsFromString(testAudioCaps))
	srcB.SetArg("format", "time")
	require.NoError(t, elem.SetProperty("is-live", false))
	require.NoError(t, p.pipeline.AddMany(elem, conv))
	require.NoError(t, gst.ElementLinkMany(elem, conv, p.mixer))
	require.NoError(t, elem.SetState(gst.StatePaused))
	require.NoError(t, conv.SetState(gst.StatePaused))
	require.Equal(t, gst.StatePaused, elem.GetCurrentState(), "precondition: never reported PLAYING")

	pushSilence(t, srcB, 2, pts)
	require.Equal(t, gst.FlowOK, srcA.EndStream())
	require.Equal(t, gst.FlowOK, srcB.EndStream())

	require.True(t, p.waitForEOS(t),
		"EOS from a linked-but-not-yet-PLAYING appsrc must still complete the pipeline - "+
			"this is what makes addedToPipeline a valid guard")
}

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
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

var gstInitOnce sync.Once

// fixedPTS is a TrackSync that assigns every packet the same PTS.
type fixedPTS struct{}

var _ synchronizer.TrackSync = fixedPTS{}

func (fixedPTS) PrimeForStart(pkt jitter.ExtPacket) ([]jitter.ExtPacket, int, bool) {
	return []jitter.ExtPacket{pkt}, 0, true
}
func (fixedPTS) GetPTS(jitter.ExtPacket) (time.Duration, error) { return time.Millisecond, nil }
func (fixedPTS) OnSenderReport(func(time.Duration))             {}
func (fixedPTS) LastPTSAdjusted() time.Duration                 { return 0 }
func (fixedPTS) Close()                                         {}

// flushingHarness runs pushSamples against a real appsrc inside a pipeline the
// test can tear down mid-stream, the way Pipeline.Stop() does.
type flushingHarness struct {
	pipeline  *gst.Pipeline
	callbacks *gstreamer.Callbacks
	writer    *AppWriter
	errs      chan error
}

func newFlushingHarness(t *testing.T) *flushingHarness {
	t.Helper()
	gstInitOnce.Do(func() { gst.Init(nil) })

	pipeline, err := gst.NewPipeline("flushing-test")
	require.NoError(t, err)
	appsrc, err := gst.NewElement("appsrc")
	require.NoError(t, err)
	sink, err := gst.NewElement("fakesink")
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(appsrc, sink))
	require.NoError(t, appsrc.Link(sink))
	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })

	errs := make(chan error, 1)
	callbacks := &gstreamer.Callbacks{}
	callbacks.SetOnError(func(err error) { errs <- err })
	callbacks.OnPipelinePaused()

	w := &AppWriter{
		conf:         &config.PipelineConfig{},
		logger:       logger.GetLogger(),
		track:        &webrtc.TrackRemote{},
		src:          app.SrcFromElement(appsrc),
		callbacks:    callbacks,
		translator:   NewNullTranslator(),
		trackSync:    fixedPTS{},
		sendPLI:      func() {},
		timeProvider: gstreamer.NopTimeProvider(),
	}
	w.samplesCond = sync.NewCond(&w.samplesLock)
	w.Playing()

	return &flushingHarness{pipeline: pipeline, callbacks: callbacks, writer: w, errs: errs}
}

// enqueue hands pushSamples one sample of n packets, as the jitter buffer does.
func (h *flushingHarness) enqueue(n int) {
	sample := make([]jitter.ExtPacket, n)
	for i := range sample {
		sample[i] = jitter.ExtPacket{
			ReceivedAt: time.Now(),
			Packet: &rtp.Packet{
				Header:  rtp.Header{Version: 2, SequenceNumber: uint16(i), Timestamp: uint32(i) * 960},
				Payload: []byte{0},
			},
		}
	}
	h.writer.onPacket(sample)
}

func (h *flushingHarness) waitFinished(t *testing.T) {
	t.Helper()
	select {
	case <-h.writer.endStreamProcessed.Watch():
	case <-time.After(5 * time.Second):
		t.Fatal("pushSamples did not finish")
	}
}

func (h *flushingHarness) reportedError() (error, bool) {
	select {
	case err := <-h.errs:
		return err, true
	default:
		return nil, false
	}
}

// TestPushSamplesFlushingThreshold pins how pushSamples reacts once the appsrc
// has refused flushingThreshold consecutive buffers. While the pipeline is
// running, that is a stranded appsrc and is reported through OnError, whether
// or not the track itself is ending. Once Pipeline.Stop has begun, the refused
// backlog is the expected consequence of the teardown and must not turn an
// abort into a failed egress.
func TestPushSamplesFlushingThreshold(t *testing.T) {
	run := func(t *testing.T, trackDraining, pipelineStopping bool) (error, bool) {
		h := newFlushingHarness(t)
		w := h.writer

		go w.pushSamples()

		// one buffer through the live pipeline proves pushSamples is past its
		// start gates before the appsrc starts refusing buffers
		h.enqueue(1)
		require.Eventually(t, func() bool { return !w.lastPushed.Load().IsZero() },
			5*time.Second, 10*time.Millisecond)

		if pipelineStopping {
			// Pipeline.Stop runs OnStop before it sets the pipeline to NULL
			require.NoError(t, h.callbacks.OnStop())
		}
		require.NoError(t, h.pipeline.SetState(gst.StateNull))
		require.Equal(t, gst.FlowFlushing, w.src.PushBuffer(gst.NewBufferFromBytes([]byte{0})),
			"precondition: a stopped appsrc refuses buffers with FlowFlushing")

		if trackDraining {
			w.draining.Break()
		}
		for i := 0; i < 3; i++ {
			h.enqueue(flushingThreshold / 2)
		}
		w.endStreamSourceProcessed.Break()
		w.notifyPushSamples()
		h.waitFinished(t)

		return h.reportedError()
	}

	t.Run("live track in a running pipeline reports persistent flushing", func(t *testing.T) {
		err, reported := run(t, false, false)
		require.True(t, reported, "a stranded appsrc under a live track must fail the egress")
		require.ErrorIs(t, err, errors.ErrPersistentFlushing)
	})

	t.Run("ending track in a running pipeline still reports persistent flushing", func(t *testing.T) {
		err, reported := run(t, true, false)
		require.True(t, reported,
			"a track draining into a stranded appsrc must still fail the egress, or its queued tail is silently lost")
		require.ErrorIs(t, err, errors.ErrPersistentFlushing)
	})

	t.Run("stopping pipeline ends the track without an error", func(t *testing.T) {
		err, reported := run(t, true, true)
		require.False(t, reported,
			"a pipeline being torn down must not fail the egress when it refuses the queued backlog, got %v", err)
	})
}

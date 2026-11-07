package builder

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// fillerTestGuard is the room a source leaves between a filler GAP's end and the event it stops at
const fillerTestGuard = 200 * time.Millisecond

func TestCoordinatedFillerCoversNoTrackSpans(t *testing.T) {
	// a non-live selector whose filler is the source's appsrc: the source covers the spans with no video track with
	// GAPs, which become black at their own time, and a track takes the selector on its first frame and hands it back
	// on its EOS, with nothing of the track dropped and no black past where the source said
	initGStreamer(t)
	for _, codec := range gapTestCodecs {
		t.Run(codec.name, func(t *testing.T) {
			packets := gapTestRTPPackets(t, codec, 2*gapTestFPS)
			firstSecond, _ := splitAt(packets, time.Second)
			// a decoder may hold the last frame until its next input, and videorate the last frame it got
			held := 3 * gapTestFrame

			t.Run("TrackFromStart", func(t *testing.T) {
				// the source sends the filler nothing while a track is live, and the track's first frame is kept
				h := newCoordinatedFillerHarness(t, codec, true)
				h.push(firstSecond, 0)
				h.requireOutputThrough(time.Second - held)

				require.Equal(t, time.Duration(0), h.out[0].pts, "the track's first frame was dropped")
				requireColor(t, h.out, 0, time.Second-held, true)
			})

			t.Run("LatePublishAndEOS", func(t *testing.T) {
				h := newCoordinatedFillerHarness(t, codec, false)

				// before waiting at the publish event at 2 s, the source covers [0, 2 s - guard)
				h.fillerGap(0, 2*time.Second-fillerTestGuard)
				h.requireOutputThrough(2*time.Second - fillerTestGuard - held)

				// the track is added as the event is handled and its first frame is at 2 s
				h.addTrack()
				h.push(firstSecond, 2*time.Second)
				h.requireOutputThrough(3*time.Second - held)
				requireColor(t, h.out, 0, 2*time.Second-gapTestFrame, false)
				requireColor(t, h.out, 2*time.Second, 3*time.Second-held, true)

				// the track ends at 3 s; before waiting at the end event at 5 s, the source covers [3 s, 5 s - guard).
				// The track's last frame is repeated once as its segment closes, then the black starts.
				h.endTrack()
				h.fillerGap(3*time.Second, 2*time.Second-fillerTestGuard)
				h.requireOutputThrough(5*time.Second - fillerTestGuard - held)
				requireColor(t, h.out, 3*time.Second+2*gapTestFrame, 5*time.Second-fillerTestGuard-held, false)

				// at the end the source covers up to the last event, then the pipeline sends EOS
				h.fillerGap(5*time.Second-fillerTestGuard, fillerTestGuard)
				out := h.finish()
				requireColor(t, out, 3*time.Second+2*gapTestFrame, 5*time.Second-2*gapTestFrame, false)
				require.InDelta(t, float64(5*time.Second), float64(out[len(out)-1].pts), float64(2*gapTestFrame),
					"the output did not end where the source's last GAP did")
			})
		})
	}
}

type coordinatedFillerHarness struct {
	*videoGapHarness
	pipeline  *gstreamer.Pipeline
	callbacks *gstreamer.Callbacks
	filler    *app.Source
	codec     gapTestCodec
}

// newCoordinatedFillerHarness builds the video bin of a non-live, non-composited pipeline whose filler is an appsrc
// the test sends GAPs to, with one track at build when withTrack is set, and a raw video sink in place of the encoder
func newCoordinatedFillerHarness(t *testing.T, codec gapTestCodec, withTrack bool) *coordinatedFillerHarness {
	callbacks := &gstreamer.Callbacks{}
	p, err := gstreamer.NewPipeline("coordinated_filler_test", 0, callbacks)
	require.NoError(t, err)

	fillerEl, err := gst.NewElement("appsrc")
	require.NoError(t, err)
	filler := app.SrcFromElement(fillerEl)
	conf := &config.PipelineConfig{Live: false}
	conf.SourceType = types.SourceTypeSDK
	conf.VideoDecoding = true
	conf.Width, conf.Height, conf.Framerate = 64, 64, gapTestFPS
	conf.Latency.PipelineLatency = 3 * time.Second
	conf.VideoFillerSrc = filler

	h := &coordinatedFillerHarness{
		videoGapHarness: &videoGapHarness{t: t},
		pipeline:        p,
		callbacks:       callbacks,
		filler:          filler,
		codec:           codec,
	}
	if withTrack {
		conf.VideoTracks = []*config.TrackSource{h.newTrackSource()}
	}
	if err = BuildVideoBin(p, conf, func(string, int, int) {}); err != nil {
		t.Skipf("%s decoding elements not available: %v", codec.name, err)
	}
	sinkBin := p.NewBin("image_filler_test")
	out, err := gst.NewElement("appsink")
	require.NoError(t, err)
	out.SetArg("sync", "false")
	require.NoError(t, sinkBin.AddElement(out))
	h.sink = app.SinkFromElement(out)

	require.NoError(t, p.AddSinkBin(sinkBin))
	require.NoError(t, p.Link())
	require.NoError(t, p.SetState(gst.StatePlaying))
	_, ok := p.UpgradeState(gstreamer.StateRunning)
	require.True(t, ok)
	t.Cleanup(func() { _ = p.SetState(gst.StateNull) })
	return h
}

func (h *coordinatedFillerHarness) newTrackSource() *config.TrackSource {
	srcEl, err := gst.NewElement("appsrc")
	require.NoError(h.t, err)
	h.src = app.SrcFromElement(srcEl)
	return &config.TrackSource{
		TrackID: "TR_filler_test", TrackKind: lksdk.TrackKindVideo,
		MimeType: h.codec.mime, PayloadType: 96, ClockRate: 90000, AppSrc: h.src,
	}
}

// addTrack adds a track to the running pipeline the way the source does when it handles a publish event
func (h *coordinatedFillerHarness) addTrack() {
	h.callbacks.OnTrackAdded(h.newTrackSource())
}

func (h *coordinatedFillerHarness) fillerGap(start, duration time.Duration) {
	require.True(h.t, h.filler.SendEvent(gst.NewGapEvent(gst.ClockTime(start), gst.ClockTime(duration))))
}

func (h *coordinatedFillerHarness) endTrack() {
	h.src.EndStream()
}

// finish sends the pipeline's EOS, which ends the filler, and reads the output to its end
func (h *coordinatedFillerHarness) finish() []gapTestOutput {
	h.pipeline.SendEOS()
	for {
		sample := h.sink.TryPullSample(gst.ClockTime(5 * time.Second))
		if sample == nil {
			break
		}
		h.out = append(h.out, gapTestFrameOf(sample))
	}
	require.True(h.t, h.sink.IsEOS(), "the output stopped at %v", h.lastPTS())
	return h.out
}

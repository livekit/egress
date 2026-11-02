package builder

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
)

const (
	gapTestFPS     = 30
	gapTestFrame   = time.Second / gapTestFPS
	gapTestRawCaps = "video/x-raw,format=I420,width=64,height=64,framerate=30/1"
)

type timedRTP struct {
	pts  time.Duration
	data []byte
}

// gapTestOutput is one compositor output frame: its PTS and whether the muted input's white frame covers it
type gapTestOutput struct {
	pts   time.Duration
	white bool
}

// gapTestCodec encodes white frames to RTP, which buildVideoAppSrcBin decodes as a non-live compositor input
type gapTestCodec struct {
	name   string
	encode string
	mime   types.MimeType
}

var gapTestCodecs = []gapTestCodec{
	{
		name:   "H264",
		encode: "x264enc tune=zerolatency speed-preset=ultrafast key-int-max=30 ! rtph264pay config-interval=-1 pt=96",
		mime:   types.MimeTypeH264,
	},
	{
		name:   "VP8",
		encode: "vp8enc deadline=1 keyframe-max-dist=30 ! rtpvp8pay pt=96",
		mime:   types.MimeTypeVP8,
	},
	{
		name:   "VP9",
		encode: "vp9enc deadline=1 keyframe-max-dist=30 ! rtpvp9pay pt=96",
		mime:   types.MimeTypeVP9,
	},
	{
		name:   "AV1",
		encode: "svtav1enc preset=12 intra-period-length=29 ! av1parse ! rtpav1pay pt=96",
		mime:   types.MimeTypeAV1,
	},
}

func TestVideoGapFillKeepsCompositorFed(t *testing.T) {
	// a muted camera: its source sends a GAP and nothing more until the unmute, and the
	// non-live compositor keeps producing frames through the gap, with nothing drawn for that input
	initGStreamer(t)
	for _, codec := range gapTestCodecs {
		t.Run(codec.name, func(t *testing.T) {
			packets := gapTestRTPPackets(t, codec, 2*gapTestFPS)
			firstSecond, secondSecond := splitAt(packets, time.Second)

			t.Run("StartsMuted", func(t *testing.T) {
				h := newVideoGapHarness(t, codec)
				h.gap(0, time.Second)
				h.requireOutputThrough(time.Second - 2*gapTestFrame)
				h.push(firstSecond, time.Second)
				out := h.finish()

				requireColor(t, out, 0, time.Second-gapTestFrame, false)
				requireColor(t, out, time.Second+gapTestFrame, 2*time.Second-gapTestFrame, true)
			})

			t.Run("MutedMidStream", func(t *testing.T) {
				h := newVideoGapHarness(t, codec)
				h.push(firstSecond, 0)
				h.gap(time.Second, time.Second)
				h.requireOutputThrough(2*time.Second - 2*gapTestFrame)
				h.push(secondSecond, 2*time.Second)
				out := h.finish()

				requireColor(t, out, 0, time.Second-gapTestFrame, true)
				requireColor(t, out, time.Second+gapTestFrame, 2*time.Second-gapTestFrame, false)
				requireColor(t, out, 2*time.Second+gapTestFrame, 3*time.Second-gapTestFrame, true)
			})
		})
	}
}

type videoGapHarness struct {
	t    *testing.T
	src  *app.Source
	sink *app.Sink
	out  []gapTestOutput
}

// newVideoGapHarness builds a compositor input: buildVideoAppSrcBin's decoder for a non-live compositing
// pipeline, then videorate and caps into a non-live compositor
func newVideoGapHarness(t *testing.T, codec gapTestCodec) *videoGapHarness {
	p, err := gstreamer.NewPipeline("video_gap_test", 0, &gstreamer.Callbacks{})
	require.NoError(t, err)

	srcEl, err := gst.NewElement("appsrc")
	require.NoError(t, err)
	src := app.SrcFromElement(srcEl)
	conf := &config.PipelineConfig{Live: false}
	conf.Compositing = true
	ts := &config.TrackSource{TrackID: "TR_gap_test", MimeType: codec.mime, PayloadType: 96, ClockRate: 90000, AppSrc: src}
	srcBin, probe, err := buildVideoAppSrcBin(p.Bin, conf, ts, "track", true)
	if err != nil {
		t.Skipf("%s decoding elements not available: %v", codec.name, err)
	}
	t.Cleanup(probe.Close)

	sinkBin := p.NewBin("compositor_input")
	add := func(factory string, args ...string) *gst.Element {
		el, err := gst.NewElement(factory)
		require.NoError(t, err)
		for i := 0; i+1 < len(args); i += 2 {
			el.SetArg(args[i], args[i+1])
		}
		require.NoError(t, sinkBin.AddElement(el))
		return el
	}
	add("videoconvert")
	add("videoscale")
	rate := add("videorate", "skip-to-first", "true")
	add("capsfilter", "caps", gapTestRawCaps)
	add("compositor", "background", "black")
	add("capsfilter", "caps", gapTestRawCaps)
	out := add("appsink", "sync", "false")
	installVideoGapFill(rate.GetStaticPad("sink"))

	require.NoError(t, p.AddSourceBin(srcBin))
	require.NoError(t, p.AddSinkBin(sinkBin))
	require.NoError(t, p.Link())
	require.NoError(t, p.SetState(gst.StatePlaying))
	t.Cleanup(func() { _ = p.SetState(gst.StateNull) })
	return &videoGapHarness{t: t, src: src, sink: app.SinkFromElement(out)}
}

func (h *videoGapHarness) gap(start, duration time.Duration) {
	require.True(h.t, h.src.SendEvent(gst.NewGapEvent(gst.ClockTime(start), gst.ClockTime(duration))))
}

func (h *videoGapHarness) push(packets []timedRTP, offset time.Duration) {
	for _, pkt := range packets {
		buf := gst.NewBufferFromBytes(pkt.data)
		buf.SetPresentationTimestamp(gst.ClockTime(pkt.pts + offset))
		require.Equal(h.t, gst.FlowOK, h.src.PushBuffer(buf))
	}
}

// requireOutputThrough pulls compositor output until it reaches pts, with no more input than already sent
func (h *videoGapHarness) requireOutputThrough(pts time.Duration) {
	for len(h.out) == 0 || h.out[len(h.out)-1].pts < pts {
		sample := h.sink.TryPullSample(gst.ClockTime(5 * time.Second))
		require.NotNil(h.t, sample, "the compositor stopped at %v waiting on the muted input", h.lastPTS())
		h.out = append(h.out, gapTestFrameOf(sample))
	}
}

func (h *videoGapHarness) finish() []gapTestOutput {
	h.src.EndStream()
	for {
		sample := h.sink.TryPullSample(gst.ClockTime(5 * time.Second))
		if sample == nil {
			break
		}
		h.out = append(h.out, gapTestFrameOf(sample))
	}
	require.True(h.t, h.sink.IsEOS(), "the compositor stopped at %v", h.lastPTS())
	return h.out
}

func (h *videoGapHarness) lastPTS() time.Duration {
	if len(h.out) == 0 {
		return -1
	}
	return h.out[len(h.out)-1].pts
}

func gapTestFrameOf(sample *gst.Sample) gapTestOutput {
	buf := sample.GetBuffer()
	mapped := buf.Map(gst.MapRead)
	defer buf.Unmap()
	// the luma of the top-left pixel: the compositor's black background is 16, the encoded white frame near 235
	return gapTestOutput{pts: *buf.PresentationTimestamp().AsDuration(), white: mapped.Bytes()[0] > 128}
}

func requireColor(t *testing.T, out []gapTestOutput, from, to time.Duration, white bool) {
	t.Helper()
	var checked int
	for _, o := range out {
		if o.pts < from || o.pts > to {
			continue
		}
		checked++
		require.Equal(t, white, o.white, "frame at %v", o.pts)
	}
	require.Greater(t, checked, 0, "no output between %v and %v", from, to)
}

// gapTestRTPPackets encodes n white frames at 30fps with a keyframe every second and returns their RTP packets
func gapTestRTPPackets(t *testing.T, codec gapTestCodec, n int) []timedRTP {
	t.Helper()
	p, err := gst.NewPipelineFromString(fmt.Sprintf(
		"videotestsrc num-buffers=%d pattern=white ! %s ! %s ! appsink name=enc sync=false", n, gapTestRawCaps, codec.encode))
	if err != nil {
		t.Skipf("%s encoding elements not available", codec.name)
	}
	encEl, err := p.GetElementByName("enc")
	require.NoError(t, err)
	enc := app.SinkFromElement(encEl)
	require.NoError(t, p.SetState(gst.StatePlaying))
	defer func() { _ = p.SetState(gst.StateNull) }()

	var packets []timedRTP
	for {
		sample := enc.PullSample()
		if sample == nil {
			break
		}
		buf := sample.GetBuffer()
		packets = append(packets, timedRTP{pts: *buf.PresentationTimestamp().AsDuration(), data: append([]byte(nil), buf.Bytes()...)})
	}
	require.NotEmpty(t, packets)
	// encoders may offset their timestamps so DTS never goes negative
	base := packets[0].pts
	for i := range packets {
		packets[i].pts -= base
	}
	return packets
}

func splitAt(packets []timedRTP, pts time.Duration) (before, after []timedRTP) {
	for _, pkt := range packets {
		if pkt.pts < pts {
			before = append(before, pkt)
		} else {
			after = append(after, timedRTP{pts: pkt.pts - pts, data: pkt.data})
		}
	}
	return before, after
}

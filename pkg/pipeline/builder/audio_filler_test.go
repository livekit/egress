package builder

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const audioFillerTestPacket = 20 * time.Millisecond

func TestAudioFillerRunsMixerToItsCover(t *testing.T) {
	// a non-live mixer whose filler is the source's appsrc runs as far as the filler is covered, with no track or
	// past a track's EOS, mixes a track over the filler's silence, and ends where the filler's last GAP does
	initGStreamer(t)

	t.Run("NoTrack", func(t *testing.T) {
		h := newAudioFillerHarness(t, false)
		h.fillerGap(0, 2*time.Second)
		h.requireOutputThrough(2 * time.Second)

		// the mixer waits on the filler, so nothing comes out past the cover
		if sample := h.sink.TryPullSample(gst.ClockTime(500 * time.Millisecond)); sample != nil {
			h.out = append(h.out, audioFillerOutputOf(sample))
			require.LessOrEqual(t, h.end(), 2*time.Second, "the output ran past the filler's cover")
		}

		h.fillerGap(2*time.Second, time.Second)
		out := h.finish()
		requireAudioEnd(t, out, 3*time.Second)
		requireLoud(t, out, 0, 3*time.Second, false)
	})

	t.Run("TrackEndsFirst", func(t *testing.T) {
		h := newAudioFillerHarness(t, true)
		h.pushTone(time.Second)
		h.track.EndStream()
		h.fillerGap(0, 3*time.Second)
		out := h.finish()

		requireAudioEnd(t, out, 3*time.Second)
		requireLoud(t, out, 0, time.Second-audioFillerTestPacket, true)
		requireLoud(t, out, time.Second+audioFillerTestPacket, 3*time.Second, false)
	})
}

func TestNonLiveAudioMixingRequiresFiller(t *testing.T) {
	// without a filler a non-live mixer would stop at the last track's end and output nothing before the first, so
	// the audio bin refuses it
	initGStreamer(t)
	p, err := gstreamer.NewPipeline("no_audio_filler_test", 0, &gstreamer.Callbacks{})
	require.NoError(t, err)
	conf := &config.PipelineConfig{Live: false}
	conf.SourceType = types.SourceTypeSDK
	conf.AudioTranscoding = true
	conf.AudioOutCodec = types.MimeTypeRawAudio
	require.Error(t, BuildAudioBin(p, conf))
}

// audioFillerOutput is one mixer output buffer: its span and whether it carries the track's tone
type audioFillerOutput struct {
	pts, duration time.Duration
	loud          bool
}

type audioFillerHarness struct {
	t        *testing.T
	pipeline *gstreamer.Pipeline
	filler   *app.Source
	track    *app.Source
	sink     *app.Sink
	out      []audioFillerOutput
}

// newAudioFillerHarness builds the audio bin of a non-live raw audio pipeline whose filler is an appsrc the test sends
// GAPs to, with one PCMU track when withTrack is set, and an appsink in place of the encoder
func newAudioFillerHarness(t *testing.T, withTrack bool) *audioFillerHarness {
	p, err := gstreamer.NewPipeline("audio_filler_test", 0, &gstreamer.Callbacks{})
	require.NoError(t, err)

	fillerEl, err := gst.NewElement("appsrc")
	require.NoError(t, err)
	h := &audioFillerHarness{t: t, pipeline: p, filler: app.SrcFromElement(fillerEl)}

	conf := &config.PipelineConfig{Live: false}
	conf.SourceType = types.SourceTypeSDK
	conf.AudioTranscoding = true
	conf.AudioOutCodec = types.MimeTypeRawAudio
	conf.Latency.PipelineLatency = 3 * time.Second
	conf.AudioFillerSrc = h.filler
	if withTrack {
		trackEl, err := gst.NewElement("appsrc")
		require.NoError(t, err)
		h.track = app.SrcFromElement(trackEl)
		conf.AudioTracks = []*config.TrackSource{{
			TrackID: "TR_audio_filler_test", TrackKind: lksdk.TrackKindAudio,
			MimeType: types.MimeTypePCMU, PayloadType: 0, ClockRate: 8000, AppSrc: h.track,
		}}
	}
	require.NoError(t, BuildAudioBin(p, conf))

	sinkBin := p.NewBin("audio_filler_test_sink")
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

func (h *audioFillerHarness) fillerGap(start, duration time.Duration) {
	require.True(h.t, h.filler.SendEvent(gst.NewGapEvent(gst.ClockTime(start), gst.ClockTime(duration))))
}

// pushTone pushes the track's PCMU packets from 0 for duration, each at full scale
func (h *audioFillerHarness) pushTone(duration time.Duration) {
	samples := int(audioFillerTestPacket * 8000 / time.Second)
	for i := 0; time.Duration(i)*audioFillerTestPacket < duration; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    0,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * samples),
				SSRC:           1,
			},
			// mu-law 0x00 is the most negative sample
			Payload: make([]byte, samples),
		}
		data, err := pkt.Marshal()
		require.NoError(h.t, err)
		buf := gst.NewBufferFromBytes(data)
		buf.SetPresentationTimestamp(gst.ClockTime(time.Duration(i) * audioFillerTestPacket))
		require.Equal(h.t, gst.FlowOK, h.track.PushBuffer(buf))
	}
}

func (h *audioFillerHarness) requireOutputThrough(to time.Duration) {
	for h.end() < to {
		sample := h.sink.TryPullSample(gst.ClockTime(5 * time.Second))
		require.NotNil(h.t, sample, "the output stopped at %v, short of %v", h.end(), to)
		h.out = append(h.out, audioFillerOutputOf(sample))
	}
}

// finish sends the pipeline's EOS, which ends the filler, and reads the output to its end
func (h *audioFillerHarness) finish() []audioFillerOutput {
	h.pipeline.SendEOS()
	for {
		sample := h.sink.TryPullSample(gst.ClockTime(5 * time.Second))
		if sample == nil {
			break
		}
		h.out = append(h.out, audioFillerOutputOf(sample))
	}
	require.True(h.t, h.sink.IsEOS(), "the output stopped at %v", h.end())
	return h.out
}

func (h *audioFillerHarness) end() time.Duration {
	if len(h.out) == 0 {
		return 0
	}
	last := h.out[len(h.out)-1]
	return last.pts + last.duration
}

func audioFillerOutputOf(sample *gst.Sample) audioFillerOutput {
	buf := sample.GetBuffer()
	o := audioFillerOutput{pts: *buf.PresentationTimestamp().AsDuration(), duration: *buf.Duration().AsDuration()}
	data := buf.Bytes()
	for i := 0; i+1 < len(data); i += 2 {
		if v := int16(binary.LittleEndian.Uint16(data[i:])); v > 1000 || v < -1000 {
			o.loud = true
			break
		}
	}
	return o
}

func requireAudioEnd(t *testing.T, out []audioFillerOutput, end time.Duration) {
	t.Helper()
	require.NotEmpty(t, out)
	last := out[len(out)-1]
	require.InDelta(t, float64(end), float64(last.pts+last.duration), float64(audioFillerTestPacket),
		"the output did not end where the filler's last GAP did")
}

// requireLoud asserts every output buffer within [from, to) carries the tone, or none does
func requireLoud(t *testing.T, out []audioFillerOutput, from, to time.Duration, loud bool) {
	t.Helper()
	var checked int
	for _, o := range out {
		if o.pts < from || o.pts+o.duration > to {
			continue
		}
		checked++
		require.Equal(t, loud, o.loud, "buffer at %v", o.pts)
	}
	require.Greater(t, checked, 0, "no output between %v and %v", from, to)
}

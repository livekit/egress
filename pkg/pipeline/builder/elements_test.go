// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package builder

import (
	"fmt"
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

// Every element the builders can construct, with the properties they set on it.
// Codec-specific branches are only reached for the matching input or output, so
// most of these never run together in one pipeline, and some (vp9enc, vp9dec,
// faac) are not reached by the integration suite at all.
//
// Values carry the same Go type the builder passes. SetProperty compares the
// GValue type against the property's own type and errors on a mismatch, so a
// property that changes type upstream fails here rather than at pipeline build
// time on a production egress.

type prop struct {
	name  string
	value any
}

// capsValue defers gst.NewCapsFromString until after gst.Init: building caps in
// a package-level initializer crashes, because that runs before any test does.
type capsValue string

// enumArg is a property set through SetArg, which takes the value as a string
// and looks up the enum nickname. gst_util_set_object_arg returns silently when
// the property is missing or the value does not convert, so a renamed nickname
// leaves the element on its default with no error anywhere. value must differ
// from the default for the change to be observable.
type enumArg struct {
	name  string
	value string
}

type elementSpec struct {
	name string
	// bin names the builder that constructs this element, for failure output.
	bin      string
	props    []prop
	args     []enumArg
	padProps []prop
	// propExistsOnly covers properties whose value needs a live pipeline object,
	// so only their presence and not a write is checked.
	propExistsOnly []string
	// staticPads the builder retrieves by name and dereferences.
	staticPads []string
}

// Builder labels, used in failure output to name where an element is built.
const (
	binAudio    = "audio"
	binAudioSDK = "audio/sdk"
	binVideo    = "video"
	binVideoSDK = "video/sdk"
	binFile     = "file"
	binImage    = "image"

	binVideoImage = "video, image"
)

var elementInventory = []elementSpec{
	// --- audio input ---
	{name: "pulsesrc", bin: "audio/web", props: []prop{
		{"device", "EG_test.monitor"},
	}},
	{name: "appsrc", bin: "audio,video/sdk", props: []prop{
		{"is-live", true},
		{"block", true},
		{"caps", capsValue("application/x-rtp,media=audio,payload=111,encoding-name=OPUS,clock-rate=48000")},
	}},
	{name: "rtpopusdepay", bin: binAudioSDK},
	{name: "opusdec", bin: binAudioSDK},
	{name: "rtppcmudepay", bin: binAudioSDK},
	{name: "mulawdec", bin: binAudioSDK},
	{name: "rtppcmadepay", bin: binAudioSDK},
	{name: "alawdec", bin: binAudioSDK},

	// --- audio processing ---
	{name: "audiotestsrc", bin: binAudio, props: []prop{
		{"volume", 0.0},
		{"do-timestamp", true},
		{"is-live", true},
		{"samplesperbuffer", 960},
	}},
	{name: "audiomixer", bin: binAudio, props: []prop{
		{"latency", uint64(100000000)},
		{"alignment-threshold", uint64(100000000)},
	}, padProps: []prop{
		// Set on each sink pad as it is added, and only logged on failure, so
		// losing it stops QoS reporting without failing an egress.
		{"qos-messages", true},
	}},
	{name: "audioconvert", bin: binAudio},
	{name: "audioresample", bin: binAudio},
	{name: "audiorate", bin: "gstreamer.BuildAudioRate", props: []prop{
		{"skip-to-first", true},
		{"tolerance", uint64(40000000)},
	}},
	// Driven by the audio tempo controller through SetArg on every drift
	// correction. A missing property makes the whole correction a no-op.
	{name: "pitch", bin: "audio/pacer", args: []enumArg{
		{"tempo", "1.05"},
	}},

	// --- audio encoders ---
	{name: "opusenc", bin: binAudio, props: []prop{
		{"bitrate", int(96000)},
	}},
	{name: "faac", bin: binAudio, props: []prop{
		{"bitrate", int(128000)},
	}},
	{name: "lamemp3enc", bin: binAudio, props: []prop{
		{"cbr", true},
		{"bitrate", int(128)},
	}},

	// --- video input ---
	{name: "ximagesrc", bin: "video/web", props: []prop{
		{"display-name", ":0"},
		{"use-damage", false},
		{"show-pointer", false},
	}},
	{name: "rtph264depay", bin: binVideoSDK},
	{name: "h264parse", bin: "video/sdk, segment", staticPads: []string{"src"}},
	{name: "avdec_h264", bin: binVideoSDK},
	{name: "rtpvp8depay", bin: binVideoSDK},
	{name: "vp8dec", bin: binVideoSDK},
	{name: "rtpvp9depay", bin: binVideoSDK},
	{name: "vp9parse", bin: binVideoSDK},
	{name: "vp9dec", bin: binVideoSDK},
	{name: "videotestsrc", bin: binVideo, props: []prop{
		{"is-live", true},
	}},

	// --- video processing ---
	{name: "videoconvert", bin: binVideoImage},
	{name: "videoscale", bin: binVideoImage},
	{name: "videorate", bin: binVideoImage, props: []prop{
		{"skip-to-first", true},
	}},
	{name: "capsfilter", bin: "audio, video, image", props: []prop{
		{"caps", capsValue("video/x-raw,framerate=30/1")},
	}},
	{name: "input-selector", bin: binVideo, propExistsOnly: []string{"active-pad"}},
	{name: "tee", bin: "audio, video, stream", props: []prop{
		{"allow-not-linked", true},
	}},
	{name: "queue", bin: "gstreamer.BuildQueue, video", props: []prop{
		{"max-size-time", uint64(100000000)},
		{"max-size-bytes", uint(0)},
		{"max-size-buffers", uint(0)},
		{"min-threshold-time", uint64(500000000)},
	}, args: []enumArg{
		{"leaky", "downstream"},
	}},

	// --- video encoders ---
	{name: "x264enc", bin: binVideo, props: []prop{
		{"threads", uint(4)},
		{"key-int-max", uint(60)},
		{"vbv-buf-capacity", uint(2000)},
		{"bitrate", uint(3000)},
		{"option-string", "scenecut=0:nal-hrd=cbr"},
	}, args: []enumArg{
		{"speed-preset", "veryfast"},
	}},
	{name: "jpegenc", bin: binImage},

	// --- muxers ---
	{name: "oggmux", bin: binFile},
	{name: "avmux_ivf", bin: binFile},
	{name: "mp4mux", bin: binFile},
	{name: "webmmux", bin: binFile},
	{name: "xingmux", bin: "file/mp3"},
	{name: "flvmux", bin: "stream/rtmp", props: []prop{
		{"streamable", true},
		{"skip-backwards-streams", true},
		{"latency", uint64(100000000)},
	}},
	{name: "mpegtsmux", bin: "stream/srt, segment", props: []prop{
		{"latency", uint64(100000000)},
	}},

	// --- sinks ---
	{name: "filesink", bin: binFile, props: []prop{
		{"location", "/dev/null"},
		{"sync", false},
		{"async", false},
	}},
	{name: "multifilesink", bin: binImage, props: []prop{
		{"post-messages", true},
		{"location", "/dev/null"},
	}},
	{name: "splitmuxsink", bin: "segment", props: []prop{
		{"max-size-time", uint64(6000000000)},
		{"send-keyframe-requests", true},
		{"muxer-factory", "mpegtsmux"},
	}},
	{name: "rtmp2sink", bin: "stream/rtmp", props: []prop{
		{"location", "rtmp://localhost:1935/live/test"},
		{"async-connect", false},
		{"async", false},
		{"sync", false},
	}},
	{name: "srtsink", bin: "stream/srt", props: []prop{
		{"uri", "srt://localhost:8890?streamid=publish:test"},
		{"wait-for-connection", false},
		{"async", false},
		{"sync", false},
	}},
	{name: "appsink", bin: "websocket"},
	{name: "fakesink", bin: binImage},
}

func TestElementInventory(t *testing.T) {
	initGStreamer(t)

	for _, spec := range elementInventory {
		t.Run(spec.name, func(t *testing.T) {
			e, err := gst.NewElement(spec.name)
			require.NoError(t, err, "%s constructs no %s; the plugin providing it is missing from the image",
				spec.bin, spec.name)
			require.NotNil(t, e)

			for _, p := range spec.props {
				require.NoError(t, e.SetProperty(p.name, propValue(t, p)),
					"%s sets %s on %s", spec.bin, p.name, spec.name)
			}

			for _, name := range spec.propExistsOnly {
				_, err := e.GetPropertyType(name)
				require.NoError(t, err, "%s sets %s on %s", spec.bin, name, spec.name)
			}

			for _, a := range spec.args {
				requireSetArgApplies(t, e, spec, a)
			}

			for _, name := range spec.staticPads {
				require.NotNil(t, e.GetStaticPad(name),
					"%s dereferences the %q pad of %s", spec.bin, name, spec.name)
			}

			for _, p := range spec.padProps {
				requireSinkPadProperty(t, e, spec, p)
			}
		})
	}
}

// propValue builds a caps value now that GStreamer is initialized, and passes
// every other value through.
func propValue(t *testing.T, p prop) any {
	t.Helper()

	cs, ok := p.value.(capsValue)
	if !ok {
		return p.value
	}

	caps := gst.NewCapsFromString(string(cs))
	require.NotNil(t, caps, "caps string for %s no longer parses: %s", p.name, cs)
	return caps
}

// requireSetArgApplies reads the property back, because SetArg reports nothing
// when it does not apply.
func requireSetArgApplies(t *testing.T, e *gst.Element, spec elementSpec, a enumArg) {
	t.Helper()

	before, err := e.GetProperty(a.name)
	require.NoError(t, err, "%s sets %s on %s via SetArg", spec.bin, a.name, spec.name)

	e.SetArg(a.name, a.value)

	after, err := e.GetProperty(a.name)
	require.NoError(t, err)
	require.NotEqual(t, before, after,
		"SetArg(%q, %q) left %s on its default (%v); the value no longer converts and %s silently keeps the default",
		a.name, a.value, spec.name, before, spec.bin)
}

// requireSinkPadProperty checks a property the builder sets on a requested sink
// pad rather than on the element.
func requireSinkPadProperty(t *testing.T, e *gst.Element, spec elementSpec, p prop) {
	t.Helper()

	pad := e.GetRequestPad("sink_%u")
	require.NotNil(t, pad, "%s requests a sink pad from %s", spec.bin, spec.name)

	require.NoError(t, pad.SetProperty(p.name, propValue(t, p)),
		"%s sets %s on a %s sink pad", spec.bin, p.name, spec.name)
}

// Caps are built from strings at pipeline build time, and NewCapsFromString
// returns nil on a parse failure rather than an error, so an unparseable string
// reaches SetProperty as a nil GstCaps. Shapes here mirror the builders, with
// representative values substituted for the runtime ones.
func TestCapsStringsParse(t *testing.T) {
	initGStreamer(t)

	for _, capsStr := range []string{
		// audio.go
		"application/x-rtp,media=audio,payload=111,encoding-name=OPUS,clock-rate=48000",
		"application/x-rtp,media=audio,payload=0,encoding-name=PCMU,clock-rate=8000",
		"application/x-rtp,media=audio,payload=8,encoding-name=PCMA,clock-rate=8000",
		"audio/x-raw,format=F32LE,layout=interleaved,rate=48000,channels=2",
		"audio/x-raw,format=F32LE,layout=interleaved,rate=44100,channels=1,channel-mask=(bitmask)0x1",
		"audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2",
		"audio/x-raw,format=S16LE,layout=interleaved,rate=44100,channels=1,channel-mask=(bitmask)0x2",
		// video.go
		"application/x-rtp,media=video,payload=96,encoding-name=H264,clock-rate=90000",
		"application/x-rtp,media=video,payload=96,encoding-name=VP8,clock-rate=90000",
		"application/x-rtp,media=video,payload=98,encoding-name=VP9,clock-rate=90000",
		"video/x-h264,stream-format=byte-stream",
		"video/x-vp9,width=[16,2147483647],height=[16,2147483647]",
		"video/x-raw,framerate=30/1",
		"video/x-raw,framerate=30/1,format=I420,width=1920,height=1080,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
		"video/x-raw,format=I420,width=1920,height=1080,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
		// image.go, which leaves a trailing comma when dimensions are set
		"video/x-raw,framerate=1/10,format=I420,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
		"video/x-raw,framerate=1/10,format=I420,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1,width=640,height=360,",
	} {
		t.Run(truncate(capsStr), func(t *testing.T) {
			require.NotNil(t, gst.NewCapsFromString(capsStr), "caps string no longer parses")
		})
	}
}

// The h264 encoder caps carry a multiview flag set, whose serialization the
// caps parser has to round-trip for the profile to reach x264enc.
func TestH264ProfileCapsParse(t *testing.T) {
	initGStreamer(t)

	for _, profile := range []string{"baseline", "main", "high"} {
		t.Run(profile, func(t *testing.T) {
			capsStr := fmt.Sprintf(
				"video/x-h264,profile=%s,multiview-mode=mono,multiview-flags=(GstVideoMultiviewFlagsSet)0:ffffffff:/right-view-first/left-flipped/left-flopped/right-flipped/right-flopped/half-aspect/mixed-mono",
				profile,
			)
			caps := gst.NewCapsFromString(capsStr)
			require.NotNil(t, caps, "caps string no longer parses")

			s := caps.GetStructureAt(0)
			require.NotNil(t, s)
			got, err := s.GetValue("profile")
			require.NoError(t, err)
			require.Equal(t, profile, got)
		})
	}
}

func truncate(s string) string {
	if len(s) <= 48 {
		return s
	}
	return s[:48]
}

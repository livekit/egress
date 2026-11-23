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

package gstreamer

import (
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

// A pipeline's graph names its stream sinks and where they send, without the
// stream keys and passphrases their properties hold.
func TestDebugBinToDotDataRedactsStreamSecrets(t *testing.T) {
	gst.Init(nil)

	sinks := []struct {
		name     string
		factory  string
		property string
		url      string
		secrets  []string
		kept     []string
	}{
		{
			name:     "rtmp",
			factory:  "rtmp2sink",
			property: "location",
			url:      "rtmp://rtmp.example.com/app1/abcd-1234-efgh-5678",
			secrets:  []string{"abcd-1234-efgh-5678"},
			kept:     []string{"rtmp.example.com", "app1"},
		},
		{
			// longer than the graph shows of a value
			name:     "rtmps with a query",
			factory:  "rtmp2sink",
			property: "location",
			url:      "rtmps://live.example.com:443/rtmp/FB-1234567890-0-AbCdEfGhIjKlMnOp?s_bl=1&s_oil=2&s_psm=1&s_pub=1&s_sw=0&a=AbxYzAbxYz",
			secrets:  []string{"FB-1234567890", "AbCdEfGhIjKlMnOp", "AbxYzAbxYz"},
			kept:     []string{"live.example.com"},
		},
		{
			name:     "rtmp with credentials",
			factory:  "rtmp2sink",
			property: "location",
			url:      "rtmp://someuser:somepassword@auth.example.com/app2/key-9876",
			secrets:  []string{"someuser", "somepassword", "key-9876"},
			kept:     []string{"auth.example.com", "app2"},
		},
		{
			name:     "srt",
			factory:  "srtsink",
			property: "uri",
			url:      "srt://srt.example.com:7001?streamid=publish:mykey123&passphrase=supersecretpass&pbkeylen=16",
			secrets:  []string{"mykey123", "supersecretpass"},
			kept:     []string{"srt.example.com:7001", "pbkeylen=16"},
		},
	}

	for _, details := range []struct {
		name  string
		flags gst.DebugGraphDetails
	}{
		{"all", gst.DebugGraphShowAll},
		{"full params", gst.DebugGraphShowAll | gst.DebugGraphShowFullParams},
	} {
		t.Run(details.name, func(t *testing.T) {
			p, err := NewPipeline("dot-test", 0, &Callbacks{})
			require.NoError(t, err)

			// a file sink's location is a path, which stays
			file, err := gst.NewElementWithName("filesink", "file_sink")
			require.NoError(t, err)
			require.NoError(t, file.SetProperty("location", "/tmp/egress/recording.mp4"))
			elements := []*gst.Element{file}

			for i, s := range sinks {
				sink, err := gst.NewElementWithName(s.factory, "stream_sink_"+string(rune('a'+i)))
				require.NoError(t, err, s.name)
				require.NoError(t, sink.SetProperty(s.property, s.url), s.name)
				elements = append(elements, sink)
			}
			require.NoError(t, p.AddElements(elements...))

			dot := p.DebugBinToDotData(details.flags)

			require.Contains(t, dot, "/tmp/egress/recording.mp4")
			for i, s := range sinks {
				require.Contains(t, dot, "stream_sink_"+string(rune('a'+i)), s.name)
				for _, secret := range s.secrets {
					require.NotContains(t, dot, secret, s.name)
				}
				for _, kept := range s.kept {
					require.Contains(t, dot, kept, s.name)
				}
			}
		})
	}
}

// Graphs differ by GStreamer version in how they separate an element's
// properties, and newer ones repeat them in a tooltip.
func TestRedactDotFormats(t *testing.T) {
	for name, c := range map[string]struct{ dot, want string }{
		"one property per line": {
			dot:  "label=\"GstRtmp2Sink\\nsink\\n[>]\nhost=\\\"example.com\\\"\nlocation=\\\"rtmp://example.com/live/key\\\"\nstream=\\\"key\\\"\";",
			want: "label=\"GstRtmp2Sink\\nsink\\n[>]\nhost=\\\"example.com\\\"\nlocation=\\\"rtmp://{redacted}\\\"\nstream=\\\"{redacted}\\\"\";",
		},
		"literal separators": {
			dot:  `label="GstRtmp2Sink\nsink\n[>]\nhost=\"example.com\"\nlocation=\"rtmp://example.com/live/key\"\nstream=\"key\"";`,
			want: `label="GstRtmp2Sink\nsink\n[>]\nhost=\"example.com\"\nlocation=\"rtmp://{redacted}\"\nstream=\"{redacted}\"";`,
		},
		"tooltip": {
			dot:  `tooltip="stream=\"key\"&#10;host=\"example.com\"&#10;location=\"rtmp://example.com/live/key\"";`,
			want: `tooltip="stream=\"{redacted}\"&#10;host=\"example.com\"&#10;location=\"rtmp://{redacted}\"";`,
		},
		"value cut short": {
			dot:  "\nuri=\\\"srt://example.com:7001?passphrase=secret&streamid=publ…\npbkeylen=16",
			want: "\nuri=\\\"srt://example.com:7001?{redacted}…\npbkeylen=16",
		},
		"other elements": {
			dot:  "\nlocation=\\\"/tmp/out.mp4\\\"\nuri=\\\"https://example.com/page\\\"\nstream-format=byte-stream",
			want: "\nlocation=\\\"/tmp/out.mp4\\\"\nuri=\\\"https://example.com/page\\\"\nstream-format=byte-stream",
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, c.want, redactDot(c.dot))
		})
	}
}

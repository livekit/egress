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

package builder

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
)

func TestSetSegmentMuxer(t *testing.T) {
	initGStreamer(t)

	for _, test := range []struct {
		outputType types.OutputType
		factory    string
	}{
		{outputType: types.OutputTypeTS, factory: tsMuxerFactory},
		{outputType: types.OutputTypeM4S, factory: fmp4MuxerFactory},
	} {
		t.Run(string(test.outputType), func(t *testing.T) {
			sink, err := gst.NewElement("splitmuxsink")
			require.NoError(t, err)

			require.NoError(t, setSegmentMuxer(sink, &config.SegmentConfig{SegmentOutputType: test.outputType}))

			factory, err := sink.GetProperty("muxer-factory")
			require.NoError(t, err)
			require.Equal(t, test.factory, factory)

			// muxer-properties is only applied when async-finalize is set, so the muxer
			// is configured from the muxer-added callback instead
			async, err := sink.GetProperty("async-finalize")
			require.NoError(t, err)
			require.Equal(t, false, async)
		})
	}
}

func TestConfigureFMP4Muxer(t *testing.T) {
	initGStreamer(t)

	muxer, err := gst.NewElement(fmp4MuxerFactory)
	require.NoError(t, err)

	require.NoError(t, configureFMP4Muxer(muxer))

	fragmentDuration, err := muxer.GetProperty("fragment-duration")
	require.NoError(t, err)
	require.Equal(t, uint64(24*time.Hour), fragmentDuration)
}

// TestSegmentElementProperties fails if an element the segment bin configures loses a
// property it sets. The fmp4 muxers come from gst-plugins-rs, so they move faster than
// the rest of the image.
func TestSegmentElementProperties(t *testing.T) {
	initGStreamer(t)

	for _, test := range []struct {
		element    string
		properties []string
	}{
		{
			element:    "splitmuxsink",
			properties: []string{"max-size-time", "send-keyframe-requests", "muxer-factory", "async-finalize"},
		},
		{
			element:    fmp4MuxerFactory,
			properties: []string{"fragment-duration"},
		},
	} {
		t.Run(test.element, func(t *testing.T) {
			e, err := gst.NewElement(test.element)
			require.NoError(t, err)

			for _, property := range test.properties {
				_, err = e.GetProperty(property)
				require.NoError(t, err, "%s has no %s property", test.element, property)
			}
		})
	}
}

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

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
)

func TestImageKeyframeInterval(t *testing.T) {
	images := func(intervals ...uint32) []OutputConfig {
		var out []OutputConfig
		for _, i := range intervals {
			out = append(out, &ImageConfig{CaptureInterval: i})
		}
		return out
	}

	for _, test := range []struct {
		name     string
		enabled  bool
		outputs  map[types.EgressType][]OutputConfig
		expected time.Duration
	}{
		{
			name:     "disabled",
			enabled:  false,
			outputs:  map[types.EgressType][]OutputConfig{types.EgressTypeImages: images(10)},
			expected: 0,
		},
		{
			name:     "single image output",
			enabled:  true,
			outputs:  map[types.EgressType][]OutputConfig{types.EgressTypeImages: images(10)},
			expected: 10 * time.Second,
		},
		{
			name:     "several image outputs share the greatest common divisor",
			enabled:  true,
			outputs:  map[types.EgressType][]OutputConfig{types.EgressTypeImages: images(10, 4, 30)},
			expected: 2 * time.Second,
		},
		{
			name:     "equal intervals keep the interval",
			enabled:  true,
			outputs:  map[types.EgressType][]OutputConfig{types.EgressTypeImages: images(6, 6)},
			expected: 6 * time.Second,
		},
		{
			name:     "common divisor below the minimum would request a keyframe too often",
			enabled:  true,
			outputs:  map[types.EgressType][]OutputConfig{types.EgressTypeImages: images(5, 7)},
			expected: 0,
		},
		{
			name:    "images alongside a file need every frame",
			enabled: true,
			outputs: map[types.EgressType][]OutputConfig{
				types.EgressTypeImages: images(10),
				types.EgressTypeFile:   {&FileConfig{}},
			},
			expected: 0,
		},
		{
			name:     "interval below the minimum would request a keyframe too often",
			enabled:  true,
			outputs:  map[types.EgressType][]OutputConfig{types.EgressTypeImages: images(10, 1)},
			expected: 0,
		},
		{
			name:     "no image output",
			enabled:  true,
			outputs:  map[types.EgressType][]OutputConfig{types.EgressTypeFile: {&FileConfig{}}},
			expected: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &PipelineConfig{Outputs: test.outputs}
			p.ImageKeyframeDecoding = test.enabled

			require.Equal(t, test.expected, p.ImageKeyframeInterval())
		})
	}
}

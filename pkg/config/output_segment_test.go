// Copyright 2025 LiveKit, Inc.
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
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

func TestGetSegmentDuration(t *testing.T) {
	for _, test := range []struct {
		name        string
		segments    *livekit.SegmentedFileOutput
		expected    time.Duration
		invalid     bool
		errContains string
	}{
		{
			name:     "default",
			segments: &livekit.SegmentedFileOutput{},
			expected: defaultSegmentDuration,
		},
		{
			name:     "deprecated field",
			segments: &livekit.SegmentedFileOutput{SegmentDuration: 6}, //nolint:staticcheck // deprecated, still supported
			expected: 6 * time.Second,
		},
		{
			name:     "seconds",
			segments: &livekit.SegmentedFileOutput{SegmentDurationSeconds: 2.5},
			expected: 2500 * time.Millisecond,
		},
		{
			name: "seconds takes precedence",
			segments: &livekit.SegmentedFileOutput{
				SegmentDuration:        6, //nolint:staticcheck // deprecated, still supported
				SegmentDurationSeconds: 2.5,
			},
			expected: 2500 * time.Millisecond,
		},
		{
			name:     "at the minimum",
			segments: &livekit.SegmentedFileOutput{SegmentDurationSeconds: 0.3},
			expected: minSegmentDuration,
		},
		{
			name:        "below the minimum",
			segments:    &livekit.SegmentedFileOutput{SegmentDurationSeconds: 0.25},
			invalid:     true,
			errContains: "segment_duration_seconds is 250ms, below the 300ms minimum",
		},
		{
			name:     "negative",
			segments: &livekit.SegmentedFileOutput{SegmentDurationSeconds: -1},
			invalid:  true,
		},
		{
			name:     "rounds to zero",
			segments: &livekit.SegmentedFileOutput{SegmentDurationSeconds: 1e-12},
			invalid:  true,
		},
		{
			name:     "NaN",
			segments: &livekit.SegmentedFileOutput{SegmentDurationSeconds: math.NaN()},
			invalid:  true,
		},
		{
			name:     "Inf",
			segments: &livekit.SegmentedFileOutput{SegmentDurationSeconds: math.Inf(1)},
			invalid:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, err := getSegmentDuration(test.segments)
			if test.invalid {
				require.Error(t, err)
				if test.errContains != "" {
					require.Contains(t, err.Error(), test.errContains)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.expected, d)
		})
	}
}

func TestKeyFrameIntervalMinimum(t *testing.T) {
	for _, test := range []struct {
		name             string
		keyFrameInterval float64
		invalid          bool
	}{
		{name: "unset", keyFrameInterval: 0},
		{name: "above the minimum", keyFrameInterval: 2},
		{name: "at the minimum", keyFrameInterval: 0.3},
		{name: "below the minimum", keyFrameInterval: 0.25, invalid: true},
		{name: "negative", keyFrameInterval: -1, invalid: true},
		{name: "NaN", keyFrameInterval: math.NaN(), invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &PipelineConfig{}
			err := p.applyAdvanced(&livekit.EncodingOptions{KeyFrameInterval: test.keyFrameInterval})
			if test.invalid {
				require.Error(t, err)
				require.Contains(t, err.Error(), "key_frame_interval")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.keyFrameInterval, p.KeyFrameInterval)
		})
	}
}

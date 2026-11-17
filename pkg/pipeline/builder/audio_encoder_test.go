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

package builder

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const faacAverageBitrateMode = 2

func TestNewAACEncoderTargetsRequestedBitrate(t *testing.T) {
	initGStreamer(t)

	faac, err := newAACEncoder(32)
	require.NoError(t, err)

	rateControl, err := faac.GetProperty("rate-control")
	require.NoError(t, err)
	require.Equal(t, faacAverageBitrateMode, rateControl)

	bitrate, err := faac.GetProperty("bitrate")
	require.NoError(t, err)
	require.Equal(t, 32000, bitrate)
}

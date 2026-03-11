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

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func TestFileOutputRetrySafety(t *testing.T) {
	for _, test := range []struct {
		name       string
		retryCount int32
		filepath   string
		expectErr  bool
	}{
		{
			name:       "first attempt with explicit path",
			retryCount: 0,
			filepath:   "recordings/my-file.mp4",
			expectErr:  false,
		},
		{
			name:       "retry with empty path (auto-generated)",
			retryCount: 1,
			filepath:   "",
			expectErr:  false,
		},
		{
			name:       "retry with directory path (auto-generated)",
			retryCount: 1,
			filepath:   "recordings/",
			expectErr:  false,
		},
		{
			name:       "retry with {retry} placeholder",
			retryCount: 1,
			filepath:   "recordings/my-file-{retry}.mp4",
			expectErr:  false,
		},
		{
			name:       "retry with explicit path missing {retry}",
			retryCount: 1,
			filepath:   "recordings/my-file.mp4",
			expectErr:  true,
		},
		{
			name:       "retry with {retry} in directory part",
			retryCount: 1,
			filepath:   "recordings/{retry}/my-file.mp4",
			expectErr:  false,
		},
		{
			name:       "second retry with {retry} placeholder",
			retryCount: 2,
			filepath:   "recordings/my-file-{retry}.mp4",
			expectErr:  false,
		},
		{
			name:       "second retry with explicit path missing {retry}",
			retryCount: 2,
			filepath:   "recordings/my-file.mp4",
			expectErr:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &PipelineConfig{
				Info:    &livekit.EgressInfo{RoomName: "test-room", RetryCount: test.retryCount},
				TmpDir:  t.TempDir(),
				Outputs: make(map[types.EgressType][]OutputConfig),
			}

			_, err := p.getEncodedFileConfig(&livekit.EncodedFileOutput{
				FileType: livekit.EncodedFileType_MP4,
				Filepath: test.filepath,
			})

			if test.expectErr {
				require.ErrorIs(t, err, errors.ErrNonRetryableOutput)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSegmentOutputRetrySafety(t *testing.T) {
	for _, test := range []struct {
		name       string
		retryCount int32
		prefix     string
		playlist   string
		expectErr  bool
	}{
		{
			name:       "first attempt with explicit prefix",
			retryCount: 0,
			prefix:     "segments/my-stream",
			playlist:   "segments/playlist",
			expectErr:  false,
		},
		{
			name:       "retry with both empty (auto-generated)",
			retryCount: 1,
			prefix:     "",
			playlist:   "",
			expectErr:  false,
		},
		{
			name:       "retry with {retry} in prefix only",
			retryCount: 1,
			prefix:     "segments/my-stream-{retry}",
			playlist:   "segments/playlist",
			expectErr:  false,
		},
		{
			name:       "retry with {retry} in playlist only",
			retryCount: 1,
			prefix:     "segments/my-stream",
			playlist:   "segments/playlist-{retry}",
			expectErr:  false,
		},
		{
			name:       "retry with {retry} in both",
			retryCount: 1,
			prefix:     "segments/my-stream-{retry}",
			playlist:   "segments/playlist-{retry}",
			expectErr:  false,
		},
		{
			name:       "retry with explicit prefix missing {retry}",
			retryCount: 1,
			prefix:     "segments/my-stream",
			playlist:   "",
			expectErr:  true,
		},
		{
			name:       "retry with explicit playlist missing {retry}",
			retryCount: 1,
			prefix:     "",
			playlist:   "segments/playlist",
			expectErr:  true,
		},
		{
			name:       "retry with both explicit and neither has {retry}",
			retryCount: 1,
			prefix:     "segments/my-stream",
			playlist:   "segments/playlist",
			expectErr:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &PipelineConfig{
				Info:    &livekit.EgressInfo{EgressId: "test_egress", RoomName: "test-room", RetryCount: test.retryCount},
				TmpDir:  t.TempDir(),
				Outputs: make(map[types.EgressType][]OutputConfig),
			}

			_, err := p.getSegmentConfig(&livekit.SegmentedFileOutput{
				FilenamePrefix: test.prefix,
				PlaylistName:   test.playlist,
			})

			if test.expectErr {
				require.ErrorIs(t, err, errors.ErrNonRetryableOutput)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
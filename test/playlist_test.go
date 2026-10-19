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

//go:build integration

package test

import (
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadPlaylist(t *testing.T) {
	dir := t.TempDir()

	ts := "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:04.814Z\n#EXTINF:5.994,\np_00000.ts\n" +
		"#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:10.808Z\n#EXTINF:5.994,\np_00001.ts\n#EXT-X-ENDLIST\n"
	fmp4 := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MAP:URI=\"p_init.mp4\"\n" +
		"#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:04.814Z\n#EXTINF:5.994,\np_00000.m4s\n" +
		"#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:10.808Z\n#EXTINF:5.994,\np_00001.m4s\n#EXT-X-ENDLIST\n"
	live := "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:1\n" +
		"#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:04.814Z\n#EXTINF:5.994,\np_00001.ts\n"

	for _, tc := range []struct {
		name, body, init, first string
		version                 int
		mediaType               string
		segs                    int
		closed                  bool
	}{
		{"ts", ts, "", "p_00000.ts", 4, "EVENT", 2, true},
		{"fmp4", fmp4, "p_init.mp4", "p_00000.m4s", 7, "EVENT", 2, true},
		{"live", live, "", "p_00001.ts", 4, "", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := path.Join(dir, tc.name+".m3u8")
			require.NoError(t, os.WriteFile(f, []byte(tc.body), 0644))

			p, err := readPlaylist(f)
			require.NoError(t, err)
			require.Equal(t, tc.version, p.Version)
			require.Equal(t, tc.mediaType, p.MediaType)
			require.Equal(t, 6, p.TargetDuration)
			require.Equal(t, tc.init, p.InitSegment)
			require.Equal(t, tc.closed, p.Closed)
			require.Len(t, p.Segments, tc.segs)
			require.Equal(t, tc.first, p.Segments[0].Filename)
			require.Equal(t, 5.994, p.Segments[0].Duration)
			require.False(t, p.Segments[0].ProgramDateTime.IsZero())
		})
	}
}

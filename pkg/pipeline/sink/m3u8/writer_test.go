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

package m3u8

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEventPlaylistWriter(t *testing.T) {
	playlistName := "playlist.m3u8"

	w, err := NewEventPlaylistWriter(playlistName, 6)
	require.NoError(t, err)

	t.Cleanup(func() { _ = os.Remove(playlistName) })

	now := time.Unix(0, 1683154504814142000)
	duration := 5.994

	for i := 0; i < 3; i++ {
		require.NoError(t, w.Append(now, duration, fmt.Sprintf("playlist_0000%d.ts", i)))
		now = now.Add(time.Millisecond * 5994)
	}

	require.NoError(t, w.Close())

	b, err := os.ReadFile(playlistName)
	require.NoError(t, err)

	expected := "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:04.814Z\n#EXTINF:5.994,\nplaylist_00000.ts\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:10.808Z\n#EXTINF:5.994,\nplaylist_00001.ts\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:16.802Z\n#EXTINF:5.994,\nplaylist_00002.ts\n#EXT-X-ENDLIST\n"
	require.Equal(t, expected, string(b))
}

func TestLivePlaylistWriter(t *testing.T) {
	playlistName := "playlist.m3u8"

	w, err := NewLivePlaylistWriter(playlistName, 6, 3)
	require.NoError(t, err)

	t.Cleanup(func() { _ = os.Remove(playlistName) })

	now := time.Unix(0, 1683154504814142000)
	duration := 5.994

	for i := 0; i < 2; i++ {
		require.NoError(t, w.Append(now, duration, fmt.Sprintf("playlist_0000%d.ts", i)))
		now = now.Add(time.Millisecond * 5994)
	}

	b, err := os.ReadFile(playlistName)
	require.NoError(t, err)

	expected := "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:04.814Z\n#EXTINF:5.994,\nplaylist_00000.ts\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:10.808Z\n#EXTINF:5.994,\nplaylist_00001.ts\n"
	require.Equal(t, expected, string(b))

	for i := 2; i < 4; i++ {
		require.NoError(t, w.Append(now, duration, fmt.Sprintf("playlist_0000%d.ts", i)))
		now = now.Add(time.Millisecond * 5994)
	}

	require.NoError(t, w.Close())

	b, err = os.ReadFile(playlistName)
	require.NoError(t, err)

	expected = "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:04.814Z\n#EXTINF:5.994,\nplaylist_00001.ts\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:16.802Z\n#EXTINF:5.994,\nplaylist_00002.ts\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:22.796Z\n#EXTINF:5.994,\nplaylist_00003.ts\n#EXT-X-ENDLIST\n"
	require.Equal(t, expected, string(b))
}

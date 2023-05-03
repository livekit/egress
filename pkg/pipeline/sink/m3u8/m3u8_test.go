package m3u8

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMediaPlaylist(t *testing.T) {
	playlistName := "playlist.m3u8"

	w, err := NewPlaylistWriter(playlistName, 6)
	require.NoError(t, err)

	t.Cleanup(func() { os.Remove(playlistName) })

	now := time.Unix(0, 1683154504814142000)
	duration := 5.99432

	for i := 0; i < 3; i++ {
		require.NoError(t, w.Append(now, duration, fmt.Sprintf("playlist_0000%d.ts", i)))
		now = now.Add(time.Microsecond * 5994320)
	}

	require.NoError(t, w.Close())

	b, err := os.ReadFile(playlistName)
	require.NoError(t, err)

	expected := "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-TARGETDURATION:6\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:04.814142Z\n#EXTINF:5.994\nplaylist_00000.ts\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:10.808462Z\n#EXTINF:5.994\nplaylist_00001.ts\n#EXT-X-PROGRAM-DATE-TIME:2023-05-03T22:55:16.802782Z\n#EXTINF:5.994\nplaylist_00002.ts\n#EXT-X-ENDLIST\n"
	require.Equal(t, expected, string(b))

	p, err := OpenPlaylist(playlistName)
	require.NoError(t, err)

	require.Equal(t, 4, p.Version)
	require.Equal(t, "EVENT", p.MediaType)
	require.Equal(t, 6, p.TargetDuration)
	require.Equal(t, 3, len(p.Segments))
	for _, segment := range p.Segments {
		require.NotZero(t, segment.StartTime)
		require.Equal(t, 5.994, segment.Duration)
		require.NotEmpty(t, segment.Filename)
	}
}

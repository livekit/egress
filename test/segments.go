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
	"encoding/binary"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/m3u8"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func (r *Runner) testSegments(t *testing.T) {
	if !r.should(runSegments) {
		return
	}

	t.Run("Segments", func(t *testing.T) {
		for _, test := range []*testCase{

			// ---- Room Composite -----

			{
				name:        "RoomComposite",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec:       types.MimeTypeOpus,
					videoCodec:       types.MimeTypeVP8,
					layout:           layoutSpeaker,
					multiParticipant: true,
				},
				encodingOptions: &livekit.EncodingOptions{
					AudioCodec:   livekit.AudioCodec_AAC,
					VideoCodec:   livekit.VideoCodec_H264_BASELINE,
					Width:        1920,
					Height:       1080,
					VideoBitrate: 4500,
				},
				segmentOptions: &segmentOptions{
					prefix:       "r_{room_name}_{time}",
					playlist:     "r_{room_name}_{time}.m3u8",
					livePlaylist: "r_live_{room_name}_{time}.m3u8",
					suffix:       livekit.SegmentedFileSuffix_INDEX,
				},
			},
			{
				name:        "RoomComposite/FMP4",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
					layout:     layoutSpeaker,
				},
				encodingOptions: &livekit.EncodingOptions{
					AudioCodec:   livekit.AudioCodec_AAC,
					VideoCodec:   livekit.VideoCodec_H264_BASELINE,
					Width:        1280,
					Height:       720,
					VideoBitrate: 3000,
				},
				segmentOptions: &segmentOptions{
					prefix:   "r_{room_name}_fmp4_{time}",
					playlist: "r_{room_name}_fmp4_{time}.m3u8",
					suffix:   livekit.SegmentedFileSuffix_INDEX,
					protocol: livekit.SegmentedFileProtocol_HLS_FMP4_PROTOCOL,
				},
			},
			{
				name:        "RoomComposite/AudioOnly",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				encodingOptions: &livekit.EncodingOptions{
					AudioCodec: livekit.AudioCodec_AAC,
				},
				segmentOptions: &segmentOptions{
					prefix:   "r_{room_name}_audio_{time}",
					playlist: "r_{room_name}_audio_{time}.m3u8",
					suffix:   livekit.SegmentedFileSuffix_TIMESTAMP,
				},
			},

			// ---------- Web ----------

			{
				name:        "Web",
				requestType: types.RequestTypeWeb,
				segmentOptions: &segmentOptions{
					prefix:   "web_{time}",
					playlist: "web_{time}.m3u8",
				},
			},

			// ------ Participant ------

			{
				name:        "ParticipantComposite/VP8",
				requestType: types.RequestTypeParticipant,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
					// videoDelay:     time.Second * 10,
					// videoUnpublish: time.Second * 20,
				},
				segmentOptions: &segmentOptions{
					prefix:   "participant_{publisher_identity}_vp8_{time}",
					playlist: "participant_{publisher_identity}_vp8_{time}.m3u8",
				},
			},
			{
				name:        "ParticipantComposite/AudioOnlyPublisher",
				requestType: types.RequestTypeParticipant,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
				},
				segmentOptions: &segmentOptions{
					prefix:          "participant_{publisher_identity}_audio_only_{time}",
					playlist:        "participant_{publisher_identity}_audio_only_{time}.m3u8",
					segmentDuration: 6,
				},
			},
			{
				name:        "ParticipantComposite/H264",
				requestType: types.RequestTypeParticipant,
				publishOptions: publishOptions{
					audioCodec:     types.MimeTypeOpus,
					audioDelay:     time.Second * 10,
					audioUnpublish: time.Second * 20,
					videoCodec:     types.MimeTypeH264,
				},
				segmentOptions: &segmentOptions{
					prefix:   "participant_{room_name}_h264_{time}",
					playlist: "participant_{room_name}_h264_{time}.m3u8",
				},
			},

			// ---- Track Composite ----

			{
				name:        "TrackComposite/H264",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeH264,
				},
				segmentOptions: &segmentOptions{
					prefix:       "tcs_{room_name}_h264_{time}",
					playlist:     "tcs_{room_name}_h264_{time}.m3u8",
					livePlaylist: "tcs_live_{room_name}_h264_{time}.m3u8",
				},
			},
			{
				name:        "TrackComposite/AudioOnly",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				segmentOptions: &segmentOptions{
					prefix:   "tcs_{room_name}_audio_{time}",
					playlist: "tcs_{room_name}_audio_{time}.m3u8",
				},
			},

			// --------- Web V2 --------

			{
				name:        "WebV2",
				requestType: types.RequestTypeWeb,
				segmentOptions: &segmentOptions{
					prefix:   "webv2_{time}",
					playlist: "webv2_{time}.m3u8",
				},
				v2OutputOptions: &v2OutputOptions{},
			},
		} {
			if !r.run(t, test) {
				return
			}
		}
	})
}

func (r *Runner) verifySegments(
	t *testing.T, tc *testCase, p *config.PipelineConfig,
	filenameSuffix livekit.SegmentedFileSuffix,
	res *livekit.EgressInfo, enableLivePlaylist bool,
) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// segments info
	require.Len(t, res.GetSegmentResults(), 1)
	segments := res.GetSegmentResults()[0]

	require.Greater(t, segments.Size, int64(0))
	require.Greater(t, segments.Duration, int64(0))

	r.verifySegmentOutput(t, tc, p, filenameSuffix, segmentPlaylist{
		name:         segments.PlaylistName,
		location:     segments.PlaylistLocation,
		segmentCount: int(segments.SegmentCount),
		playlistType: m3u8.PlaylistTypeEvent,
	}, res)
	if enableLivePlaylist {
		r.verifySegmentOutput(t, tc, p, filenameSuffix, segmentPlaylist{
			name:         segments.LivePlaylistName,
			location:     segments.LivePlaylistLocation,
			segmentCount: 5,
			playlistType: m3u8.PlaylistTypeLive,
		}, res)
	}
}

type segmentPlaylist struct {
	name         string
	location     string
	segmentCount int
	playlistType m3u8.PlaylistType
}

func (r *Runner) verifySegmentOutput(
	t *testing.T, tc *testCase, p *config.PipelineConfig,
	filenameSuffix livekit.SegmentedFileSuffix,
	pl segmentPlaylist,
	res *livekit.EgressInfo,
) {

	require.NotEmpty(t, pl.name)
	require.NotEmpty(t, pl.location)

	storedPlaylistPath := pl.name

	// download from cloud storage
	localPlaylistPath := path.Join(r.FilePrefix, path.Base(storedPlaylistPath))
	download(t, p.GetSegmentConfig().StorageConfig, localPlaylistPath, storedPlaylistPath, false)

	if pl.playlistType == m3u8.PlaylistTypeEvent {
		manifestLocal := path.Join(path.Dir(localPlaylistPath), res.EgressId+".json")
		manifestStorage := path.Join(path.Dir(storedPlaylistPath), res.EgressId+".json")
		manifest := loadManifest(t, p.GetSegmentConfig().StorageConfig, manifestLocal, manifestStorage)

		for _, playlist := range manifest.Playlists {
			require.Equal(t, pl.segmentCount, len(playlist.Segments))
			for _, segment := range playlist.Segments {
				localPath := path.Join(r.FilePrefix, path.Base(segment.Filename))
				download(t, p.GetSegmentConfig().StorageConfig, localPath, segment.Filename, false)
			}
		}
	}

	verifyPlaylistProgramDateTime(t, filenameSuffix, localPlaylistPath, pl.playlistType)

	// fmp4 segments are not playable without the init segment the playlist points at,
	// and it is not listed in the manifest, so fetch it before ffprobe reads the playlist
	if initSegment := readInitSegmentName(t, localPlaylistPath); initSegment != "" {
		require.Equal(t, types.OutputTypeM4S, p.GetSegmentConfig().SegmentOutputType)

		localInitPath := path.Join(path.Dir(localPlaylistPath), initSegment)
		download(t, p.GetSegmentConfig().StorageConfig, localInitPath,
			path.Join(path.Dir(storedPlaylistPath), initSegment), false)

		verifyFMP4Structure(t, localPlaylistPath, localInitPath, pl.playlistType)
	} else {
		require.Equal(t, types.OutputTypeTS, p.GetSegmentConfig().SegmentOutputType)
	}

	// verify
	info := verify(t, localPlaylistPath, p, res, types.EgressTypeSegments, r.sourceFramerate, pl.playlistType == m3u8.PlaylistTypeLive)
	// Live playlists are a rolling subset of segments; their partial
	// content isn't a fair representation of the full recording for
	// avsync verification. Structure is already validated above.
	if pl.playlistType != m3u8.PlaylistTypeLive {
		runContentCheck(t, tc, localPlaylistPath, info, "segments", "hls")
	}
}

// verifyFMP4Structure checks that the initialization segment carries the boxes that
// initialize playback and the media segments carry none of them: splitmuxsink builds a
// fresh muxer per segment, so each one is written with a copy that has to be removed.
func verifyFMP4Structure(t *testing.T, localPlaylistPath, localInitPath string, plType m3u8.PlaylistType) {
	require.Equal(t, []string{"ftyp", "moov"}, topLevelBoxes(t, localInitPath),
		"init segment should hold the initialization boxes and nothing else")

	pl, err := readPlaylist(localPlaylistPath)
	require.NoError(t, err)
	require.NotEmpty(t, pl.Segments)

	for _, segment := range pl.Segments {
		// only the event playlist downloads every segment it lists
		localPath := path.Join(path.Dir(localPlaylistPath), segment.Filename)
		if plType == m3u8.PlaylistTypeLive {
			if _, err = os.Stat(localPath); err != nil {
				continue
			}
		}

		boxes := topLevelBoxes(t, localPath)
		require.NotEmpty(t, boxes, segment.Filename)
		require.Equal(t, "styp", boxes[0], "%s should start with a segment type box", segment.Filename)
		require.NotContains(t, boxes, "ftyp", "%s should not repeat the init segment", segment.Filename)
		require.NotContains(t, boxes, "moov", "%s should not repeat the init segment", segment.Filename)

		// splitmuxsink ends the segment, and the muxer is configured not to split it further
		var moofs int
		for _, box := range boxes {
			if box == "moof" {
				moofs++
			}
		}
		require.Equal(t, 1, moofs, "%s should hold a single fragment", segment.Filename)
	}
}

// topLevelBoxes returns the type of each top level box of an ISO base media file.
func topLevelBoxes(t *testing.T, filename string) []string {
	b, err := os.ReadFile(filename)
	require.NoError(t, err)

	var boxes []string
	for i := 0; i+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[i : i+4]))
		boxes = append(boxes, string(b[i+4:i+8]))

		switch size {
		case 0:
			// box extends to the end of the file
			return boxes
		case 1:
			require.LessOrEqual(t, i+16, len(b), "truncated large box in %s", filename)
			size = int(binary.BigEndian.Uint64(b[i+8 : i+16]))
		}

		require.GreaterOrEqual(t, size, 8, "invalid box size in %s", filename)
		require.LessOrEqual(t, i+size, len(b), "box overruns %s", filename)
		i += size
	}

	return boxes
}

func readInitSegmentName(t *testing.T, localPlaylistPath string) string {
	p, err := readPlaylist(localPlaylistPath)
	require.NoError(t, err)
	return p.InitSegment
}

func verifyPlaylistProgramDateTime(t *testing.T, filenameSuffix livekit.SegmentedFileSuffix, localPlaylistPath string, plType m3u8.PlaylistType) {
	p, err := readPlaylist(localPlaylistPath)
	require.NoError(t, err)
	require.Equal(t, string(plType), p.MediaType)
	require.True(t, p.Closed)

	now := time.Now()

	for i, s := range p.Segments {
		const leeway = 50 * time.Millisecond

		// Make sure the program date time is current, ie not more than 2 min in the past
		require.InDelta(t, now.Unix(), s.ProgramDateTime.Unix(), 120)

		if filenameSuffix == livekit.SegmentedFileSuffix_TIMESTAMP {
			m := segmentTimeRegexp.FindStringSubmatch(s.Filename)
			require.Equal(t, 3, len(m))

			tm, err := time.Parse("20060102150405", m[1])
			require.NoError(t, err)

			ms, err := strconv.Atoi(m[2])
			require.NoError(t, err)

			tm = tm.Add(time.Duration(ms) * time.Millisecond)

			require.InDelta(t, s.ProgramDateTime.UnixNano(), tm.UnixNano(), float64(time.Millisecond))
		}

		if i < len(p.Segments)-2 {
			nextSegmentStartDate := p.Segments[i+1].ProgramDateTime

			dateDuration := nextSegmentStartDate.Sub(s.ProgramDateTime)
			require.InDelta(t, time.Duration(s.Duration*float64(time.Second)), dateDuration, float64(leeway))
		}
	}
}

type Playlist struct {
	Version        int
	MediaType      string
	TargetDuration int
	InitSegment    string
	Segments       []*Segment
	Closed         bool
}

type Segment struct {
	ProgramDateTime time.Time
	Duration        float64
	Filename        string
}

// tagValue returns the value of an m3u8 tag, or an empty string when it is absent.
func tagValue(lines []string, tag string) string {
	for _, line := range lines {
		if strings.HasPrefix(line, tag+":") {
			return strings.TrimPrefix(line, tag+":")
		}
	}
	return ""
}

func readPlaylist(filename string) (*Playlist, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(b), "\n")

	// the header varies with the version, so look tags up rather than count lines:
	// fmp4 playlists carry an EXT-X-MAP and drop the EXT-X-ALLOW-CACHE removed in v7
	version, _ := strconv.Atoi(tagValue(lines, "#EXT-X-VERSION"))
	mediaType := tagValue(lines, "#EXT-X-PLAYLIST-TYPE")
	targetDuration, _ := strconv.Atoi(tagValue(lines, "#EXT-X-TARGETDURATION"))
	initSegment := strings.Trim(strings.TrimPrefix(tagValue(lines, "#EXT-X-MAP"), "URI="), `"`)

	segmentLineStart := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:") {
			segmentLineStart = i
			break
		}
	}

	p := &Playlist{
		Version:        version,
		MediaType:      mediaType,
		TargetDuration: targetDuration,
		InitSegment:    initSegment,
		Segments:       make([]*Segment, 0),
	}

	for i := segmentLineStart; i < len(lines)-3; i += 3 {
		startTime, _ := time.Parse("2006-01-02T15:04:05.999Z07:00", strings.SplitN(lines[i], ":", 2)[1])
		durStr := strings.Split(lines[i+1], ":")[1]
		durStr = durStr[:len(durStr)-1] // remove trailing comma
		duration, _ := strconv.ParseFloat(durStr, 64)

		p.Segments = append(p.Segments, &Segment{
			ProgramDateTime: startTime,
			Duration:        duration,
			Filename:        lines[i+2],
		})
	}

	if lines[len(lines)-2] == "#EXT-X-ENDLIST" {
		p.Closed = true
	}

	return p, nil
}

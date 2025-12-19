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
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
					layout:     "speaker",
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
				contentCheck: r.fullContentCheck,
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
				contentCheck: r.audioOnlyContentCheck,
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
				contentCheck: r.fullContentCheck,
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
				contentCheck: r.fullContentCheck,
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
				contentCheck: r.audioOnlyContentCheck,
			},
		} {
			if !r.run(t, test, r.runSegmentsTest) {
				return
			}
		}
	})
}

func (r *Runner) runSegmentsTest(t *testing.T, test *testCase) {
	req := r.build(test)

	egressID := r.startEgress(t, req)

	time.Sleep(time.Second * 10)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// stop
	time.Sleep(time.Second * 15)
	res := r.stopEgress(t, egressID)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	require.Equal(t, !test.audioOnly, p.VideoEncoding)

	r.verifySegments(t, test, p, test.segmentOptions.suffix, res, test.livePlaylist != "")
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

	// verify
	info := verify(t, localPlaylistPath, p, res, types.EgressTypeSegments, r.Muting, r.sourceFramerate, pl.playlistType == m3u8.PlaylistTypeLive)
	if tc.contentCheck != nil && info != nil {
		tc.contentCheck(t, localPlaylistPath, info)
	}
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
	Segments       []*Segment
	Closed         bool
}

type Segment struct {
	ProgramDateTime time.Time
	Duration        float64
	Filename        string
}

func readPlaylist(filename string) (*Playlist, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var segmentLineStart = 5
	var i = 1

	lines := strings.Split(string(b), "\n")
	version, _ := strconv.Atoi(strings.Split(lines[i], ":")[1])
	i++
	var mediaType string
	if strings.Contains(string(b), "#EXT-X-PLAYLIST-TYPE") {
		mediaType = strings.Split(lines[i], ":")[1]
		segmentLineStart++
		i++
	}
	i++ // #EXT-X-ALLOW-CACHE:NO hardcoded
	targetDuration, _ := strconv.Atoi(strings.Split(lines[i], ":")[1])

	p := &Playlist{
		Version:        version,
		MediaType:      mediaType,
		TargetDuration: targetDuration,
		Segments:       make([]*Segment, 0),
	}

	for i = segmentLineStart; i < len(lines)-3; i += 3 {
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

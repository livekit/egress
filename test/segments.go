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
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/m3u8"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func (r *Runner) runSegmentsTest(t *testing.T, req *rpc.StartEgressRequest, test *testCase) {
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

	r.verifySegments(t, p, test.filenameSuffix, res, test.livePlaylist != "")
	if !test.audioOnly {
		require.Equal(t, test.expectVideoEncoding, p.VideoEncoding)
	}
}

func (r *Runner) verifySegments(t *testing.T, p *config.PipelineConfig, filenameSuffix livekit.SegmentedFileSuffix, res *livekit.EgressInfo, enableLivePlaylist bool) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// segments info
	require.Len(t, res.GetSegmentResults(), 1)
	segments := res.GetSegmentResults()[0]

	require.Greater(t, segments.Size, int64(0))
	require.Greater(t, segments.Duration, int64(0))

	r.verifySegmentOutput(t, p, filenameSuffix, segments.PlaylistName, segments.PlaylistLocation, int(segments.SegmentCount), res, m3u8.PlaylistTypeEvent)
	r.verifyManifest(t, p, segments.PlaylistName)
	if enableLivePlaylist {
		r.verifySegmentOutput(t, p, filenameSuffix, segments.LivePlaylistName, segments.LivePlaylistLocation, 5, res, m3u8.PlaylistTypeLive)
	}
}

func (r *Runner) verifyManifest(t *testing.T, p *config.PipelineConfig, plName string) {
	localPlaylistPath := fmt.Sprintf("%s/%s", r.FilePrefix, plName)

	if uploadConfig := p.GetSegmentConfig().UploadConfig; uploadConfig != nil {
		download(t, uploadConfig, localPlaylistPath+".json", plName+".json")
	}
}

func (r *Runner) verifySegmentOutput(t *testing.T, p *config.PipelineConfig, filenameSuffix livekit.SegmentedFileSuffix, plName string, plLocation string, segmentCount int, res *livekit.EgressInfo, plType m3u8.PlaylistType) {
	require.NotEmpty(t, plName)
	require.NotEmpty(t, plLocation)

	storedPlaylistPath := plName
	localPlaylistPath := plName

	// download from cloud storage
	if uploadConfig := p.GetSegmentConfig().UploadConfig; uploadConfig != nil {
		localPlaylistPath = fmt.Sprintf("%s/%s", r.FilePrefix, storedPlaylistPath)
		download(t, uploadConfig, localPlaylistPath, storedPlaylistPath)
		if plType == m3u8.PlaylistTypeEvent {
			// Only download segments once
			base := storedPlaylistPath[:len(storedPlaylistPath)-5]
			for i := 0; i < segmentCount; i++ {
				cloudPath := fmt.Sprintf("%s_%05d.ts", base, i)
				localPath := fmt.Sprintf("%s/%s", r.FilePrefix, cloudPath)
				download(t, uploadConfig, localPath, cloudPath)
			}
		}
	}

	verifyPlaylistProgramDateTime(t, filenameSuffix, localPlaylistPath, plType)

	// verify
	verify(t, localPlaylistPath, p, res, types.EgressTypeSegments, r.Muting, r.sourceFramerate, plType == m3u8.PlaylistTypeLive)
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

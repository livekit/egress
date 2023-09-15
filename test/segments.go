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

	if !test.audioOnly {
		require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)
	}
	r.verifySegments(t, p, test.filenameSuffix, res)
}

func (r *Runner) verifySegments(t *testing.T, p *config.PipelineConfig, filenameSuffix livekit.SegmentedFileSuffix, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// segments info
	require.Len(t, res.GetSegmentResults(), 1)
	segments := res.GetSegmentResults()[0]

	require.NotEmpty(t, segments.PlaylistName)
	require.NotEmpty(t, segments.PlaylistLocation)
	require.Greater(t, segments.Size, int64(0))
	require.Greater(t, segments.Duration, int64(0))

	storedPlaylistPath := segments.PlaylistName
	localPlaylistPath := segments.PlaylistName

	// download from cloud storage
	if uploadConfig := p.GetSegmentConfig().UploadConfig; uploadConfig != nil {
		base := storedPlaylistPath[:len(storedPlaylistPath)-5]
		localPlaylistPath = fmt.Sprintf("%s/%s", r.FilePrefix, storedPlaylistPath)
		download(t, uploadConfig, localPlaylistPath, storedPlaylistPath)
		download(t, uploadConfig, localPlaylistPath+".json", storedPlaylistPath+".json")
		for i := 0; i < int(segments.SegmentCount); i++ {
			cloudPath := fmt.Sprintf("%s_%05d.ts", base, i)
			localPath := fmt.Sprintf("%s/%s", r.FilePrefix, cloudPath)
			download(t, uploadConfig, localPath, cloudPath)
		}
	}

	verifyPlaylistProgramDateTime(t, filenameSuffix, localPlaylistPath)

	// verify
	verify(t, localPlaylistPath, p, res, types.EgressTypeSegments, r.Muting, r.sourceFramerate)
}

func verifyPlaylistProgramDateTime(t *testing.T, filenameSuffix livekit.SegmentedFileSuffix, localPlaylistPath string) {
	p, err := readPlaylist(localPlaylistPath)
	require.NoError(t, err)
	require.Equal(t, "EVENT", p.MediaType)
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

		if i < len(p.Segments)-1 {
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

	lines := strings.Split(string(b), "\n")
	version, _ := strconv.Atoi(strings.Split(lines[1], ":")[1])
	mediaType := strings.Split(lines[2], ":")[1]
	targetDuration, _ := strconv.Atoi(strings.Split(lines[5], ":")[1])

	p := &Playlist{
		Version:        version,
		MediaType:      mediaType,
		TargetDuration: targetDuration,
		Segments:       make([]*Segment, 0),
	}

	for i := 6; i < len(lines)-3; i += 3 {
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

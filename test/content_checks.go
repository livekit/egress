// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build integration

package test

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/stretchr/testify/require"
)

func (r *Runner) fullContentCheck(t *testing.T, file string, _ *FFProbeInfo) {
	if r.Muting {
		// TODO: support for content check on muted tracks to be added later
		return
	}

	// TODO: enable after fixing the issue with missing beeps
	// dur, err := parseFFProbeDuration(info.Format.Duration)
	//require.NoError(t, err)

	flashes, err := extractFlashTimestamps(file, r.FilePrefix)
	require.NoError(t, err)

	beeps, err := extractBeepTimestamps(file, testSampleBeepLevel, r.FilePrefix)
	require.NoError(t, err)

	silenceRanges, err := detectSilence(file, testSampleSilenceLevel, time.Millisecond*100)
	if len(silenceRanges) > 0 || err != nil {
		logger.Errorw("silence ranges not empty", err, "silenceRanges", silenceRanges)
	}

	// require.InDelta(t, len(flashes), len(beeps), 3)
	// require.InDelta(t, len(flashes), dur.Round(time.Second).Seconds(), 3)

	// avgFlashSpacing, err := averageSpacing(flashes)
	// require.NoError(t, err)
	// 200ms is still pretty generous, should be tighter
	// requireDurationInDelta(t, avgFlashSpacing, time.Second, time.Millisecond*200)

	// avgBeepSpacing, err := averageSpacing(beeps)
	// require.NoError(t, err)
	// requireDurationInDelta(t, avgBeepSpacing, time.Second, time.Millisecond*200)

	logger.Debugw("beeps", "beeps", beeps)
	logger.Debugw("flashes", "flashes", flashes)
}

func (r *Runner) videoOnlyContentCheck(t *testing.T, file string, info *FFProbeInfo) {
	if r.Muting {
		// TODO: support for content check on muted tracks to be added later
		return
	}

	flashes, err := extractFlashTimestamps(file, r.FilePrefix)
	require.NoError(t, err)

	dur, err := parseFFProbeDuration(info.Format.Duration)
	require.NoError(t, err)

	require.InDelta(t, len(flashes), dur.Round(time.Second).Seconds(), 3)
	avgFlashSpacing, err := averageSpacing(flashes)
	require.NoError(t, err)
	// 200ms is still pretty generous, should be tighter
	requireDurationInDelta(t, avgFlashSpacing, time.Second, time.Millisecond*200)
}

func (r *Runner) audioOnlyContentCheck(t *testing.T, file string, _ *FFProbeInfo) {
	if r.Muting {
		// TODO: support for content check on muted tracks to be added later
		return
	}

	//TODO: enable after fixing the issue with missing beeps
	//dur, err := parseFFProbeDuration(info.Format.Duration)
	//require.NoError(t, err)

	beeps, err := extractBeepTimestamps(file, testSampleBeepLevel, r.FilePrefix)
	require.NoError(t, err)

	silenceRanges, err := detectSilence(file, testSampleSilenceLevel, time.Millisecond*100)
	if len(silenceRanges) > 0 || err != nil {
		logger.Errorw("silence ranges not empty", err, "silenceRanges", silenceRanges)
	}

	// require.NoError(t, err)
	// // sometimes the silence range is at the end of the file, ignore it
	// require.True(t, len(silenceRanges) == 0 || silenceRanges[0].start > dur-time.Second*2,
	// 	fmt.Sprintf("unexpected silence ranges: %v", silenceRanges))

	// require.InDelta(t, len(beeps), dur.Round(time.Second).Seconds(), 3)

	// avgBeepSpacing, err := averageSpacing(beeps)
	// require.NoError(t, err)
	// requireDurationInDelta(t, avgBeepSpacing, time.Second, time.Millisecond*200)
	logger.Debugw("beeps", "beeps", beeps)
}

func (r *Runner) fullContentCheckWithVideoUnpublishAt10AndRepublishAt20(t *testing.T, file string, info *FFProbeInfo) {
	if r.Muting {
		// TODO: support for content check on muted to be added later
		return
	}

	flashes, err := extractFlashTimestamps(file, r.FilePrefix)
	require.NoError(t, err)

	dur, err := parseFFProbeDuration(info.Format.Duration)
	require.NoError(t, err)

	gapLength := time.Second * 10
	require.InDelta(
		t,
		float64(len(flashes))+gapLength.Seconds(),
		dur.Round(time.Second).Seconds(),
		5.0,
		"flashes+gap ~= duration (±3s)",
	)

	gapsFound := 0
	for i := 1; i < len(flashes); i++ {
		if flashes[i]-flashes[i-1] > gapLength-time.Millisecond*500 {
			gapsFound++
			requireDurationInDelta(t, flashes[i], time.Second*20, time.Second*2)
		} else {
			// all other flashes should be within 1 second of the previous flash
			requireDurationInDelta(t, flashes[i], flashes[i-1], time.Second+time.Millisecond*200)
		}
	}
	require.Equal(t, gapsFound, 1)

	r.audioOnlyContentCheck(t, file, info)

}

func (r *Runner) streamKeyframeContentCheck(expectedInterval float64) func(t *testing.T, target string, _ *FFProbeInfo) {
	return func(t *testing.T, target string, _ *FFProbeInfo) {
		requireKeyframeInterval(t, target, expectedInterval)
	}
}

// ensures input is read long enough to get sufficient keyframes for spacing check
func requireKeyframeInterval(t *testing.T, input string, expectedInterval float64) {
	t.Helper()
	if expectedInterval <= 0 {
		return
	}

	timestamps, err := ffprobeKeyframeTimestamps(input, expectedInterval)

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(timestamps), 2, "ffprobe returned less than two keyframes for %s", input)

	tolerance := 0.020 // 20ms
	prev := timestamps[0]
	found := false
	for _, ts := range timestamps[1:] {
		if ts <= prev {
			prev = ts
			continue
		}
		found = true
		require.InDelta(t, expectedInterval, ts-prev, tolerance, "keyframe spacing mismatch for %s", input)
		prev = ts
	}
	require.True(t, found, "no increasing keyframe timestamps found for %s", input)
}

func ffprobeKeyframeTimestamps(input string, expectedInterval float64) ([]float64, error) {
	timestamps := []float64{}
	var err error

	// ensure at least 3 keyframes are read
	readSeconds := expectedInterval*4 + 1

	args := []string{
		"-v", "error",
		"-fflags", "nobuffer",
		"-rw_timeout", "5000000",
		"-select_streams", "v:0",
		"-show_packets",
		"-show_entries", "packet=pts_time,dts_time,flags,stream_index,size,pos",
		"-of", "csv=p=0",
		input,
	}

	timeout := time.Duration(readSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffprobe: %w", err)
	}
	defer cmd.Wait()

	csvReader := csv.NewReader(stdout)

	for {
		record, e := csvReader.Read()
		if e != nil {
			if ctx.Err() != nil || e == io.EOF {
				// ignore context && EOF errors, we could be canceling the context after readSeconds
			} else {
				err = fmt.Errorf("read csv: %w", e)
			}
			break
		}

		if len(record) != 6 {
			err = fmt.Errorf("unexpected record length: %d", len(record))
			break
		}

		pts, e := strconv.ParseFloat(record[1], 64)
		if e != nil {
			err = fmt.Errorf("parse pts: %w", e)
			break
		}
		if strings.Contains(record[5], "K") {
			timestamps = append(timestamps, pts)
		}
	}

	if err != nil {
		return nil, err
	}
	return timestamps, nil
}

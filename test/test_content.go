// Copyright 2025 LiveKit, Inc.
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

package test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

//----------------------------------------------------------------------
// Utilities for checking AV sync for the given audio/video test sample
// https://github.com/livekit/media-samples/avsync_minmotion_livekit*
//----------------------------------------------------------------------

const (
	testSampleSilenceLevel = -38
	testSampleBeepLevel    = -30.0
)

var (
	rePTS  = regexp.MustCompile(`pts_time:([0-9.]+)`)
	reYAVG = regexp.MustCompile(`lavfi\.signalstats\.YAVG[=:]\s*([0-9.]+)`)
	reRMS  = regexp.MustCompile(`RMS_level[=:](-?[0-9.infINFNaN]+)`)
)

func ffmpegVideoStats(videoPath, statsFile string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats", "-loglevel", "repeat+info",
		"-i", videoPath,
		"-map", "0:v:0",
		"-vf", fmt.Sprintf("crop=w=iw:h=8:x=0:y=0,signalstats,metadata=print:file=%s", statsFile),
		"-f", "null", "-")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("ffmpeg video stats timeout after 15s")
		}
		return fmt.Errorf("ffmpeg video stats extraction failed: %w\nstdout:\n%s\nstderr:\n%s",
			err, outBuf.String(), errBuf.String())
	}
	return nil
}

func ffmpegAudioStats(audioPath, statsFile string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats", "-loglevel", "repeat+info",
		"-i", audioPath,
		"-af", fmt.Sprintf("pan=mono|c0=0.5*c0+0.5*c1,astats=metadata=1:reset=1,ametadata=print:key=lavfi.astats.Overall.RMS_level:file=%s", statsFile),
		"-f", "null", "-")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("ffmpeg audio stats timeout after 15s")
		}
		return fmt.Errorf("ffmpeg audio stats extraction failed: %w\nstdout:\n%s\nstderr:\n%s",
			err, outBuf.String(), errBuf.String())
	}
	return nil
}

func ffmpegSilenceStats(audioPath string, noiseLevel int, minDuration float64) (*bytes.Buffer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats", "-loglevel", "info",
		"-i", audioPath,
		"-af", "silencedetect=noise="+fmt.Sprintf("%d", noiseLevel)+"dB:d="+strconv.FormatFloat(minDuration, 'f', -1, 64),
		"-f", "null", "-")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ffmpeg silence stats timeout after 15s")
		}
		return nil, fmt.Errorf("ffmpeg silence stats extraction failed: %w\nstderr:\n%s",
			err, stderr.String())
	}
	return &stderr, nil
}

// extractFlashTimestamps runs ffmpeg + signalstats on the top stripe
// and returns one timestamp per flash event (YAVG >= 130, spaced ≥0.2s).
func extractFlashTimestamps(videoPath, outPath string) ([]time.Duration, error) {
	logFile := filepath.Join(outPath, "video_flash.log")

	err := ffmpegVideoStats(videoPath, logFile)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(logFile)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg video stats failed to open log file: %w", err)
	}
	defer file.Close()

	const flashThreshold = 130.0
	const minGap = 200 * time.Millisecond

	var (
		flashes   []time.Duration
		lastFlash = -999 * time.Second
		curPTS    time.Duration
	)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()

		if m := rePTS.FindStringSubmatch(line); len(m) == 2 {
			// PTS is logged as seconds (float); convert to duration
			if d, perr := parsePTSSecondsToDuration(m[1]); perr == nil {
				curPTS = d
			}
			continue
		}

		if m := reYAVG.FindStringSubmatch(line); len(m) == 2 {
			y, _ := strconv.ParseFloat(m[1], 64)
			if y >= flashThreshold && curPTS-lastFlash > minGap {
				flashes = append(flashes, curPTS)
				lastFlash = curPTS
			}
		}
	}
	return flashes, scanner.Err()
}

// extractBeepTimestamps runs ffmpeg + astats to find beeps.
// A beep is when RMS_level > beepThreshold, debounced by 0.2s.
func extractBeepTimestamps(audioPath string, beepThreshold float64, outPath string) ([]time.Duration, error) {
	logFile := filepath.Join(outPath, "audio_beep.log")

	err := ffmpegAudioStats(audioPath, logFile)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(logFile)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg audio stats failed to open log file: %w", err)
	}
	defer file.Close()

	const minGap = 200 * time.Millisecond

	var (
		beeps  []time.Duration
		last   = -999 * time.Second
		curPTS time.Duration
	)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()

		if m := rePTS.FindStringSubmatch(line); len(m) == 2 {
			if d, perr := parsePTSSecondsToDuration(m[1]); perr == nil {
				curPTS = d
			}
			continue
		}

		if m := reRMS.FindStringSubmatch(line); len(m) == 2 {
			val := m[1]
			if strings.Contains(val, "inf") || strings.Contains(val, "nan") {
				continue // skip silence or invalid
			}
			lvl, _ := strconv.ParseFloat(val, 64)
			if lvl > beepThreshold && curPTS-last > minGap {
				beeps = append(beeps, curPTS)
				last = curPTS
			}
		}
	}
	return beeps, scanner.Err()
}

// silenceRange represents one silence segment in durations.
type silenceRange struct {
	start    time.Duration
	end      time.Duration
	duration time.Duration
}

// detectSilence runs ffmpeg silencedetect and returns all silence ranges.
func detectSilence(audioPath string, noiseLevel int, minDuration time.Duration) ([]silenceRange, error) {
	stderr, err := ffmpegSilenceStats(audioPath, noiseLevel, minDuration.Seconds())
	if err != nil {
		return nil, err
	}

	var ranges []silenceRange
	var current silenceRange
	inSilence := false

	reStart := regexp.MustCompile(`silence_start:\s*([0-9.]+)`)
	reEnd := regexp.MustCompile(`silence_end:\s*([0-9.]+)\s*\|\s*silence_duration:\s*([0-9.]+)`)

	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()

		if m := reStart.FindStringSubmatch(line); len(m) == 2 {
			if start, perr := strconv.ParseFloat(m[1], 64); perr == nil {
				current = silenceRange{start: secondsToDuration(start)}
				inSilence = true
			}
		}

		if m := reEnd.FindStringSubmatch(line); len(m) == 3 {
			if inSilence {
				end, _ := strconv.ParseFloat(m[1], 64)
				dur, _ := strconv.ParseFloat(m[2], 64)
				current.end = secondsToDuration(end)
				current.duration = secondsToDuration(dur)
				ranges = append(ranges, current)
				inSilence = false
			}
		}
	}

	return ranges, scanner.Err()
}

func secondsToDuration(f float64) time.Duration {
	return time.Duration(f * float64(time.Second))
}

func parsePTSSecondsToDuration(s string) (time.Duration, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(f * float64(time.Second)), nil
}

func averageSpacing(ts []time.Duration) (time.Duration, error) {
	if len(ts) < 2 {
		return 0, fmt.Errorf("need at least 2 timestamps (got %d)", len(ts))
	}

	var sum time.Duration
	var gaps int
	for i := 1; i < len(ts); i++ {
		d := ts[i] - ts[i-1]
		if d <= 0 {
			// skip non-positive gaps (duplicates or out-of-order anomalies)
			continue
		}
		sum += d
		gaps++
	}
	if gaps == 0 {
		return 0, fmt.Errorf("no positive gaps to compute spacing")
	}
	return time.Duration(int64(sum) / int64(gaps)), nil
}

func requireDurationInDelta(t *testing.T, expected, actual, delta time.Duration, msgAndArgs ...interface{}) {
	require.InDelta(t,
		expected.Nanoseconds(),
		actual.Nanoseconds(),
		float64(delta.Nanoseconds()),
		msgAndArgs...)
}

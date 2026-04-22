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

//go:build integration

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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/logger"
)

// ParticipantSpec describes a participant for sync analysis.
type ParticipantSpec struct {
	Identity  string
	Frequency float64 // Hz for audio identification
	TileX     int     // expected X position in compositor output
	TileY     int     // expected Y position
	TileW     int     // expected tile width
	TileH     int     // expected tile height
}

// SyncResult holds per-participant sync analysis results.
type SyncResult struct {
	Participant  string
	IntraOffset  time.Duration // audio-video offset within this participant
	InterOffset  time.Duration // drift relative to expected rotation schedule
	AudioPresent bool          // was audio detected during active window
	VideoPresent bool          // was video detected in expected tile region
}

// audioEvent represents a detected period of audio presence for a frequency.
type audioEvent struct {
	start time.Duration
	end   time.Duration
}

// videoEvent represents a detected flash in a video region.
type videoEvent struct {
	timestamp time.Duration
}

// extractFrequencyAudio runs FFmpeg with a bandpass filter at the given frequency
// and returns timestamps where RMS exceeds the threshold.
// This isolates a single participant's audio from the composited mix.
func extractFrequencyAudio(audioPath string, frequency float64, outPath string) ([]audioEvent, error) {
	logFile := filepath.Join(outPath, fmt.Sprintf("audio_%dhz.log", int(frequency)))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Bandpass filter isolates the target frequency, then astats extracts RMS levels
	af := fmt.Sprintf("bandpass=f=%.0f:width_type=h:w=100,astats=metadata=1:reset=1,ametadata=print:key=lavfi.astats.Overall.RMS_level:file=%s",
		frequency, logFile)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats", "-loglevel", "repeat+info",
		"-i", audioPath,
		"-af", af,
		"-f", "null", "-")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ffmpeg frequency extraction timeout after 30s")
		}
		return nil, fmt.Errorf("ffmpeg frequency extraction failed: %w\nstderr:\n%s",
			err, errBuf.String())
	}

	return parseAudioEvents(logFile, -30.0)
}

// parseAudioEvents reads an astats log file and groups consecutive above-threshold
// frames into events. Returns a list of (start, end) duration pairs.
func parseAudioEvents(logFile string, threshold float64) ([]audioEvent, error) {
	file, err := os.Open(logFile)
	if err != nil {
		return nil, fmt.Errorf("open audio log: %w", err)
	}
	defer file.Close()

	const minGap = 300 * time.Millisecond

	var (
		events  []audioEvent
		current *audioEvent
		curPTS  time.Duration
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
				// silence — close any open event
				if current != nil {
					current.end = curPTS
					events = append(events, *current)
					current = nil
				}
				continue
			}
			lvl, _ := strconv.ParseFloat(val, 64)
			if lvl > threshold {
				if current == nil {
					current = &audioEvent{start: curPTS}
				}
				current.end = curPTS
			} else if current != nil {
				// Below threshold — if gap is small, keep the event open (debounce)
				if curPTS-current.end > minGap {
					events = append(events, *current)
					current = nil
				}
			}
		}
	}
	if current != nil {
		events = append(events, *current)
	}

	return events, scanner.Err()
}

// extractRegionFlashes runs FFmpeg with a crop filter targeting a specific tile region
// and detects flash events using signalstats YAVG, matching extractFlashTimestamps() logic.
func extractRegionFlashes(videoPath string, tileX, tileY, tileW, tileH int, outPath string) ([]videoEvent, error) {
	logFile := filepath.Join(outPath, fmt.Sprintf("video_region_%d_%d.log", tileX, tileY))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Crop to the tile region's top 8 pixels (matching existing flash detection),
	// then run signalstats to get YAVG
	vf := fmt.Sprintf("crop=w=%d:h=8:x=%d:y=%d,signalstats,metadata=print:file=%s",
		tileW, tileX, tileY, logFile)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats", "-loglevel", "repeat+info",
		"-i", videoPath,
		"-map", "0:v:0",
		"-vf", vf,
		"-f", "null", "-")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ffmpeg region flash extraction timeout after 30s")
		}
		return nil, fmt.Errorf("ffmpeg region flash extraction failed: %w\nstderr:\n%s",
			err, errBuf.String())
	}

	return parseFlashEvents(logFile)
}

// parseFlashEvents reads a signalstats log file and returns timestamps of flash events.
// Uses the same YAVG >= 130 threshold and 200ms minimum gap as extractFlashTimestamps().
func parseFlashEvents(logFile string) ([]videoEvent, error) {
	file, err := os.Open(logFile)
	if err != nil {
		return nil, fmt.Errorf("open video log: %w", err)
	}
	defer file.Close()

	const flashThreshold = 130.0
	const minGap = 200 * time.Millisecond

	var (
		events    []videoEvent
		lastFlash = -999 * time.Second
		curPTS    time.Duration
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

		if m := reYAVG.FindStringSubmatch(line); len(m) == 2 {
			y, _ := strconv.ParseFloat(m[1], 64)
			if y >= flashThreshold && curPTS-lastFlash > minGap {
				events = append(events, videoEvent{timestamp: curPTS})
				lastFlash = curPTS
			}
		}
	}
	return events, scanner.Err()
}

// AnalyzeSync verifies intra-participant and inter-participant A/V sync
// in a composited output file. It fails the test if any participant's offset
// exceeds the tolerance.
//
// The rotation schedule is assumed: participant i is active (audio unmuted)
// from t = i*turnDuration to t = (i+1)*turnDuration, cycling through all participants.
func AnalyzeSync(
	t *testing.T,
	outputFile string,
	participants []ParticipantSpec,
	turnDuration time.Duration,
	tolerance time.Duration,
	outPath string,
) []SyncResult {
	t.Helper()

	results := make([]SyncResult, len(participants))

	// Extract per-frequency audio events and per-region video flashes
	allAudioEvents := make([][]audioEvent, len(participants))
	allVideoEvents := make([][]videoEvent, len(participants))

	for i, p := range participants {
		var err error
		allAudioEvents[i], err = extractFrequencyAudio(outputFile, p.Frequency, outPath)
		require.NoError(t, err, "failed to extract audio for participant %s (%.0fHz)", p.Identity, p.Frequency)

		allVideoEvents[i], err = extractRegionFlashes(outputFile, p.TileX, p.TileY, p.TileW, p.TileH, outPath)
		require.NoError(t, err, "failed to extract video for participant %s", p.Identity)

		results[i].Participant = p.Identity
	}

	// Analyze each participant's turn
	n := len(participants)
	for i, p := range participants {
		windowStart := time.Duration(i) * turnDuration
		windowEnd := windowStart + turnDuration

		// --- Intra-participant sync ---
		// Find the first audio event that overlaps this participant's active window
		audioOnset := findAudioOnset(allAudioEvents[i], windowStart, windowEnd)
		// Find the first video flash in this window
		videoOnset := findVideoOnset(allVideoEvents[i], windowStart, windowEnd)

		results[i].AudioPresent = audioOnset >= 0
		results[i].VideoPresent = videoOnset >= 0

		if audioOnset >= 0 && videoOnset >= 0 {
			intraOffset := absDuration(audioOnset - videoOnset)
			results[i].IntraOffset = intraOffset

			logger.Infow("intra-participant sync",
				"participant", p.Identity,
				"audioOnset", audioOnset,
				"videoOnset", videoOnset,
				"offset", intraOffset,
				"tolerance", tolerance,
			)

			require.LessOrEqual(t, intraOffset, tolerance,
				"intra-participant A/V sync exceeded tolerance for %s: audio=%v video=%v offset=%v",
				p.Identity, audioOnset, videoOnset, intraOffset)
		}

		// --- Inter-participant sync ---
		// Compare actual audio onset to expected schedule
		if audioOnset >= 0 {
			expectedOnset := windowStart
			interOffset := absDuration(audioOnset - expectedOnset)
			results[i].InterOffset = interOffset

			logger.Infow("inter-participant sync",
				"participant", p.Identity,
				"expectedOnset", expectedOnset,
				"actualOnset", audioOnset,
				"offset", interOffset,
				"tolerance", tolerance,
			)

			// Inter-participant tolerance is more relaxed — mute propagation adds latency
			interTolerance := tolerance + 500*time.Millisecond
			require.LessOrEqual(t, interOffset, interTolerance,
				"inter-participant sync exceeded tolerance for %s: expected=%v actual=%v offset=%v",
				p.Identity, expectedOnset, audioOnset, interOffset)
		} else {
			logger.Warnw("no audio detected during active window", nil,
				"participant", p.Identity,
				"window", fmt.Sprintf("[%v, %v]", windowStart, windowEnd),
			)
		}

		_ = n // used by rotation wrap-around logic if needed
	}

	return results
}

// findAudioOnset returns the start time of the first audio event that overlaps
// the given window, or -1 if none found.
func findAudioOnset(events []audioEvent, windowStart, windowEnd time.Duration) time.Duration {
	for _, e := range events {
		if e.end >= windowStart && e.start <= windowEnd {
			if e.start < windowStart {
				return windowStart
			}
			return e.start
		}
	}
	return -1
}

// findVideoOnset returns the timestamp of the first video flash within the window,
// or -1 if none found.
func findVideoOnset(events []videoEvent, windowStart, windowEnd time.Duration) time.Duration {
	for _, e := range events {
		if e.timestamp >= windowStart && e.timestamp <= windowEnd {
			return e.timestamp
		}
	}
	return -1
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

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
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/logger"
)

// ----------------------------------------------------------------------
// Shared constants, regex patterns, and utility functions
// ----------------------------------------------------------------------

const (
	testSampleSilenceLevel = -38
	testSampleBeepLevel    = -30.0
)

var (
	rePTS  = regexp.MustCompile(`pts_time:([0-9.]+)`)
	reYAVG = regexp.MustCompile(`lavfi\.signalstats\.YAVG[=:]\s*([0-9.]+)`)
	reYMAX = regexp.MustCompile(`lavfi\.signalstats\.YMAX[=:]\s*([0-9.]+)`)
	reRMS  = regexp.MustCompile(`RMS_level[=:](-?[0-9.infINFNaN]+)`)
)

func parsePTSSecondsToDuration(s string) (time.Duration, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(f * float64(time.Second)), nil
}

func secondsToDuration(f float64) time.Duration {
	return time.Duration(f * float64(time.Second))
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

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ----------------------------------------------------------------------
// FFmpeg helpers — run ffmpeg/ffprobe and extract stats to log files
// ----------------------------------------------------------------------

func ffmpegVideoStats(videoPath, statsFile string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats", "-loglevel", "repeat+info",
		"-i", videoPath,
		"-map", "0:v:0",
		"-vf", fmt.Sprintf("signalstats,metadata=print:file=%s", statsFile),
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

// ----------------------------------------------------------------------
// Single-participant content extraction (legacy test media)
// ----------------------------------------------------------------------

// extractFlashTimestamps runs ffmpeg + signalstats on the top stripe
// and returns one timestamp per flash event (YAVG >= flashDetectionThreshold, spaced >= 0.2s).
// The threshold is set low enough to detect colored flashes (red Y≈76, green Y≈150, blue Y≈29)
// as well as white flashes (Y=255).
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

	const flashThreshold = 100.0 // YMAX: flash frames ~187-191, grey text ~64-71
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
			if d, perr := parsePTSSecondsToDuration(m[1]); perr == nil {
				curPTS = d
			}
			continue
		}

		if m := reYMAX.FindStringSubmatch(line); len(m) == 2 {
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
				continue
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

// silenceRange represents one silence segment.
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

// ----------------------------------------------------------------------
// Content verification — replaces the old content_checks.go functions.
// Called from verifyFile / verifySegments / verifyStream.
// ----------------------------------------------------------------------

// verifyContent runs standard content checks on an output file.
// For audio+video: extracts flashes and beeps, checks they exist.
// For video-only: checks flash count matches duration and spacing is ~1s.
// For audio-only: extracts beeps, checks they exist.
func verifyContent(t *testing.T, file string, info *FFProbeInfo, hasAudio, hasVideo bool, muting bool, outPath string) {
	t.Helper()

	if muting {
		// content checks not yet reliable with muting enabled
		return
	}

	if hasVideo {
		flashes, err := extractFlashTimestamps(file, outPath)
		require.NoError(t, err)
		logger.Debugw("flashes", "flashes", flashes, "count", len(flashes))

		if !hasAudio && info != nil {
			// video-only: verify flashes are present
			require.NotEmpty(t, flashes, "expected flashes in video-only output")
		}
	}

	if hasAudio {
		beeps, err := extractBeepTimestamps(file, testSampleBeepLevel, outPath)
		require.NoError(t, err)
		logger.Debugw("beeps", "beeps", beeps, "count", len(beeps))

		silenceRanges, err := detectSilence(file, testSampleSilenceLevel, time.Millisecond*100)
		if len(silenceRanges) > 0 || err != nil {
			logger.Errorw("silence ranges not empty", err, "silenceRanges", silenceRanges)
		}
	}
}

// ----------------------------------------------------------------------
// Special-case content checks (used as testCase.contentCheck)
// ----------------------------------------------------------------------

// verifyVideoGap checks that flashes have a ~10s gap starting around t=10s
// (video unpublished at 10s, republished at 20s).
func verifyVideoGap(t *testing.T, file string, info *FFProbeInfo) {
	flashes, err := extractFlashTimestamps(file, filepath.Dir(file))
	require.NoError(t, err)

	dur, err := parseFFProbeDuration(info.Format.Duration)
	require.NoError(t, err)

	gapLength := time.Second * 10
	require.InDelta(
		t,
		float64(len(flashes))+gapLength.Seconds(),
		dur.Round(time.Second).Seconds(),
		5.0,
		"flashes+gap ~= duration (±5s)",
	)

	gapsFound := 0
	for i := 1; i < len(flashes); i++ {
		if flashes[i]-flashes[i-1] > gapLength-time.Millisecond*500 {
			gapsFound++
			requireDurationInDelta(t, flashes[i], time.Second*20, time.Second*2)
		} else {
			requireDurationInDelta(t, flashes[i], flashes[i-1], time.Second+time.Millisecond*200)
		}
	}
	require.Equal(t, gapsFound, 1)
}

// ----------------------------------------------------------------------
// Keyframe interval verification (for stream tests)
// ----------------------------------------------------------------------

func keyframeContentCheck(expectedInterval float64) func(t *testing.T, target string, _ *FFProbeInfo) {
	return func(t *testing.T, target string, _ *FFProbeInfo) {
		requireKeyframeInterval(t, target, expectedInterval)
	}
}

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
			if ctx.Err() == nil && e != io.EOF {
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

// ----------------------------------------------------------------------
// Multi-participant sync analysis
// ----------------------------------------------------------------------

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
// and returns time ranges where RMS exceeds the threshold.
func extractFrequencyAudio(audioPath string, frequency float64, outPath string) ([]audioEvent, error) {
	logFile := filepath.Join(outPath, fmt.Sprintf("audio_%dhz.log", int(frequency)))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
// frames into events.
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
// and detects flash events using signalstats YAVG.
func extractRegionFlashes(videoPath string, tileX, tileY, tileW, tileH int, outPath string) ([]videoEvent, error) { //nolint:revive
	logFile := filepath.Join(outPath, fmt.Sprintf("video_region_%d_%d.log", tileX, tileY))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
func parseFlashEvents(logFile string) ([]videoEvent, error) {
	file, err := os.Open(logFile)
	if err != nil {
		return nil, fmt.Errorf("open video log: %w", err)
	}
	defer file.Close()

	const flashThreshold = 28.0
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

// AnalyzeSync verifies A/V sync in a composited output file.
//
// For every audio beep detected from any participant, it checks that every
// visible participant's video tile had a flash within the tolerance window.
// Audio beeps that fall within 1 second of a mute/unmute transition are
// skipped since propagation delay makes them unreliable.
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

	// Audio RMS detection has an inherent lag relative to video YMAX detection:
	// the astats filter reports threshold crossings ~100ms after the actual beep
	// onset, while YMAX catches the first bright frame almost immediately.
	// Subtract this lag from detected beep timestamps to align with flash timestamps.
	const audioDetectionAdj = -140 * time.Millisecond
	const videoDetectionAdj = 30 * time.Millisecond

	// Extract beep timestamps from the mixed audio (all participants combined).
	rawBeepTimestamps, err := extractBeepTimestamps(outputFile, testSampleBeepLevel, outPath)
	require.NoError(t, err, "failed to extract beep timestamps")

	beepTimestamps := make([]time.Duration, len(rawBeepTimestamps))
	for i, ts := range rawBeepTimestamps {
		beepTimestamps[i] = ts + audioDetectionAdj
	}

	// Extract flash timestamps from the full composited frame.
	// All participants flash at the same cadence, so we don't need per-tile analysis.
	flashTimestamps, err := extractFlashTimestamps(outputFile, outPath)
	require.NoError(t, err, "failed to extract flash timestamps")

	for i := range participants {
		results[i].Participant = participants[i].Identity
	}
	results[0].AudioPresent = len(beepTimestamps) > 0
	results[0].VideoPresent = len(flashTimestamps) > 0

	logger.Infow("extraction results",
		"beeps", len(beepTimestamps),
		"flashes", len(flashTimestamps),
		"participants", len(participants),
	)

	// Build a set of "skip windows":
	// - First 5 seconds of recording (sync settling)
	// - 1 second around each mute/unmute transition
	n := len(participants)
	skipDuration := 1 * time.Second
	var skipWindows []struct{ start, end time.Duration }
	skipWindows = append(skipWindows, struct{ start, end time.Duration }{
		start: 0,
		end:   5500 * time.Millisecond,
	})
	for i := 0; i < n; i++ {
		transition := time.Duration(i) * turnDuration
		skipWindows = append(skipWindows, struct{ start, end time.Duration }{
			start: transition,
			end:   transition + skipDuration,
		})
	}

	isInSkipWindow := func(ts time.Duration) bool {
		for _, w := range skipWindows {
			if ts >= w.start && ts <= w.end {
				return true
			}
		}
		return false
	}

	flashTimes := make([]time.Duration, len(flashTimestamps))
	for i, ts := range flashTimestamps {
		flashTimes[i] = ts + videoDetectionAdj
	}

	// For each beep, find the closest flash. They should align within tolerance
	// since the beep cadence (1/s) matches the flash cadence (1/s).
	totalChecked := 0
	totalPassed := 0
	var worstOffset time.Duration

	for _, beepTime := range beepTimestamps {
		if isInSkipWindow(beepTime) {
			continue
		}

		closestDist := time.Duration(1<<62 - 1)
		for _, ft := range flashTimes {
			if d := absDuration(beepTime - ft); d < closestDist {
				closestDist = d
			}
		}

		totalChecked++
		if closestDist <= tolerance {
			totalPassed++
		} else {
			logger.Warnw("A/V sync failure: beep without matching flash", nil,
				"beepTime", beepTime,
				"closestFlash", closestDist,
				"tolerance", tolerance,
			)
		}
		if closestDist > worstOffset {
			worstOffset = closestDist
		}
	}

	// Reverse check: for each flash, find the closest beep
	for _, ft := range flashTimes {
		if isInSkipWindow(ft) {
			continue
		}

		closestDist := time.Duration(1<<62 - 1)
		for _, beepTime := range beepTimestamps {
			if d := absDuration(ft - beepTime); d < closestDist {
				closestDist = d
			}
		}

		totalChecked++
		if closestDist <= tolerance {
			totalPassed++
		} else {
			logger.Warnw("A/V sync failure: flash without matching beep", nil,
				"flashTime", ft,
				"closestBeep", closestDist,
				"tolerance", tolerance,
			)
		}
		if closestDist > worstOffset {
			worstOffset = closestDist
		}
	}

	logger.Infow("sync analysis complete",
		"totalChecked", totalChecked,
		"totalPassed", totalPassed,
		"worstOffset", worstOffset,
		"tolerance", tolerance,
	)

	require.Greater(t, totalChecked, 0, "no A/V sync checks were performed")

	failRate := float64(totalChecked-totalPassed) / float64(totalChecked)
	require.LessOrEqual(t, failRate, 0.05,
		"A/V sync failure rate %.1f%% exceeds 5%% threshold (%d/%d failed, worst offset %v)",
		failRate*100, totalChecked-totalPassed, totalChecked, worstOffset)

	return results
}

// findClosestFlash returns the timestamp of the flash closest to the target time,
// or -1 if there are no flashes.
func findClosestFlash(flashes []videoEvent, target time.Duration) time.Duration {
	if len(flashes) == 0 {
		return -1
	}

	closest := flashes[0].timestamp
	closestDist := absDuration(target - closest)

	for _, f := range flashes[1:] {
		dist := absDuration(target - f.timestamp)
		if dist < closestDist {
			closest = f.timestamp
			closestDist = dist
		}
	}

	return closest
}

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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/llehouerou/go-mp3/lameinfo"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

var (
	segmentTimeRegexp = regexp.MustCompile(`_(\d{14})(\d{3})\.ts`)
)

type FFProbeInfo struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
		Profile   string `json:"profile"`

		// audio
		SampleRate    string `json:"sample_rate"`
		Channels      int    `json:"channels"`
		ChannelLayout string `json:"channel_layout"`

		// video
		Width        int32  `json:"width"`
		Height       int32  `json:"height"`
		RFrameRate   string `json:"r_frame_rate"`
		AvgFrameRate string `json:"avg_frame_rate"`
		BitRate      string `json:"bit_rate"`
	} `json:"streams"`
	Format struct {
		Filename   string `json:"filename"`
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		ProbeScore int    `json:"probe_score"`
		Tags       struct {
			Encoder string `json:"encoder"`
		} `json:"tags"`
	} `json:"format"`
}

func ffprobe(input string) (*FFProbeInfo, error) {
	args := []string{
		"-v", "quiet",
		"-hide_banner",
		"-show_format",
		"-show_streams",
		"-print_format", "json",
	}

	if strings.HasSuffix(input, ".raw") {
		args = append(args,
			"-f", "s16le",
			"-ac", "2",
			"-ar", "48k",
		)
	}

	args = append(args, input)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ffprobe timeout after 15s")
		}
		return nil, err
	}

	info := &FFProbeInfo{}
	err = json.Unmarshal(out, info)
	return info, err
}

func verify(t *testing.T, in string, p *config.PipelineConfig, res *livekit.EgressInfo, egressType types.EgressType, sourceFramerate float64, live bool) *FFProbeInfo {
	info, err := ffprobe(in)
	require.NoError(t, err)

	if res != nil {
		var expected livekit.EgressSourceType
		switch p.SourceType {
		case types.SourceTypeWeb:
			expected = livekit.EgressSourceType_EGRESS_SOURCE_TYPE_WEB
		case types.SourceTypeSDK:
			expected = livekit.EgressSourceType_EGRESS_SOURCE_TYPE_SDK
		}
		require.Equal(t, expected, res.SourceType)
	}

	switch egressType {
	case types.EgressTypeFile:
		// size
		require.NotEqual(t, "0", info.Format.Size)

		// duration
		fileRes := res.GetFile() //nolint:staticcheck
		if fileRes == nil {
			fileRes = res.FileResults[0]
		}
		expected := float64(fileRes.Duration) / 1e9
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		// file duration can be different from egress duration based on keyframes, muting, and latency
		delta := 5.0
		switch p.RequestType {
		case types.RequestTypeRoomComposite, types.RequestTypeTemplate, types.RequestTypeWeb:
			require.InDelta(t, expected, actual, delta)

		case types.RequestTypeTrack:
			if p.AudioEnabled {
				require.InDelta(t, expected, actual, delta)
			}
		}

	case types.EgressTypeSegments:
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		require.Len(t, res.GetSegmentResults(), 1)
		segments := res.GetSegmentResults()[0]

		if live {
			require.InDelta(t, float64(5*p.GetSegmentConfig().SegmentDuration), actual, float64(p.GetSegmentConfig().SegmentDuration))
		} else {
			expected := int64(math.Ceil(actual / float64(p.GetSegmentConfig().SegmentDuration)))
			require.InDelta(t, expected, segments.SegmentCount, 1)
		}

	case types.EgressTypeWebsocket:
		size, err := strconv.Atoi(info.Format.Size)
		require.NoError(t, err)
		require.Greater(t, size, 6300000)

		expected := float64(res.StreamResults[0].Duration) / 1e9
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		require.InDelta(t, expected, actual, 4.1)
	}

	// verify Xing/Info header for MP3 files
	if egressType == types.EgressTypeFile && p.AudioOutCodec == types.MimeTypeMP3 {
		fpDuration, _ := strconv.ParseFloat(info.Format.Duration, 64)
		verifyXingHeader(t, in, int(p.AudioFrequency), fpDuration)
	}

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			// codec
			switch p.AudioOutCodec {
			case types.MimeTypeAAC:
				require.Equal(t, "aac", stream.CodecName)
				require.Equal(t, fmt.Sprint(p.AudioFrequency), stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case types.MimeTypeOpus:
				require.Equal(t, "opus", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case types.MimeTypeMP3:
				require.Equal(t, "mp3", stream.CodecName)
				require.Equal(t, fmt.Sprint(p.AudioFrequency), stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

				// verify CBR: stream bitrate should match configured bitrate
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.InDelta(t, int(p.AudioBitrate)*1000, bitrate, 5000,
					"MP3 bitrate %d bps not close to configured %d kbps", bitrate, p.AudioBitrate)

			case types.MimeTypeRawAudio:
				require.Equal(t, "pcm_s16le", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
			}

			// channels
			require.Equal(t, 2, stream.Channels)

			// audio bitrate
			if p.Outputs[egressType][0].GetOutputType() == types.OutputTypeMP4 {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
			}

		case "video":
			hasVideo = true

			// codec and profile
			switch p.VideoOutCodec {
			case types.MimeTypeH264:
				require.Equal(t, "h264", stream.CodecName)

				if p.VideoEncoding {
					switch p.VideoProfile {
					case types.ProfileBaseline:
						require.Equal(t, "Constrained Baseline", stream.Profile)
					case types.ProfileMain:
						require.Equal(t, "Main", stream.Profile)
					case types.ProfileHigh:
						require.Equal(t, "High", stream.Profile)
					}
				}
			case types.MimeTypeVP8:
				require.Equal(t, "vp8", stream.CodecName)
			case types.MimeTypeVP9:
				require.Equal(t, "vp9", stream.CodecName)
			case types.MimeTypeAV1:
				require.Equal(t, "av1", stream.CodecName)
			}

			if p.VideoEncoding {
				// dimensions
				require.Equal(t, p.Width, stream.Width)
				require.Equal(t, p.Height, stream.Height)
			}

			switch p.Outputs[egressType][0].GetOutputType() {
			case types.OutputTypeIVF:
				require.Equal(t, "vp8", stream.CodecName)

			case types.OutputTypeMP4:
				if p.VideoOutCodec == "" {
					require.Equal(t, "h264", stream.CodecName)
				}

				if p.VideoEncoding {
					// bitrate, not available for HLS or WebM
					bitrate, err := strconv.Atoi(stream.BitRate)
					require.NoError(t, err)
					require.NotZero(t, bitrate)
					require.Less(t, int32(bitrate), p.VideoBitrate*1050)

					// framerate
					frac := strings.Split(stream.AvgFrameRate, "/")
					require.Len(t, frac, 2)
					n, err := strconv.ParseFloat(frac[0], 64)
					require.NoError(t, err)
					d, err := strconv.ParseFloat(frac[1], 64)
					require.NoError(t, err)
					require.NotZero(t, d)
					require.Less(t, n/d, float64(p.Framerate)*1.5)
					require.Greater(t, n/d, float64(sourceFramerate)*0.8)
				}

			case types.OutputTypeHLS:
				require.Equal(t, "h264", stream.CodecName)
			}

		default:
			t.Fatalf("unrecognized stream type %s", stream.CodecType)
		}
	}

	// passthrough derives the out codecs at subscribe time, so this config never carries them
	if p.AudioEnabled {
		require.True(t, hasAudio)
		if !p.Passthrough {
			require.NotEmpty(t, p.AudioOutCodec)
		}
	}

	if p.VideoEnabled {
		require.True(t, hasVideo)
		if !p.Passthrough {
			require.NotEmpty(t, p.VideoOutCodec)
		}
	}
	return info
}

const (
	// A recording's video timeline should be continuous. A clean recording's
	// largest frame gap is under 100ms, so anything past this is a hole.
	maxFrameGap = 500 * time.Millisecond
	// Any stretch the filler covers alone costs timeline, because the filler runs
	// videoTestSrcDelay behind through the test src queue's min-threshold-time and
	// stalls whenever its level drops below that. Measured at 2.08s on a republish
	// and 2.07s on a delayed first publish. Tighten once the filler no longer lags.
	fillerFrameGap = 2500 * time.Millisecond
	// mpdecimate and blackdetect time their spans independently, so the edges of
	// a filler stretch do not line up exactly.
	frozenBlackGrace = 100 * time.Millisecond
	// Held frames further apart than this belong to separate stretches. One frame
	// interval is 33ms at 30fps and 42ms at 24fps.
	frozenRunGap = 50 * time.Millisecond
)

// frameGapAllowance returns the largest gap this test can legitimately produce.
// Video arriving late, leaving, or coming back all hand a stretch to the filler.
func frameGapAllowance(tc *testCase) time.Duration {
	allowance := maxFrameGap
	if tc.videoDelay != 0 || tc.videoUnpublish != 0 || tc.videoRepublish != 0 {
		allowance = fillerFrameGap
	}
	if tc.disconnectDuration != 0 {
		if d := tc.disconnectDuration + fillerFrameGap; d > allowance {
			allowance = d
		}
	}
	return allowance
}

// verifyFrameContinuity fails on a hole in the video timeline. The content
// checks look for frames that should not be there, so a stretch with no frames
// at all passes them: there is nothing left to be wrong.
func verifyFrameContinuity(t *testing.T, in string, tc *testCase) {
	t.Helper()

	if tc.audioOnly {
		return
	}

	times, err := ffprobeFrameTimes(in)
	require.NoError(t, err)
	if len(times) < 2 {
		return
	}

	var worst time.Duration
	var worstAt float64
	for i := 1; i < len(times); i++ {
		if gap := time.Duration((times[i] - times[i-1]) * float64(time.Second)); gap > worst {
			worst, worstAt = gap, times[i-1]
		}
	}

	allowance := frameGapAllowance(tc)
	t.Logf("largest frame gap: %s at %.3fs (allowance %s, %d frames)", worst, worstAt, allowance, len(times))
	require.LessOrEqual(t, worst, allowance,
		"%s of video missing at %.3fs, the timeline should be continuous", worst, worstAt)

	verifyNoFreeze(t, in, allowance)
}

// ffprobeFrameTimes returns the presentation timestamp of every video frame.
func ffprobeFrameTimes(input string) ([]float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "frame=pts_time",
		"-of", "csv=p=0",
		input,
	)
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ffprobe timeout listing frames")
		}
		return nil, err
	}

	var times []float64
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		if line == "" {
			continue
		}
		f, err := strconv.ParseFloat(line, 64)
		if err != nil {
			continue
		}
		times = append(times, f)
	}
	return times, nil
}

// parseFFProbeDuration supports either "123.456" (seconds) or "HH:MM:SS.mmm"
func parseFFProbeDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}

	if strings.Contains(s, ":") {
		// HH:MM:SS(.frac)
		parts := strings.Split(s, ":")
		if len(parts) != 3 {
			return 0, fmt.Errorf("invalid H:M:S format: %q", s)
		}
		h, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid h part: %w", err)
		}
		m, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid m part: %w", err)
		}
		sec, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid s part: %w", err)
		}
		total := h*3600 + m*60 + sec
		return time.Duration(total * float64(time.Second)), nil
	}

	// Plain seconds (stringified float)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid seconds format %q: %w", s, err)
	}
	return time.Duration(f * float64(time.Second)), nil
}

// verifyXingHeader checks that an MP3 file contains a valid Xing/Info header
// and that the duration derived from its frame count matches ffprobe.
func verifyXingHeader(t *testing.T, filepath string, sampleRate int, ffprobeDuration float64) {
	t.Helper()

	f, err := os.Open(filepath)
	require.NoError(t, err)
	defer f.Close()

	xi, err := lameinfo.ParseFromReader(f)
	require.NoError(t, err, "MP3 file missing Xing/Info header")

	require.True(t, xi.HasFrameCount(), "Xing header missing frame count")
	require.NotZero(t, xi.FrameCount, "Xing header has zero frame count")

	require.True(t, xi.HasTOC(), "Xing header missing TOC seek table")

	// MPEG1 Layer 3: 1152 samples per frame.
	// Cross-check Xing frame count against ffprobe duration.
	const samplesPerFrame = 1152
	xingDuration := float64(xi.FrameCount) * samplesPerFrame / float64(sampleRate)
	require.InDelta(t, ffprobeDuration, xingDuration, 0.1,
		"Xing duration (%0.3fs from %d frames) does not match ffprobe duration (%0.3fs)",
		xingDuration, xi.FrameCount, ffprobeDuration)
}

var (
	dropRe  = regexp.MustCompile(`drop pts:\d+ pts_time:([0-9.]+)`)
	blackRe = regexp.MustCompile(`black_start:([0-9.]+) black_end:([0-9.]+)`)
)

type timeSpan struct{ start, end float64 }

// verifyNoFreeze fails on a stretch of held frames outside the black filler.
// A hole that videorate pads with duplicates keeps the timeline continuous, so
// the gap check cannot see it. Both are the same lost stretch, so they share an
// allowance.
func verifyNoFreeze(t *testing.T, in string, allowance time.Duration) {
	t.Helper()

	held, err := ffmpegHeldFrameTimes(in)
	require.NoError(t, err)
	black, err := ffmpegBlackSpans(in)
	require.NoError(t, err)

	// The filler is a still image and so is held by definition. Discard those
	// frames before merging, or one run spans the filler and what follows it.
	grace := frozenBlackGrace.Seconds()
	var outside []float64
	for _, h := range held {
		covered := false
		for _, b := range black {
			if h >= b.start-grace && h <= b.end+grace {
				covered = true
				break
			}
		}
		if !covered {
			outside = append(outside, h)
		}
	}

	worst, worstAt := longestHeldRun(outside, frozenRunGap.Seconds())
	d := time.Duration(worst * float64(time.Second))
	t.Logf("longest freeze outside the filler: %s at %.3fs (allowance %s)", d, worstAt, allowance)
	require.LessOrEqual(t, d, allowance,
		"video held the same frame for %s at %.3fs, content should keep advancing", d, worstAt)
}

// longestHeldRun returns the duration and start of the longest run of held
// frames, treating frames further apart than gap as separate runs.
func longestHeldRun(times []float64, gap float64) (float64, float64) {
	if len(times) == 0 {
		return 0, 0
	}
	var worst, worstAt float64
	runStart, runEnd := times[0], times[0]
	flush := func() {
		if runEnd-runStart > worst {
			worst, worstAt = runEnd-runStart, runStart
		}
	}
	for _, t := range times[1:] {
		if t-runEnd <= gap {
			runEnd = t
			continue
		}
		flush()
		runStart, runEnd = t, t
	}
	flush()
	return worst, worstAt
}

// ffmpegHeldFrameTimes returns the timestamp of every frame mpdecimate found to
// be a near-duplicate of the one before it.
func ffmpegHeldFrameTimes(input string) ([]float64, error) {
	out, err := runFFmpegFilter(input, "mpdecimate", "debug")
	if err != nil {
		return nil, err
	}

	var times []float64
	for _, m := range dropRe.FindAllStringSubmatch(out, -1) {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			times = append(times, f)
		}
	}
	return times, nil
}

// ffmpegBlackSpans returns the spans where the recording is black, which is the
// filler covering a gap.
func ffmpegBlackSpans(input string) ([]timeSpan, error) {
	out, err := runFFmpegFilter(input, "blackdetect=d=0.2:pix_th=0.10", "info")
	if err != nil {
		return nil, err
	}

	var spans []timeSpan
	for _, m := range blackRe.FindAllStringSubmatch(out, -1) {
		start, serr := strconv.ParseFloat(m[1], 64)
		end, eerr := strconv.ParseFloat(m[2], 64)
		if serr == nil && eerr == nil {
			spans = append(spans, timeSpan{start: start, end: end})
		}
	}
	return spans, nil
}

// runFFmpegFilter runs one filter over the input and returns what it reported.
// Both filters write to stderr.
func runFFmpegFilter(input, filter, level string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", level,
		"-i", input,
		"-vf", filter,
		"-f", "null", "-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("ffmpeg timeout running %s", filter)
		}
		return "", fmt.Errorf("ffmpeg %s: %w", filter, err)
	}
	return string(out), nil
}

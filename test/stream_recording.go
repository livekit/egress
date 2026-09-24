// Copyright 2026 LiveKit, Inc.
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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const (
	// mediamtx records every stream it receives under recordPath, set in
	// build/test/Dockerfile. Each stream key gets its own directory.
	streamRecordingRoot = "/out/output/rec"

	// mediamtx starts a recording on the first keyframe and closes it when the
	// publisher disconnects, so the recording runs short of the reported stream
	// by up to a keyframe interval, plus connection setup.
	streamRecordingDurationSlack = 2.0

	streamRecordingTimeout       = 30 * time.Second
	streamRecordingSettleTimeout = 30 * time.Second
	// A recording is finalized once its size holds still across two reads.
	streamRecordingSettleInterval = time.Second
)

// verifyStreamRecording checks what the stream sink actually delivered, by
// reading back the copy mediamtx recorded. Probing the live url only reports
// the caps the muxer advertised; the recording carries the timestamps, so it is
// the only place the suite can see a stream drift or stall.
func (r *Runner) verifyStreamRecording(
	t *testing.T, tc *testCase, p *config.PipelineConfig, res *livekit.EgressInfo,
	url []string, since time.Time,
) {
	publishUrl, redactedUrl := url[0], url[1]

	streamInfo := findStreamResult(res, redactedUrl)
	require.NotNil(t, streamInfo, "no stream result for %s", redactedUrl)

	recording := awaitStreamRecording(t, streamKeyFromUrl(publishUrl), since)
	t.Logf("stream recording: %s", recording)

	info := verify(t, recording, p, res, types.EgressTypeStream, r.sourceFramerate, false)

	recorded, err := parseFFProbeDuration(info.Format.Duration)
	require.NoError(t, err)

	keyframeInterval := p.KeyFrameInterval
	if keyframeInterval <= 0 {
		keyframeInterval = config.StreamKeyframeInterval
	}
	require.InDelta(t, float64(streamInfo.Duration)/1e9, recorded.Seconds(),
		keyframeInterval+streamRecordingDurationSlack,
		"recording holds %s of media, egress reported %s streamed",
		recorded, time.Duration(streamInfo.Duration))

	runAVSyncCheck(t, tc, recording, info, "stream", formatFromStreamURL(publishUrl))
}

func findStreamResult(res *livekit.EgressInfo, redactedUrl string) *livekit.StreamInfo {
	for _, s := range res.StreamResults {
		if s.Url == redactedUrl {
			return s
		}
	}
	return nil
}

// streamKeyFromUrl returns the mediamtx path for a publish url: the last
// segment for rtmp, and the streamid for srt.
func streamKeyFromUrl(url string) string {
	if i := strings.Index(url, "streamid=publish:"); i >= 0 {
		key := url[i+len("streamid=publish:"):]
		if j := strings.IndexByte(key, '&'); j >= 0 {
			key = key[:j]
		}
		return key
	}

	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		return url[i+1:]
	}
	return url
}

// awaitStreamRecording returns the recording for streamKey that this test
// produced, once mediamtx has finished writing it. Stream keys are reused
// across tests, so only recordings still being written after the egress started
// are considered.
func awaitStreamRecording(t *testing.T, streamKey string, since time.Time) string {
	var newest string
	findDeadline := time.Now().Add(streamRecordingTimeout)
	for time.Now().Before(findDeadline) {
		if newest = newestRecording(t, streamKey, since); newest != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NotEmpty(t, newest,
		"no recording for stream key %s under %s; mediamtx records every stream it receives, so nothing there means nothing reached it",
		streamKey, streamRecordingRoot)

	var lastSize int64 = -1
	settleDeadline := time.Now().Add(streamRecordingSettleTimeout)
	for time.Now().Before(settleDeadline) {
		fi, err := os.Stat(newest)
		require.NoError(t, err)
		if fi.Size() == lastSize {
			require.Greater(t, fi.Size(), int64(0), "recording %s is empty", newest)
			return newest
		}
		lastSize = fi.Size()
		time.Sleep(streamRecordingSettleInterval)
	}

	t.Fatalf("recording %s still growing after %s", newest, streamRecordingSettleTimeout)
	return ""
}

// newestRecording returns the most recently written recording for streamKey, or
// "" when none was written since the given time. An rtmp path carries an
// application segment ("live/<key>"), an srt path does not.
func newestRecording(t *testing.T, streamKey string, since time.Time) string {
	var candidates []string
	for _, pattern := range []string{
		filepath.Join(streamRecordingRoot, streamKey, "*"),
		filepath.Join(streamRecordingRoot, "*", streamKey, "*"),
	} {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)
		candidates = append(candidates, matches...)
	}

	type entry struct {
		path    string
		modTime time.Time
	}
	var found []entry
	for _, c := range candidates {
		fi, err := os.Stat(c)
		if err != nil || !fi.Mode().IsRegular() || fi.ModTime().Before(since) {
			continue
		}
		found = append(found, entry{path: c, modTime: fi.ModTime()})
	}
	if len(found) == 0 {
		return ""
	}

	sort.Slice(found, func(i, j int) bool { return found[i].modTime.After(found[j].modTime) })
	if len(found) > 1 {
		logger.Debugw("multiple recordings for stream key, using the newest",
			"streamKey", streamKey, "count", len(found), "path", found[0].path)
	}
	return found[0].path
}

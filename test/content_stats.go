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
	"path"
	"strings"

	"github.com/livekit/media-samples/avsync"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/test/cadence"
)

// recordContentStats runs the pure cadence.Compute against the
// quantized observation, layers on testCase-derived identity fields,
// logs the row (for Datadog), and hands it to cadence.Record. Safe
// for concurrent calls.
func recordContentStats(tc *testCase, obs *cadence.Observation, output, format string) {
	s := cadence.Compute(obs, tc.audioOnly, tc.videoOnly)

	s.IntegrationType = os.Getenv("INTEGRATION_TYPE")
	s.Test = tc.name
	s.RequestType = string(tc.requestType)
	s.Source = cadence.DeriveSource(string(tc.requestType))
	s.Output = output
	s.Format = format
	s.AudioCodec = string(tc.audioCodec)
	s.VideoCodec = string(tc.videoCodec)
	s.Layout = tc.layout
	s.Tracks = inputTrackCount(tc)

	logger.Infow("avsync stats",
		"test", s.Test,
		"requestType", s.RequestType,
		"source", s.Source,
		"output", s.Output,
		"format", s.Format,
		"audioCodec", s.AudioCodec,
		"videoCodec", s.VideoCodec,
		"layout", s.Layout,
		"audioOnly", s.AudioOnly,
		"videoOnly", s.VideoOnly,
		"tracks", s.Tracks,
		"score", s.Score,
		"flashes", s.FlashCount,
		"beeps", s.BeepCount,
		"timeToStable", s.TimeToStable,
		"audioJitter", s.AudioJitter,
		"videoJitter", s.VideoJitter,
		"avSync", s.StableAVSync,
		"avSyncStdDev", s.AVSyncStdDev,
		"maxAVSync", s.MaxAVSync,
	)

	cadence.Record(s)
}

// inputTrackCount returns the input track count for a test case:
// participants × (audio + video tracks per participant).
func inputTrackCount(tc *testCase) int {
	participants := 1
	if tc.multiParticipant {
		participants = len(avsync.AllParticipants) // 3
	}
	perPart := 2
	if tc.audioOnly || tc.videoOnly {
		perPart = 1
	}
	return participants * perPart
}

// formatFromFileName returns a lowercase extension without the leading
// dot, e.g. "mp4" for "/tmp/recording.mp4". Used by file-output callers.
func formatFromFileName(name string) string {
	ext := strings.TrimPrefix(path.Ext(name), ".")
	return strings.ToLower(ext)
}

// formatFromStreamURL extracts the protocol from a stream URL, e.g.
// "rtmp" from "rtmp://host/app/stream". Returns "" if unrecognized.
func formatFromStreamURL(url string) string {
	if i := strings.Index(url, "://"); i > 0 {
		return strings.ToLower(url[:i])
	}
	return ""
}

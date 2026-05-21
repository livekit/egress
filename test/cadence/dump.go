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

package cadence

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/linkdata/deadlock"
)

// allStats collects one row per Record call. Dump reads it at
// end-of-test (registered via t.Cleanup in TestEgress).
var (
	allStats   []Stats
	allStatsMu deadlock.Mutex
)

// Record appends a fully-populated Stats row to the global slice.
// Safe for concurrent calls. Called by integration test glue once
// per output verification.
func Record(s Stats) {
	allStatsMu.Lock()
	allStats = append(allStats, s)
	allStatsMu.Unlock()
}

// Dump writes the collected stats to AVSYNC_STATS_PATH (default
// /tmp/avsync-stats.json). Intended to be invoked from t.Cleanup in
// TestEgress so it runs once after every output has been processed.
//
// Inapplicable metrics (e.g., video stats on audio-only outputs)
// serialize as JSON null so the rendered table can show "-" instead
// of misleading 0.
func Dump() {
	allStatsMu.Lock()
	defer allStatsMu.Unlock()

	out := make([]map[string]any, 0, len(allStats))
	for _, s := range allStats {
		// Durations are emitted as float64 seconds (not duration strings)
		// so the jq renderer doesn't have to parse Go's multi-unit
		// duration format (e.g., "1m30s"). Callers' log lines keep
		// time.Duration values for human readability.
		var (
			flashes      any = s.FlashCount
			videoJitter  any = s.VideoJitter.Seconds()
			beeps        any = s.BeepCount
			audioJitter  any = s.AudioJitter.Seconds()
			stableAVSync any = s.StableAVSync.Seconds()
			avSyncStdDev any = s.AVSyncStdDev.Seconds()
			maxAVSync    any = s.MaxAVSync.Seconds()
		)
		if s.AudioOnly {
			flashes, videoJitter = nil, nil
		}
		if s.VideoOnly {
			beeps, audioJitter = nil, nil
		}
		if s.AudioOnly || s.VideoOnly {
			stableAVSync, avSyncStdDev, maxAVSync = nil, nil, nil
		}

		out = append(out, map[string]any{
			"integrationType": s.IntegrationType,
			"test":            s.Test,
			"requestType":     s.RequestType,
			"source":          s.Source,
			"output":          s.Output,
			"format":          s.Format,
			"audioCodec":      s.AudioCodec,
			"videoCodec":      s.VideoCodec,
			"layout":          s.Layout,
			"audioOnly":       s.AudioOnly,
			"videoOnly":       s.VideoOnly,
			"tracks":          s.Tracks,
			"flashes":         flashes,
			"beeps":           beeps,
			"score":           s.Score,
			"timeToStable":    s.TimeToStable.Seconds(),
			"audioJitter":     audioJitter,
			"videoJitter":     videoJitter,
			"avSync":          stableAVSync,
			"avSyncStdDev":    avSyncStdDev,
			"maxAVSync":       maxAVSync,
		})
	}

	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cadence.Dump: marshal failed: %v\n", err)
		return
	}

	target := os.Getenv("AVSYNC_STATS_PATH")
	if target == "" {
		target = "/tmp/avsync-stats.json"
	}
	if err := os.WriteFile(target, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "cadence.Dump: write %s failed: %v\n", target, err)
		return
	}
}

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

package source

import (
	"strconv"
	"testing"

	jsonv2 "github.com/chromedp/cdproto/cdp/jsonv2"
	"github.com/chromedp/cdproto/runtime"
	"github.com/stretchr/testify/require"
)

func consoleEvent(t *testing.T, args string) runtime.EventConsoleAPICalled {
	t.Helper()
	var ev runtime.EventConsoleAPICalled
	raw := `{"type":"log","executionContextId":1,"timestamp":1,"args":` + args + `}`
	require.NoError(t, jsonv2.Unmarshal(jsonv2.Value(raw), &ev))
	return ev
}

func stringArg(v string) string {
	return `{"type":"string","value":` + strconv.Quote(v) + `}`
}

func TestInboundStatsFields(t *testing.T) {
	stats := `{"trackSid":"TR_video","kind":"video","jitterBufferMs":612.5,"playoutClockMs":-12.5,"framesDecoded":300}`
	ev := consoleEvent(t, `[`+stringArg(inboundStatsLog)+`,`+stringArg(stats)+`]`)

	fields, ok := inboundStatsFields(ev.Args)
	require.True(t, ok)
	require.Equal(t, []any{
		"framesDecoded", float64(300),
		"jitterBufferMs", 612.5,
		"kind", "video",
		"playoutClockMs", -12.5,
		"trackSid", "TR_video",
	}, fields)

	for name, args := range map[string]string{
		"start recording": `[` + stringArg(startRecordingLog) + `]`,
		"other tag":       `[` + stringArg("SOMETHING_ELSE") + `,` + stringArg(stats) + `]`,
		"tag only":        `[` + stringArg(inboundStatsLog) + `]`,
		"malformed body":  `[` + stringArg(inboundStatsLog) + `,` + stringArg("{not json") + `]`,
		"object arg":      `[` + stringArg(inboundStatsLog) + `,{"type":"object","objectId":"1"}]`,
	} {
		_, ok := inboundStatsFields(consoleEvent(t, args).Args)
		require.False(t, ok, name)
	}
}

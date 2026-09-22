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

package logging

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Only the handler's own structured lines go to the sink.
func TestHandlerLoggerSink(t *testing.T) {
	var got []string
	h := NewHandlerLoggerWithSink("EGH_abc", "EG_123", func(line string) {
		got = append(got, line)
	})

	for _, s := range []string{
		`{"level":"info","msg":"started"}` + "\n",
		"0:00:00.1 gstreamer noise\n",
		"(egress:12): GLib-GObject-WARNING\n",
		"ice ERROR: something\n",
		`{"level":"error","msg":"failed"}` + "\n",
	} {
		_, err := h.Write([]byte(s))
		require.NoError(t, err)
	}
	require.NoError(t, h.Close())

	require.Equal(t, []string{
		`{"level":"info","msg":"started"}`,
		`{"level":"error","msg":"failed"}`,
	}, got)
}

// The handler's stdout is a pipe, so an entry can arrive in pieces.
func TestHandlerLoggerSinkReassemblesSplitLines(t *testing.T) {
	var got []string
	h := NewHandlerLoggerWithSink("EGH_abc", "EG_123", func(line string) {
		got = append(got, line)
	})

	_, err := h.Write([]byte(`{"level":"info",`))
	require.NoError(t, err)
	_, err = h.Write([]byte(`"msg":"split"}` + "\n"))
	require.NoError(t, err)
	require.NoError(t, h.Close())

	require.Equal(t, []string{`{"level":"info","msg":"split"}`}, got)
}

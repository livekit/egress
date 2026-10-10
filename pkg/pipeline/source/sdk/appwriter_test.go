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

package sdk

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShouldSendEOS pins the cleanup decision in AppWriter.start(): EOS is owed
// by any appsrc linked into the pipeline, reported PLAYING or not. The two differ
// during shutdown, when the PLAYING notification is dropped.
func TestShouldSendEOS(t *testing.T) {
	t.Run("never added to the pipeline", func(t *testing.T) {
		w := &AppWriter{}

		require.False(t, w.shouldSendEOS(),
			"no appsrc in the pipeline means nothing downstream is waiting for EOS")
	})

	t.Run("added to the pipeline but PLAYING never delivered", func(t *testing.T) {
		w := &AppWriter{}
		w.MarkAddedToPipeline()

		require.False(t, w.playing.IsBroken(), "precondition: the notification was dropped")
		require.True(t, w.shouldSendEOS(),
			"the appsrc is linked into the pipeline, so cleanup must send EOS even "+
				"though it was never reported PLAYING")

		// this state is what AppWriter.start() logs as
		// "appsrc never reported PLAYING, sending EOS anyway"
	})

	t.Run("PLAYING implies added to the pipeline", func(t *testing.T) {
		w := &AppWriter{}
		w.Playing()

		require.True(t, w.addedToPipeline.IsBroken(),
			"an appsrc cannot reach PLAYING without being linked into the pipeline")
		require.True(t, w.shouldSendEOS(), "the normal path must still send EOS")
	})
}

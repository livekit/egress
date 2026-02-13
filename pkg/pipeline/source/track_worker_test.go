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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

// testSDKSource creates a minimal SDKSource for testing state transitions
func testSDKSource(t *testing.T) *SDKSource {
	t.Helper()

	buildReady := make(chan struct{})
	close(buildReady) // already ready

	callbacks := &gstreamer.Callbacks{
		BuildReady: buildReady,
	}
	callbacks.AddOnTrackRemoved(func(_ string) {})

	pipelineConfig := &config.PipelineConfig{
		RequestType: types.RequestTypeRoomComposite,
	}

	return &SDKSource{
		PipelineConfig:       pipelineConfig,
		callbacks:            callbacks,
		sync:                 synchronizer.NewSynchronizer(nil),
		workers:              make(map[string]*trackWorker),
		filenameReplacements: make(map[string]string),
		active:               atomic.Int32{},
		closing:              atomic.Bool{},
	}
}

func TestGetOrCreateWorker_ReturnsExistingWorker(t *testing.T) {
	s := testSDKSource(t)

	w1 := s.getOrCreateWorker("track-1")
	w2 := s.getOrCreateWorker("track-1")

	assert.Equal(t, w1, w2, "should return same worker for same trackID")
}

func TestGetOrCreateWorker_ReturnsNilWhenClosing(t *testing.T) {
	s := testSDKSource(t)
	s.closing.Store(true)

	w := s.getOrCreateWorker("track-1")

	assert.Nil(t, w, "should return nil when closing")
}

func TestSubmitOp_DropsOpWhenClosing(t *testing.T) {
	s := testSDKSource(t)
	s.closing.Store(true)

	// This should not panic or block
	s.submitOp("track-1", Operation{Type: OpPlaying})

	s.workersMu.RLock()
	_, exists := s.workers["track-1"]
	s.workersMu.RUnlock()

	assert.False(t, exists)
}

func TestStateTransitions_IdleState(t *testing.T) {
	tests := []struct {
		name      string
		op        OpType
		wantState TrackState
		wantExit  bool
	}{
		// Valid ops in IDLE
		{"OpClose exits", OpClose, TrackStateIdle, true},
		{"OpPlaying stays IDLE", OpPlaying, TrackStateIdle, false},
		{"OpSetTimeProvider stays IDLE", OpSetTimeProvider, TrackStateIdle, false},
		// Invalid ops in IDLE (should log warning but not crash)
		{"OpUnsubscribe stays IDLE", OpUnsubscribe, TrackStateIdle, false},
		{"OpFinished stays IDLE", OpFinished, TrackStateIdle, false},
		// Note: OpSubscribe requires GStreamer, tested in integration tests
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSDKSource(t)
			w := &trackWorker{
				trackID:    "test-track",
				generation: atomic.Uint64{},
			}
			state := &workerState{state: TrackStateIdle}

			exit := s.processOp(w, "test-track", state, Operation{Type: tt.op})

			assert.Equal(t, tt.wantExit, exit, "exit mismatch")
			assert.Equal(t, tt.wantState, state.state, "state mismatch")
		})
	}
}

// Note: Operations that need a real writer (Unsubscribe, Finished) require integration tests
func TestStateTransitions_ActiveState(t *testing.T) {
	tests := []struct {
		name      string
		op        OpType
		wantState TrackState
		wantExit  bool
	}{
		{"OpClose drains and exits", OpClose, TrackStateIdle, true},
		{"OpPlaying stays ACTIVE", OpPlaying, TrackStateActive, false},
		{"OpSetTimeProvider stays ACTIVE", OpSetTimeProvider, TrackStateActive, false},
		{"OpSubscribe stays ACTIVE (invalid)", OpSubscribe, TrackStateActive, false},
		// Note: OpUnsubscribe and OpFinished trigger cleanup which needs a real writer
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSDKSource(t)
			w := &trackWorker{
				trackID:    "test-track",
				generation: atomic.Uint64{},
			}
			// Start in ACTIVE state with nil writer (ok for ops that don't use it)
			state := &workerState{
				state:      TrackStateActive,
				writer:     nil,
				generation: 1,
			}
			s.active.Store(1) // simulate one active track

			exit := s.processOp(w, "test-track", state, Operation{Type: tt.op, Generation: 1})

			assert.Equal(t, tt.wantExit, exit, "exit mismatch")
			assert.Equal(t, tt.wantState, state.state, "state mismatch")
		})
	}
}

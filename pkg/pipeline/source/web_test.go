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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/gstreamer"
)

func TestWebSourceOnRoomInfo(t *testing.T) {
	type room struct{ name, id string }
	reported := make(chan room, 4)
	callbacks := &gstreamer.Callbacks{}
	callbacks.SetOnRoomChanged(func(name, id string) {
		reported <- room{name, id}
	})
	s := &WebSource{callbacks: callbacks, roomChanged: make(chan struct{}, 1)}
	go s.reportRoomInfo()
	defer s.closed.Break()

	s.onRoomInfo(`not json`)
	s.onRoomInfo(`{"name":"room","sid":""}`)
	s.onRoomInfo(`{"name":"room","sid":"RM_abc"}`)

	select {
	case r := <-reported:
		require.Equal(t, room{"room", "RM_abc"}, r)
	case <-time.After(time.Second):
		t.Fatal("room not reported")
	}
	select {
	case r := <-reported:
		t.Fatalf("unexpected report %v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

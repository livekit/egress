package source

import (
	"time"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	// test-only: delay between the egress becoming active and the injected
	// retryable disconnect, so a non-empty partial recording is produced
	disconnectInjectionDelay = time.Second * 5
)

// injectDisconnectForTest simulates a "connection to room failed" disconnect
func (s *SDKSource) injectDisconnectForTest() {
	select {
	case <-s.startRecording.Watch():
	case <-s.closed.Watch():
		return
	}
	select {
	case <-time.After(disconnectInjectionDelay):
	case <-s.closed.Watch():
		return
	}
	logger.Warnw("injecting connection failure for test", nil, "room", s.Info.RoomName)
	s.onDisconnectedWithReason(lksdk.Failed)
}

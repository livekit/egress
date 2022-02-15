package input

import "github.com/livekit/livekit-egress/pkg/errors"

type sdk struct{}

func newSDKSource() (Source, error) {
	return nil, errors.ErrNotSupported("sdk input")
}

func (s *sdk) RoomStarted() chan struct{} {
	return nil
}

func (s *sdk) RoomEnded() chan struct{} {
	return nil
}

func (s *sdk) Close() {}

//go:build test
// +build test

package display

import (
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-recorder/pkg/config"
)

type Display struct {
	endChan chan struct{}
}

func New() *Display {
	return &Display{
		endChan: make(chan struct{}, 1),
	}
}

func (d *Display) Launch(conf *config.Config, url string, opts *livekit.RecordingOptions, isTemplate bool) error {
	return nil
}

func (d *Display) WaitForRoom() {}

func (d *Display) EndMessage() chan struct{} {
	return d.endChan
}

func (d *Display) Close() {
	close(d.endChan)
}

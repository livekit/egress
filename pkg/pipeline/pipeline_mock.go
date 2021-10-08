// +build test

package pipeline

import (
	"time"

	livekit "github.com/livekit/protocol/proto"
)

type Pipeline struct {
	isStream bool
	kill     chan struct{}
}

func NewRtmpPipeline(rtmp []string, options *livekit.RecordingOptions) (*Pipeline, error) {
	return &Pipeline{
		isStream: true,
		kill:     make(chan struct{}, 1),
	}, nil
}

func NewFilePipeline(filename string, options *livekit.RecordingOptions) (*Pipeline, error) {
	return &Pipeline{
		isStream: false,
		kill:     make(chan struct{}, 1),
	}, nil
}

func (p *Pipeline) Start() error {
	select {
	case <-time.After(time.Second * 3):
	case <-p.kill:
	}
	return nil
}

func (p *Pipeline) AddOutput(url string) error {
	if !p.isStream {
		return ErrCannotAddToFile
	}
	return nil
}

func (p *Pipeline) RemoveOutput(url string) error {
	if !p.isStream {
		return ErrCannotRemoveFromFile
	}
	return nil
}

func (p *Pipeline) Close() {
	p.kill <- struct{}{}
}

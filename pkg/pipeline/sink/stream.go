// Copyright 2025 LiveKit, Inc.
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

package sink

import (
	"sync"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/builder"
)

type StreamSink struct {
	*base

	mu      sync.RWMutex
	bin     *builder.StreamBin
	streams map[string]*builder.Stream
}

func newStreamSink(p *gstreamer.Pipeline, o *config.StreamConfig) (*StreamSink, error) {
	streamBin, err := builder.BuildStreamBin(p, o)
	if err != nil {
		return nil, err
	}

	streamSink := &StreamSink{
		base: &base{
			bin: streamBin.Bin,
		},
		bin:     streamBin,
		streams: make(map[string]*builder.Stream),
	}

	o.Streams.Range(func(_, stream any) bool {
		err = streamSink.AddStream(stream.(*config.Stream))
		return err == nil
	})
	if err != nil {
		return nil, err
	}

	if err = p.AddSinkBin(streamBin.Bin); err != nil {
		return nil, err
	}

	return streamSink, nil
}

func (s *StreamSink) Start() error {
	return nil
}

func (s *StreamSink) AddStream(stream *config.Stream) error {
	ss, err := s.bin.BuildStream(stream)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.streams[stream.Name] = ss
	s.mu.Unlock()

	return s.bin.Bin.AddSinkBin(ss.Bin)
}

func (s *StreamSink) GetStream(name string) (*config.Stream, error) {
	s.mu.Lock()
	ss, ok := s.streams[name]
	s.mu.Unlock()
	if !ok {
		return nil, errors.ErrStreamNotFound(name)
	}

	return ss.Conf, nil
}

func (s *StreamSink) ResetStream(stream *config.Stream, streamErr error) (bool, error) {
	s.mu.Lock()
	ss, ok := s.streams[stream.Name]
	s.mu.Unlock()
	if !ok {
		return false, errors.ErrStreamNotFound(stream.RedactedUrl)
	}

	return ss.Reset(streamErr)
}

func (s *StreamSink) RemoveStream(stream *config.Stream) error {
	s.mu.Lock()
	_, ok := s.streams[stream.Name]
	if !ok {
		s.mu.Unlock()
		return errors.ErrStreamNotFound(stream.RedactedUrl)
	}
	delete(s.streams, stream.Name)
	s.mu.Unlock()

	return s.bin.Bin.RemoveSinkBin(stream.Name)
}

func (s *StreamSink) UploadManifest(_ string) (string, bool, error) {
	return "", false, nil
}

func (s *StreamSink) Close() error {
	return nil
}

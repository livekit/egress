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
	"time"

	"github.com/frostbyte73/core"
	"github.com/linkdata/deadlock"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/logging"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type StreamSink struct {
	*base

	conf   *config.PipelineConfig
	bin    *builder.StreamBin
	closed core.Fuse

	mu      deadlock.RWMutex
	streams map[string]*builder.Stream
	loggers map[string]*logging.CSVLogger[logging.StreamStats]
}

func newStreamSink(p *gstreamer.Pipeline, conf *config.PipelineConfig, o *config.StreamConfig) (*StreamSink, error) {
	streamBin, err := builder.BuildStreamBin(p, conf, o)
	if err != nil {
		return nil, err
	}

	ss := &StreamSink{
		base: &base{
			bin: streamBin.Bin,
		},
		conf:    conf,
		bin:     streamBin,
		streams: make(map[string]*builder.Stream),
		loggers: make(map[string]*logging.CSVLogger[logging.StreamStats]),
	}

	o.Streams.Range(func(_, stream any) bool {
		err = ss.AddStream(stream.(*config.Stream))
		return err == nil
	})
	if err != nil {
		return nil, err
	}

	if err = p.AddSinkBin(streamBin.Bin); err != nil {
		return nil, err
	}

	return ss, nil
}

func (s *StreamSink) Start() error {
	if s.conf.Debug.EnableStreamLogging {
		go func() {
			closed := s.closed.Watch()
			ticker := time.NewTicker(time.Second * 10)
			defer ticker.Stop()

			for {
				select {
				case <-closed:
					return

				case <-ticker.C:
					s.mu.RLock()
					for name, stream := range s.streams {
						if stats, ok := stream.Stats(); ok {
							if csvLogger, ok := s.loggers[name]; ok {
								csvLogger.Write(stats)
							}
						}
					}
					s.mu.RUnlock()
				}
			}
		}()
	}

	return nil
}

func (s *StreamSink) AddStream(stream *config.Stream) error {
	ss, err := s.bin.BuildStream(stream, s.conf.Framerate)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.streams[stream.Name] = ss
	if s.conf.Debug.EnableStreamLogging && s.bin.OutputType == types.OutputTypeRTMP {
		csvLogger, err := logging.NewCSVLogger[logging.StreamStats](stream.Name)
		if err != nil {
			logger.Errorw("failed to create stream logger", err)
		} else {
			s.loggers[stream.Name] = csvLogger
		}
	}

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
	s.closed.Once(func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		for _, l := range s.loggers {
			l.Close()
		}
	})

	return nil
}

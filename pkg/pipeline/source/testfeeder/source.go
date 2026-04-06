// Copyright 2024 LiveKit, Inc.
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

package testfeeder

import (
	"fmt"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v4"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// TestSource implements the source.Source interface by feeding pre-encoded media
// files through the GStreamer pipeline at maximum speed. It creates TrackSource
// objects, populates the PipelineConfig, and runs TrackFeeder goroutines after
// the pipeline starts.
//
// GStreamer must be initialized (gst.Init) before calling NewTestSource, since
// it creates appsrc elements.
//
// Usage:
//
//	gst.Init(nil)
//	ts, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
//	    {Path: "audio.ogg", MimeType: types.MimeTypeOpus, PayloadType: 111},
//	    {Path: "video.h264", MimeType: types.MimeTypeH264, PayloadType: 96, FPS: 30},
//	})
//	// conf.AudioTracks, conf.VideoTrack, etc. are now populated.
//	// Build the pipeline, then call ts.Start() to begin feeding.
type TestSource struct {
	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks

	tracks    []trackEntry
	feeders   []*TrackFeeder
	startedAt int64

	endRecording core.Fuse
}

type trackEntry struct {
	def    TrackDef
	ts     *config.TrackSource
	reader FrameReader
}

// TrackDef describes a media file to feed into the pipeline.
type TrackDef struct {
	// Path to the media file (H.264 annexb, VP8/VP9 IVF, or Opus OGG).
	Path string

	// MimeType of the media (types.MimeTypeH264, MimeTypeVP8, MimeTypeOpus, etc.).
	MimeType types.MimeType

	// PayloadType is the RTP payload type number.
	PayloadType uint8

	// FPS is the assumed frame rate for video files. Ignored for audio.
	// Defaults to 30 if zero.
	FPS int

	// TrackID is an optional identifier. Auto-generated if empty.
	TrackID string
}

// NewTestSource creates a TestSource that populates the PipelineConfig with
// TrackSource objects for each track definition. GStreamer must already be
// initialized. After this returns, the config is ready for BuildPipeline.
// Call Start() after the pipeline is built to begin feeding data.
func NewTestSource(conf *config.PipelineConfig, defs []TrackDef) (*TestSource, error) {
	s := &TestSource{
		conf: conf,
	}

	conf.Live = false
	conf.SourceType = types.SourceTypeSDK

	for i, def := range defs {
		if def.TrackID == "" {
			def.TrackID = fmt.Sprintf("test_track_%d", i)
		}

		reader, err := openReader(def)
		if err != nil {
			s.closeReaders()
			return nil, fmt.Errorf("opening %s: %w", def.Path, err)
		}

		src, err := gst.NewElementWithName("appsrc", fmt.Sprintf("app_%s", def.TrackID))
		if err != nil {
			s.closeReaders()
			return nil, fmt.Errorf("creating appsrc: %w", err)
		}

		ts := &config.TrackSource{
			TrackID:     def.TrackID,
			MimeType:    def.MimeType,
			PayloadType: webrtc.PayloadType(def.PayloadType),
			ClockRate:   reader.ClockRate(),
			AppSrc:      app.SrcFromElement(src),
		}

		switch def.MimeType {
		case types.MimeTypeOpus, types.MimeTypePCMU, types.MimeTypePCMA:
			ts.TrackKind = lksdk.TrackKindAudio
			conf.AudioEnabled = true
			conf.AudioTranscoding = true
			if conf.AudioOutCodec == "" {
				if def.MimeType == types.MimeTypePCMU || def.MimeType == types.MimeTypePCMA {
					conf.AudioOutCodec = types.MimeTypeOpus
				} else {
					conf.AudioOutCodec = def.MimeType
				}
			}
			conf.AudioTracks = append(conf.AudioTracks, ts)

		case types.MimeTypeH264, types.MimeTypeVP8, types.MimeTypeVP9:
			ts.TrackKind = lksdk.TrackKindVideo
			conf.VideoEnabled = true
			conf.VideoInCodec = def.MimeType
			if conf.VideoOutCodec == "" {
				conf.VideoOutCodec = def.MimeType
			}
			if conf.VideoInCodec != conf.VideoOutCodec {
				conf.VideoDecoding = true
				if len(conf.GetEncodedOutputs()) > 0 {
					conf.VideoEncoding = true
				}
			}
			conf.VideoTrack = ts

		default:
			s.closeReaders()
			return nil, fmt.Errorf("unsupported mime type: %s", def.MimeType)
		}

		s.tracks = append(s.tracks, trackEntry{
			def:    def,
			ts:     ts,
			reader: reader,
		})
	}

	return s, nil
}

// --- source.Source interface ---

// StartRecording returns a channel that closes immediately, signaling to the
// controller that the source is ready. It also starts feeding all tracks
// concurrently in background goroutines. EndRecording fires when all feeders
// have finished and sent EOS.
func (s *TestSource) StartRecording() <-chan struct{} {
	s.startedAt = time.Now().UnixNano()

	// Launch feeders in a goroutine — they will wait for the pipeline to
	// reach PLAYING state before pushing any data, so StartRecording can
	// return immediately to unblock the controller's Run loop.
	go s.feedAll()

	ch := make(chan struct{})
	close(ch)
	return ch
}

func (s *TestSource) feedAll() {
	// Wait for the pipeline to reach PAUSED before pushing data.
	// The audiotestsrc (is-live=true) prerolls the pipeline.
	<-s.callbacks.PipelinePaused()

	var wg sync.WaitGroup
	for i := range s.tracks {
		te := &s.tracks[i]

		feeder, err := NewTrackFeeder(TrackFeederParams{
			AppSrc:      te.ts.AppSrc,
			MimeType:    te.def.MimeType,
			PayloadType: te.def.PayloadType,
			SSRC:        uint32(1000 + i),
			Reader:      te.reader,
		})
		if err != nil {
			logger.Errorw("failed to create feeder", err, "trackID", te.def.TrackID)
			continue
		}
		s.feeders = append(s.feeders, feeder)

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := feeder.Feed(); err != nil {
				logger.Errorw("feeder error", err, "trackID", te.def.TrackID)
			}
		}()
	}

	wg.Wait()
	logger.Infow("all test feeders finished, EOS sent on all appsrc elements")
	s.endRecording.Break()
}

func (s *TestSource) EndRecording() <-chan struct{} {
	return s.endRecording.Watch()
}

func (s *TestSource) GetStartedAt() int64 {
	return s.startedAt
}

func (s *TestSource) GetEndedAt() int64 {
	return time.Now().UnixNano()
}

func (s *TestSource) Close() {
	s.closeReaders()
}

// --- source.TimeAware interface ---

func (s *TestSource) SetTimeProvider(_ gstreamer.TimeProvider) {
	// No-op. Non-live pipeline doesn't need time feedback.
}

// SetCallbacks sets the pipeline callbacks. Must be called before the pipeline
// starts (before Run). The controller exposes callbacks via Callbacks().
func (s *TestSource) SetCallbacks(cb *gstreamer.Callbacks) {
	s.callbacks = cb
}

// --- helpers ---

func (s *TestSource) closeReaders() {
	for _, te := range s.tracks {
		if te.reader != nil {
			te.reader.Close()
		}
	}
}

func openReader(def TrackDef) (FrameReader, error) {
	switch def.MimeType {
	case types.MimeTypeH264:
		return NewH264Reader(def.Path, def.FPS)
	case types.MimeTypeVP8, types.MimeTypeVP9:
		return NewIVFReader(def.Path)
	case types.MimeTypeOpus:
		return NewOGGReader(def.Path)
	default:
		return nil, fmt.Errorf("no reader for %s", def.MimeType)
	}
}

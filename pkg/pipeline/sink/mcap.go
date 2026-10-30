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

package sink

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/linkdata/deadlock"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/mcap"
	"github.com/livekit/egress/pkg/mcap/foxglove"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type MCAPSink struct {
	*base
	*config.FileConfig
	*uploader.Uploader

	conf     *config.PipelineConfig
	pipeline *gstreamer.Pipeline
	mcapBin  *builder.MCAPBin

	mu       deadlock.Mutex
	file     *os.File
	writer   *mcap.Writer
	anchor   time.Time
	topics   map[string]string // live topic -> owning track ID
	channels map[string]string // track ID -> topic
}

func newMCAPSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	o *config.FileConfig,
	monitor *stats.HandlerMonitor,
) (*MCAPSink, error) {
	u, err := uploader.New(o.StorageConfig, conf.BackupConfig, monitor, conf.StorageObserver, conf.Info)
	if err != nil {
		return nil, err
	}

	s := &MCAPSink{
		FileConfig: o,
		Uploader:   u,
		conf:       conf,
		pipeline:   p,
		topics:     make(map[string]string),
		channels:   make(map[string]string),
	}
	if s.mcapBin, err = builder.BuildMCAPBin(p, conf, s.writeSample, s.trackEnded); err != nil {
		return nil, err
	}
	s.base = &base{bin: s.mcapBin.Bin}
	return s, nil
}

func (s *MCAPSink) Start() error {
	f, err := os.Create(s.LocalFilepath)
	if err != nil {
		return errors.MarkDestinationError(err)
	}
	w, err := mcap.NewWriter(f, map[string]string{
		"egress_id": s.conf.Info.EgressId,
		"room_id":   s.conf.Info.RoomId,
		"room_name": s.conf.Info.RoomName,
	})
	if err != nil {
		_ = f.Close()
		return errors.MarkDestinationError(err)
	}

	s.mu.Lock()
	s.file = f
	s.writer = w
	s.mu.Unlock()
	return nil
}

func (s *MCAPSink) writeSample(ts *config.TrackSource, runningTime time.Duration, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.anchor.IsZero() {
		// samples carry pipeline running time; one shared anchor keeps every channel on the same clock
		if now, ok := s.pipeline.RunningTime(); ok {
			s.anchor = time.Now().Add(-now)
		} else {
			s.anchor = time.Now().Add(-runningTime)
		}
	}

	topic, registered := s.channels[ts.TrackID]
	if !registered {
		topic = s.allocateTopic(ts)
	}
	if _, owned := s.topics[topic]; !owned {
		s.topics[topic] = ts.TrackID
	}

	t := s.anchor.Add(runningTime)
	var msg proto.Message
	if ts.TrackKind == lksdk.TrackKindVideo {
		msg = &foxglove.CompressedVideo{
			Timestamp: timestamppb.New(t),
			FrameId:   strings.TrimPrefix(topic, "/"),
			Data:      data,
			Format:    strings.TrimPrefix(string(ts.MimeType), "video/"),
		}
	} else {
		msg = &foxglove.CompressedAudio{
			Timestamp: timestamppb.New(t),
			Data:      data,
			Format:    "opus",
		}
	}

	if !registered {
		if err := s.writer.AddChannel(ts.TrackID, topic, msg, map[string]string{
			"livekit.track_id":             ts.TrackID,
			"livekit.track_name":           ts.TrackName,
			"livekit.participant_identity": ts.ParticipantIdentity,
			"livekit.source":               strings.ToLower(ts.PublicationSource.String()),
			"livekit.input_codec":          string(ts.MimeType),
		}); err != nil {
			return errors.MarkDestinationError(err)
		}
		s.channels[ts.TrackID] = topic
	}
	if err := s.writer.Write(ts.TrackID, t, msg); err != nil {
		return errors.MarkDestinationError(err)
	}
	return nil
}

func (s *MCAPSink) allocateTopic(ts *config.TrackSource) string {
	name := ts.TrackName
	if name == "" {
		name = ts.TrackID
	}
	topic := fmt.Sprintf("/livekit/%s/%s", sanitizeTopicSegment(ts.ParticipantIdentity), sanitizeTopicSegment(name))
	if owner, ok := s.topics[topic]; ok && owner != ts.TrackID {
		topic = fmt.Sprintf("%s_%s", topic, sanitizeTopicSegment(ts.TrackID))
	}
	return topic
}

func sanitizeTopicSegment(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}

func (s *MCAPSink) trackEnded(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if topic, ok := s.channels[trackID]; ok && s.topics[topic] == trackID {
		delete(s.topics, topic)
	}
}

func (s *MCAPSink) UploadManifest(filepath string) (string, bool, error) {
	if s.DisableManifest && !s.conf.Info.BackupStorageUsed {
		return "", false, nil
	}

	storagePath := path.Join(path.Dir(s.StorageFilepath), path.Base(filepath))
	location, _, err := s.Upload(filepath, storagePath, types.OutputTypeJSON, false)
	if err != nil {
		return "", false, err
	}
	return location, true, nil
}

func (s *MCAPSink) Close() error {
	s.mcapBin.Close()

	s.mu.Lock()
	err := s.writer.Close()
	if closeErr := s.file.Close(); err == nil {
		err = closeErr
	}
	s.mu.Unlock()
	if err != nil {
		return errors.MarkDestinationError(err)
	}

	location, size, err := s.Upload(s.LocalFilepath, s.StorageFilepath, s.OutputType, false)
	if err != nil {
		logger.Debugw("mcap upload failed", err)
		return err
	}
	s.FileInfo.Location = location
	s.FileInfo.Size = size
	if s.conf.Manifest != nil {
		s.conf.Manifest.AddFile(s.StorageFilepath, location)
	}
	return nil
}

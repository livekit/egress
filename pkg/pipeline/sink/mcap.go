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

	conf      *config.PipelineConfig
	startedAt func() int64
	mcapBin   *builder.MCAPBin

	mu     deadlock.Mutex
	file   *os.File
	writer *mcap.Writer
	anchor time.Time
	topics map[string]string   // track ID -> topic
	owners map[string]string   // topic -> live track ID
	opened map[string]struct{} // channel keys registered with the writer
}

func newMCAPSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	o *config.FileConfig,
	monitor *stats.HandlerMonitor,
	startedAt func() int64,
) (*MCAPSink, error) {
	u, err := uploader.New(o.StorageConfig, conf.BackupConfig, monitor, conf.StorageObserver, conf.Info)
	if err != nil {
		return nil, err
	}

	s := &MCAPSink{
		FileConfig: o,
		Uploader:   u,
		conf:       conf,
		startedAt:  startedAt,
		topics:     make(map[string]string),
		owners:     make(map[string]string),
		opened:     make(map[string]struct{}),
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
		"egress_id":   s.conf.Info.EgressId,
		"room_id":     s.conf.Info.RoomId,
		"room_name":   s.conf.Info.RoomName,
		"source_type": "media_source",
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
		// running time zero is the synchronizer start, the same instant as FileInfo.StartedAt
		s.anchor = time.Unix(0, s.startedAt())
	}

	topic, ok := s.topics[ts.TrackID]
	if !ok {
		topic = s.allocateTopic(ts)
		s.topics[ts.TrackID] = topic
		s.owners[topic] = ts.TrackID
	}

	t := s.anchor.Add(runningTime)
	var msg proto.Message
	var format string
	if ts.TrackKind == lksdk.TrackKindVideo {
		format = strings.TrimPrefix(string(ts.MimeType), "video/")
		msg = &foxglove.CompressedVideo{
			Timestamp: timestamppb.New(t),
			FrameId:   strings.TrimPrefix(topic, "/"),
			Data:      data,
			Format:    format,
		}
	} else {
		format = "opus"
		msg = &foxglove.CompressedAudio{
			Timestamp: timestamppb.New(t),
			Data:      data,
			Format:    format,
		}
	}

	// a republish with the same codec continues its channel; a codec change opens a new one on the same topic
	key := topic + " " + format
	if _, ok := s.opened[key]; !ok {
		if err := s.writer.AddChannel(key, topic, msg, map[string]string{
			"codec":                        format,
			"livekit.track_id":             ts.TrackID,
			"livekit.track_name":           ts.TrackName,
			"livekit.participant_identity": ts.ParticipantIdentity,
			"livekit.source":               strings.ToLower(ts.PublicationSource.String()),
			"livekit.input_codec":          string(ts.MimeType),
		}); err != nil {
			return errors.MarkDestinationError(err)
		}
		s.opened[key] = struct{}{}
	}
	if err := s.writer.Write(key, t, msg); err != nil {
		return errors.MarkDestinationError(err)
	}
	return nil
}

func (s *MCAPSink) allocateTopic(ts *config.TrackSource) string {
	name := ts.TrackName
	if name == "" {
		name = ts.TrackID
	}
	base := fmt.Sprintf("/livekit/%s/%s", sanitizeTopicSegment(ts.ParticipantIdentity), sanitizeTopicSegment(name))
	topic := base
	for i := 1; ; i++ {
		if _, ok := s.owners[topic]; !ok {
			return topic
		}
		// another live track's name can equal a fallback, so every candidate is checked
		topic = fmt.Sprintf("%s_%s", base, sanitizeTopicSegment(ts.TrackID))
		if i > 1 {
			topic = fmt.Sprintf("%s_%d", topic, i)
		}
	}
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

	if topic, ok := s.topics[trackID]; ok {
		delete(s.topics, trackID)
		if s.owners[topic] == trackID {
			delete(s.owners, topic)
		}
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

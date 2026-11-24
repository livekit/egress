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
	"github.com/livekit/server-sdk-go/v2/datatrack"
)

type MCAPSink struct {
	*base
	*config.FileConfig
	*uploader.Uploader

	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks
	startedAt func() int64
	mcapBin   *builder.MCAPBin

	mu     deadlock.Mutex
	file   *os.File
	writer *mcap.Writer
	anchor time.Time
	topics map[string]string      // track ID -> topic
	owners map[string]string      // topic -> live track ID
	opened map[string]mcapChannel // channel key -> channel registered with the writer
	events mcapEventLog
}

func newMCAPSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	o *config.FileConfig,
	callbacks *gstreamer.Callbacks,
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
		callbacks:  callbacks,
		startedAt:  startedAt,
		topics:     make(map[string]string),
		owners:     make(map[string]string),
		opened:     make(map[string]mcapChannel),
		events:     newMCAPEventLog(),
	}
	if s.mcapBin, err = builder.BuildMCAPBin(p, conf, s.writeSample, s.trackRemoved, s.trackEnded); err != nil {
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
	s.events.writer = w
	s.mu.Unlock()

	// registered unlocked: the backlog replays synchronously into handleRoomEvent, which takes s.mu
	s.callbacks.AddOnRoomEvent(s.handleRoomEvent)
	s.callbacks.SetOnDataFrame(s.writeData)
	s.callbacks.SetOnDataTrackEnded(s.trackEnded)
	return nil
}

func (s *MCAPSink) handleRoomEvent(ev types.RoomEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events.handle(ev)
}

func (s *MCAPSink) writeSample(ts *config.TrackSource, runningTime time.Duration, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.anchor.IsZero() {
		// running time zero is the synchronizer start, the same instant as FileInfo.StartedAt
		s.anchor = time.Unix(0, s.startedAt())
	}

	topic := s.topicFor(ts.TrackID, ts.TrackName, ts.ParticipantIdentity)
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
	ch, ok := s.opened[key]
	if !ok {
		id, err := s.writer.AddChannel(key, topic, msg, map[string]string{
			"codec":                        format,
			"livekit.track_id":             ts.TrackID,
			"livekit.track_name":           ts.TrackName,
			"livekit.participant_identity": ts.ParticipantIdentity,
			"livekit.source":               strings.ToLower(ts.PublicationSource.String()),
			"livekit.input_codec":          string(ts.MimeType),
		})
		if err != nil {
			return errors.MarkDestinationError(err)
		}
		ch = mcapChannel{id: id, topic: topic, schema: string(msg.ProtoReflect().Descriptor().FullName()), format: format}
		s.opened[key] = ch
	}
	s.events.trackOpened(ts.TrackID, ch)
	if err := s.writer.Write(key, t, msg); err != nil {
		return errors.MarkDestinationError(err)
	}
	return nil
}

func (s *MCAPSink) writeData(df types.DataFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.anchor.IsZero() {
		startedAt := s.startedAt()
		if startedAt == 0 {
			return nil
		}
		s.anchor = time.Unix(0, startedAt)
	}
	if df.Time.Before(s.anchor) {
		return nil
	}

	trackID := string(df.Info.SID)
	topic := s.topicFor(trackID, df.Info.Name, df.ParticipantIdentity)
	messageEncoding, schema := mcapDataEncoding(df.Info)

	// a republish with the same encoding and schema continues its channel
	key := topic + " data " + messageEncoding
	if schema != nil {
		key += " " + schema.Name + "/" + schema.Encoding
	}
	if _, ok := s.opened[key]; !ok {
		id, err := s.writer.AddRawChannel(key, topic, messageEncoding, schema, map[string]string{
			"livekit.kind":                 "data",
			"livekit.track_id":             trackID,
			"livekit.track_name":           df.Info.Name,
			"livekit.participant_identity": df.ParticipantIdentity,
		})
		if err != nil {
			return errors.MarkDestinationError(err)
		}
		s.opened[key] = mcapChannel{id: id, topic: topic}
	}

	publishTime := df.Time
	if df.Frame.UserTimestamp != nil {
		publishTime = time.UnixMilli(int64(*df.Frame.UserTimestamp))
	}
	if err := s.writer.WriteRaw(key, df.Time, publishTime, df.Frame.Payload); err != nil {
		return errors.MarkDestinationError(err)
	}
	return nil
}

// mcapDataEncoding maps a data track's declared encodings onto MCAP's, whose registry names they share
func mcapDataEncoding(info datatrack.Info) (string, *mcap.Schema) {
	if info.FrameEncoding == nil || info.FrameEncoding == datatrack.FrameEncodingOther {
		return "", nil
	}
	messageEncoding := info.FrameEncoding.String()
	if info.Schema == nil || info.Schema.Encoding == nil || info.Schema.Encoding == datatrack.SchemaEncodingOther {
		return messageEncoding, nil
	}
	// the definition lives in a room data blob, which the SDK has no API to fetch yet
	return messageEncoding, &mcap.Schema{Name: info.Schema.Name, Encoding: info.Schema.Encoding.String()}
}

func (s *MCAPSink) topicFor(trackID, trackName, identity string) string {
	topic, ok := s.topics[trackID]
	if !ok {
		topic = s.allocateTopic(trackID, trackName, identity)
		s.topics[trackID] = topic
		s.owners[topic] = trackID
	}
	return topic
}

func (s *MCAPSink) allocateTopic(trackID, trackName, participantIdentity string) string {
	name := trackName
	if name == "" {
		name = trackID
	}
	identity := sanitizeTopicSegment(participantIdentity)
	if identity == "events" {
		logger.Warnw("participant identity collides with reserved mcap topic", nil, "identity", participantIdentity)
		identity = "events_"
	}
	base := fmt.Sprintf("/livekit/%s/%s", identity, sanitizeTopicSegment(name))
	topic := base
	for i := 1; ; i++ {
		if _, ok := s.owners[topic]; !ok {
			return topic
		}
		// another live track's name can equal a fallback, so every candidate is checked
		topic = fmt.Sprintf("%s_%s", base, sanitizeTopicSegment(trackID))
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

// trackRemoved frees the topic for a republish while the removed track's last samples keep their channel
func (s *MCAPSink) trackRemoved(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.releaseTopic(trackID)
}

func (s *MCAPSink) trackEnded(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.releaseTopic(trackID)
	delete(s.topics, trackID)
}

func (s *MCAPSink) releaseTopic(trackID string) {
	if topic, ok := s.topics[trackID]; ok && s.owners[topic] == trackID {
		delete(s.owners, topic)
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
	s.events.close()
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

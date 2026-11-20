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
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/livekit/egress/pkg/mcap"
	"github.com/livekit/egress/pkg/mcap/foxglove"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const eventsTopic = "/livekit/events"

type mcapChannel struct {
	id                    uint16
	topic, schema, format string
}

// mcapEventLog writes room events to the events channel; callers hold MCAPSink.mu
type mcapEventLog struct {
	writer   *mcap.Writer
	opened   bool
	closed   bool
	pending  map[string]types.RoomEvent // track SID -> track_published held until its channel opens
	channels map[string]mcapChannel     // track SID -> channel its first sample landed on
}

func newMCAPEventLog() mcapEventLog {
	return mcapEventLog{
		pending:  make(map[string]types.RoomEvent),
		channels: make(map[string]mcapChannel),
	}
}

func (l *mcapEventLog) handle(ev types.RoomEvent) {
	if l.closed {
		return
	}

	switch ev.Type {
	case types.RoomEventTrackPublished:
		if ch, ok := l.channels[ev.Track.Sid]; ok {
			l.write(ev, &ch)
		} else {
			l.pending[ev.Track.Sid] = ev
		}
	case types.RoomEventTrackUnpublished:
		if held, ok := l.pending[ev.Track.Sid]; ok {
			delete(l.pending, ev.Track.Sid)
			l.write(held, nil)
		}
		l.write(ev, nil)
	default:
		l.write(ev, nil)
	}
}

func (l *mcapEventLog) trackOpened(trackSID string, ch mcapChannel) {
	if _, ok := l.channels[trackSID]; ok {
		return
	}
	l.channels[trackSID] = ch
	if ev, ok := l.pending[trackSID]; ok {
		delete(l.pending, trackSID)
		l.write(ev, &ch)
	}
}

func (l *mcapEventLog) close() {
	held := make([]types.RoomEvent, 0, len(l.pending))
	for _, ev := range l.pending {
		held = append(held, ev)
	}
	sort.Slice(held, func(i, j int) bool { return held[i].Time.Before(held[j].Time) })
	for _, ev := range held {
		l.write(ev, nil)
	}
	l.pending = nil
	l.closed = true
}

// write logs failures instead of returning them: events are context, and losing one must not fail the recording
func (l *mcapEventLog) write(ev types.RoomEvent, ch *mcapChannel) {
	if !l.opened {
		if _, err := l.writer.AddChannel(eventsTopic, eventsTopic, &foxglove.Event{}, nil); err != nil {
			logger.Warnw("failed to open mcap events channel", err)
			return
		}
		l.opened = true
	}

	ts := timestamppb.New(ev.Time)
	if err := l.writer.Write(eventsTopic, ev.Time, &foxglove.Event{
		StartTime: ts,
		EndTime:   ts,
		Metadata:  eventMetadata(ev, ch),
	}); err != nil {
		logger.Warnw("failed to write mcap room event", err, "event", ev.Type)
	}
}

func eventMetadata(ev types.RoomEvent, ch *mcapChannel) []*foxglove.KeyValuePair {
	md := []*foxglove.KeyValuePair{
		kv("event_type", string(ev.Type)),
		kv("participant_identity", ev.ParticipantIdentity),
		kv("participant_sid", ev.ParticipantSID),
	}

	switch ev.Type {
	case types.RoomEventParticipantJoined, types.RoomEventParticipantLeft:
		md = append(md, kv("participant_name", ev.ParticipantName))
		if ev.Type == types.RoomEventParticipantLeft && ev.DisconnectReason != livekit.DisconnectReason_UNKNOWN_REASON {
			md = append(md, kv("reason", strings.ToLower(ev.DisconnectReason.String())))
		}
		return md
	}

	ti := ev.Track
	md = append(md, kv("track_sid", ti.Sid), kv("track_name", ti.Name))
	if ev.Type != types.RoomEventTrackPublished {
		return md
	}

	md = append(md,
		kv("track_kind", strings.ToLower(ti.Type.String())),
		kv("track_source", strings.ToLower(ti.Source.String())),
		kv("input_codec", strings.ToLower(inputMimeType(ti))),
	)
	if ti.Type == livekit.TrackType_VIDEO && ti.Width != 0 && ti.Height != 0 {
		md = append(md,
			kv("width", strconv.FormatUint(uint64(ti.Width), 10)),
			kv("height", strconv.FormatUint(uint64(ti.Height), 10)),
		)
	}
	if ch != nil {
		md = append(md,
			kv("output_format", ch.format),
			kv("mcap_schema_name", ch.schema),
			kv("mcap_channel_id", strconv.FormatUint(uint64(ch.id), 10)),
			kv("mcap_channel_topic", ch.topic),
		)
	}
	return md
}

// before the track is live only its codecs carry a mime type
func inputMimeType(ti *livekit.TrackInfo) string {
	if ti.MimeType != "" {
		return ti.MimeType
	}
	for _, c := range ti.Codecs {
		if c.MimeType != "" {
			return c.MimeType
		}
	}
	return ""
}

func kv(key, value string) *foxglove.KeyValuePair {
	return &foxglove.KeyValuePair{Key: key, Value: value}
}

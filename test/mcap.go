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

//go:build integration

package test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/foxglove/mcap/go/mcap"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/mcap/foxglove"
	"github.com/livekit/protocol/livekit"
)

func verifyMCAP(t *testing.T, tc *testCase, localPath string, fileRes *livekit.FileInfo) {
	f, err := os.Open(localPath)
	require.NoError(t, err)
	defer f.Close()

	reader, err := mcap.NewReader(f)
	require.NoError(t, err)
	info, err := reader.Info()
	require.NoError(t, err)
	require.NotNil(t, info.Statistics, "mcap summary missing")

	var fileMetadata *mcap.Metadata
	for _, idx := range info.MetadataIndexes {
		if idx.Name == "livekit" {
			fileMetadata, err = reader.GetMetadata(idx.Offset)
			require.NoError(t, err)
		}
	}
	require.NotNil(t, fileMetadata, "livekit file metadata missing")
	require.Equal(t, "media_source", fileMetadata.Metadata["source_type"])

	for _, schema := range info.Schemas {
		require.Contains(t, []string{"foxglove.CompressedVideo", "foxglove.CompressedAudio", "foxglove.Event"}, schema.Name)
	}

	channelsByTopic := make(map[string]int)
	var eventsChannel *mcap.Channel
	for id, channel := range info.Channels {
		require.Greater(t, info.Statistics.ChannelMessageCounts[id], uint64(0), "no messages on %s", channel.Topic)
		if channel.Topic == "/livekit/events" {
			require.Nil(t, eventsChannel, "events split across channels")
			require.Equal(t, "foxglove.Event", info.Schemas[channel.SchemaID].Name)
			eventsChannel = channel
			continue
		}
		channelsByTopic[channel.Topic]++
	}
	require.NotNil(t, eventsChannel, "no /livekit/events channel")
	require.Len(t, channelsByTopic, tc.mcapChannels)
	for topic, count := range channelsByTopic {
		require.Equal(t, 1, count, "topic %s split across channels", topic)
	}
	if tc.mcapExcluded != "" {
		excluded := tc.publishers[tc.mcapExcluded].identity
		for topic := range channelsByTopic {
			require.NotContains(t, topic, excluded)
		}
	}

	it, err := reader.Messages(mcap.UsingIndex(true), mcap.InOrder(mcap.LogTimeOrder))
	require.NoError(t, err)
	var first, last, prev uint64
	var events []*foxglove.Event
	sawFirst := make(map[uint16]bool)
	for {
		schema, channel, msg, err := it.NextInto(nil)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		require.GreaterOrEqual(t, msg.LogTime, prev)
		prev = msg.LogTime

		if channel.ID == eventsChannel.ID {
			ev := &foxglove.Event{}
			require.NoError(t, proto.Unmarshal(msg.Data, ev))
			events = append(events, ev)
			continue
		}

		if first == 0 {
			first = msg.LogTime
		}
		last = msg.LogTime

		if sawFirst[channel.ID] {
			continue
		}
		sawFirst[channel.ID] = true
		if schema.Name != "foxglove.CompressedVideo" {
			require.Equal(t, "opus", channel.Metadata["codec"])
			continue
		}
		video := &foxglove.CompressedVideo{}
		require.NoError(t, proto.Unmarshal(msg.Data, video))
		require.Equal(t, channel.Metadata["livekit.input_codec"], "video/"+video.Format)
		require.Equal(t, channel.Metadata["codec"], video.Format)
		switch video.Format {
		case "h264":
			require.True(t, hasNALType(video.Data, 7), "first frame on %s has no SPS", channel.Topic)
			require.True(t, hasNALType(video.Data, 5), "first frame on %s is not an IDR", channel.Topic)
		case "av1":
			require.True(t, hasOBUType(video.Data, 1), "first frame on %s has no sequence header", channel.Topic)
		}
	}
	require.InDelta(t, time.Duration(fileRes.Duration).Seconds(), time.Duration(last-first).Seconds(), 2.0)

	publishers := make([]string, 0, len(tc.publishers))
	for _, p := range tc.publishers {
		publishers = append(publishers, p.identity)
	}
	require.NotEmpty(t, events)
	require.Equal(t, "participant_joined", eventValue(events[0], "event_type"))
	require.Contains(t, publishers, eventValue(events[0], "participant_identity"))

	publishedTopics := make(map[string]bool)
	for _, ev := range events {
		require.Equal(t, "event_type", ev.Metadata[0].Key)
		if eventValue(ev, "event_type") == "track_published" {
			publishedTopics[eventValue(ev, "mcap_channel_topic")] = true
		}
	}
	for topic := range channelsByTopic {
		require.True(t, publishedTopics[topic], "no track_published event for %s", topic)
	}
}

func eventValue(ev *foxglove.Event, key string) string {
	for _, kv := range ev.Metadata {
		if kv.Key == key {
			return kv.Value
		}
	}
	return ""
}

func hasNALType(annexB []byte, nalType byte) bool {
	for _, nal := range bytes.Split(annexB, []byte{0, 0, 1}) {
		if len(nal) > 0 && nal[0]&0x1f == nalType {
			return true
		}
	}
	return false
}

func hasOBUType(obus []byte, obuType byte) bool {
	for len(obus) > 0 {
		header := obus[0]
		if (header>>3)&0xf == obuType {
			return true
		}
		offset := 1
		if header&0x04 != 0 {
			offset++
		}
		if header&0x02 == 0 {
			return false
		}
		var size uint64
		for i := 0; i < 8 && offset < len(obus); i++ {
			b := obus[offset]
			offset++
			size |= uint64(b&0x7f) << (7 * i)
			if b&0x80 == 0 {
				break
			}
		}
		if uint64(len(obus)-offset) < size {
			return false
		}
		obus = obus[offset+int(size):]
	}
	return false
}

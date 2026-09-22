// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcap

import (
	"bytes"
	"io"
	"testing"
	"time"

	mcapgo "github.com/foxglove/mcap/go/mcap"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestWriterProducesIndexedFoxgloveMCAP(t *testing.T) {
	var dst bytes.Buffer
	start := time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC)
	w, err := NewWriter(&dst, Options{
		Audio:     true,
		Video:     true,
		StartTime: start,
		Metadata:  map[string]string{"egress_id": "EG_test"},
	})
	require.NoError(t, err)
	require.NoError(t, w.WriteVideo(40*time.Millisecond, "camera", []byte{0, 0, 0, 1, 0x67}))
	require.NoError(t, w.WriteAudio(20*time.Millisecond, []byte{0xf8, 0xff}))
	require.NoError(t, w.Close())

	r, err := mcapgo.NewReader(bytes.NewReader(dst.Bytes()))
	require.NoError(t, err)
	require.Contains(t, r.Header().Library, "livekit-egress")

	info, err := r.Info()
	require.NoError(t, err)
	require.True(t, info.CanReadMessagesUsingIndex())
	require.Len(t, info.Channels, 2)
	require.Equal(t, uint64(2), info.Statistics.MessageCount)
	require.Equal(t, "foxglove.CompressedVideo", info.Schemas[videoSchemaID].Name)
	require.Equal(t, "foxglove.CompressedAudio", info.Schemas[audioSchemaID].Name)

	var descriptors descriptorpb.FileDescriptorSet
	require.NoError(t, proto.Unmarshal(info.Schemas[videoSchemaID].Data, &descriptors))
	require.Len(t, descriptors.File, 3)

	// The writer must preserve media-time order in the physical file even when
	// the independent appsink callbacks arrive out of order.
	it, err := r.Messages(mcapgo.InOrder(mcapgo.FileOrder))
	require.NoError(t, err)

	schema, channel, message, err := it.NextInto(nil)
	require.NoError(t, err)
	require.Equal(t, "foxglove.CompressedAudio", schema.Name)
	require.Equal(t, defaultAudioTopic, channel.Topic)
	require.Equal(t, uint64(start.Add(20*time.Millisecond).UnixNano()), message.LogTime)
	require.Equal(t, []byte{0xf8, 0xff}, protobufBytesField(t, message.Data, 2))
	require.Equal(t, "opus", string(protobufBytesField(t, message.Data, 3)))

	schema, channel, message, err = it.NextInto(nil)
	require.NoError(t, err)
	require.Equal(t, "foxglove.CompressedVideo", schema.Name)
	require.Equal(t, defaultVideoTopic, channel.Topic)
	require.Equal(t, uint64(start.Add(40*time.Millisecond).UnixNano()), message.LogTime)
	require.Equal(t, "camera", string(protobufBytesField(t, message.Data, 2)))
	require.Equal(t, []byte{0, 0, 0, 1, 0x67}, protobufBytesField(t, message.Data, 3))
	require.Equal(t, "h264", string(protobufBytesField(t, message.Data, 4)))

	_, _, _, err = it.NextInto(nil)
	require.ErrorIs(t, err, io.EOF)
}

func TestWriterRequiresAChannel(t *testing.T) {
	_, err := NewWriter(io.Discard, Options{})
	require.Error(t, err)
}

func protobufBytesField(t *testing.T, message []byte, wanted protowire.Number) []byte {
	t.Helper()
	for len(message) > 0 {
		number, wireType, n := protowire.ConsumeTag(message)
		require.Greater(t, n, 0)
		message = message[n:]
		if wireType == protowire.BytesType {
			value, consumed := protowire.ConsumeBytes(message)
			require.Greater(t, consumed, 0)
			if number == wanted {
				return value
			}
			message = message[consumed:]
			continue
		}
		consumed := protowire.ConsumeFieldValue(number, wireType, message)
		require.Greater(t, consumed, 0)
		message = message[consumed:]
	}
	t.Fatalf("protobuf field %d not found", wanted)
	return nil
}

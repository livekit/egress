// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package mcap writes synchronized egress media as Foxglove-compatible MCAP.
package mcap

import (
	"container/heap"
	"fmt"
	"io"
	"sync"
	"time"

	mcapgo "github.com/foxglove/mcap/go/mcap"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	videoSchemaID  uint16 = 1
	audioSchemaID  uint16 = 2
	videoChannelID uint16 = 1
	audioChannelID uint16 = 2

	defaultVideoTopic = "/video"
	defaultAudioTopic = "/audio"

	// GStreamer encoder branches can deliver their first samples at different
	// times. Keep a bounded amount of media so MCAP records are physically
	// ordered by media time without buffering the whole recording.
	reorderWindow = 3 * time.Second
)

type queuedMessage struct {
	timestamp time.Time
	order     uint64
	message   *mcapgo.Message
}

type messageHeap []*queuedMessage

func (h messageHeap) Len() int { return len(h) }
func (h messageHeap) Less(i, j int) bool {
	if h[i].timestamp.Equal(h[j].timestamp) {
		return h[i].order < h[j].order
	}
	return h[i].timestamp.Before(h[j].timestamp)
}
func (h messageHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *messageHeap) Push(value any) { *h = append(*h, value.(*queuedMessage)) }
func (h *messageHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

type Options struct {
	Video      bool
	Audio      bool
	VideoTopic string
	AudioTopic string
	Metadata   map[string]string
	// StartTime maps media PTS zero to wall-clock time. If zero, it is derived
	// from the first sample as time.Now() - PTS.
	StartTime time.Time
}

// Writer serializes writes because GStreamer audio and video appsinks invoke
// their callbacks on different streaming threads.
type Writer struct {
	mu sync.Mutex

	w         *mcapgo.Writer
	startTime time.Time
	videoSeq  uint32
	audioSeq  uint32
	queue     messageHeap
	maxTime   time.Time
	order     uint64
	closed    bool
}

func NewWriter(dst io.Writer, opts Options) (*Writer, error) {
	if !opts.Video && !opts.Audio {
		return nil, fmt.Errorf("at least one MCAP media channel is required")
	}

	w, err := mcapgo.NewWriter(dst, &mcapgo.WriterOptions{
		IncludeCRC:  true,
		Chunked:     true,
		ChunkSize:   4 * 1024 * 1024,
		Compression: mcapgo.CompressionNone,
	})
	if err != nil {
		return nil, err
	}
	if err = w.WriteHeader(&mcapgo.Header{Library: "livekit-egress"}); err != nil {
		return nil, err
	}

	descriptors, err := foxgloveDescriptorSet()
	if err != nil {
		return nil, err
	}
	if opts.Video {
		if opts.VideoTopic == "" {
			opts.VideoTopic = defaultVideoTopic
		}
		if err = w.WriteSchema(&mcapgo.Schema{
			ID: videoSchemaID, Name: "foxglove.CompressedVideo", Encoding: "protobuf", Data: descriptors,
		}); err != nil {
			return nil, err
		}
		if err = w.WriteChannel(&mcapgo.Channel{
			ID: videoChannelID, SchemaID: videoSchemaID, Topic: opts.VideoTopic, MessageEncoding: "protobuf",
			Metadata: map[string]string{"codec": "h264"},
		}); err != nil {
			return nil, err
		}
	}
	if opts.Audio {
		if opts.AudioTopic == "" {
			opts.AudioTopic = defaultAudioTopic
		}
		if err = w.WriteSchema(&mcapgo.Schema{
			ID: audioSchemaID, Name: "foxglove.CompressedAudio", Encoding: "protobuf", Data: descriptors,
		}); err != nil {
			return nil, err
		}
		if err = w.WriteChannel(&mcapgo.Channel{
			ID: audioChannelID, SchemaID: audioSchemaID, Topic: opts.AudioTopic, MessageEncoding: "protobuf",
			Metadata: map[string]string{"codec": "opus"},
		}); err != nil {
			return nil, err
		}
	}
	if len(opts.Metadata) > 0 {
		if err = w.WriteMetadata(&mcapgo.Metadata{Name: "livekit", Metadata: opts.Metadata}); err != nil {
			return nil, err
		}
	}

	return &Writer{w: w, startTime: opts.StartTime}, nil
}

func (w *Writer) WriteVideo(pts time.Duration, frameID string, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return fmt.Errorf("MCAP writer is closed")
	}
	ts := w.timestampLocked(pts)
	msg := encodeCompressedVideo(ts, frameID, data)
	w.enqueueLocked(ts, &mcapgo.Message{
		ChannelID: videoChannelID, Sequence: w.videoSeq, LogTime: uint64(ts.UnixNano()),
		PublishTime: uint64(ts.UnixNano()), Data: msg,
	})
	w.videoSeq++
	return w.flushReadyLocked(w.maxTime.Add(-reorderWindow))
}

func (w *Writer) WriteAudio(pts time.Duration, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return fmt.Errorf("MCAP writer is closed")
	}
	ts := w.timestampLocked(pts)
	msg := encodeCompressedAudio(ts, data)
	w.enqueueLocked(ts, &mcapgo.Message{
		ChannelID: audioChannelID, Sequence: w.audioSeq, LogTime: uint64(ts.UnixNano()),
		PublishTime: uint64(ts.UnixNano()), Data: msg,
	})
	w.audioSeq++
	return w.flushReadyLocked(w.maxTime.Add(-reorderWindow))
}

func (w *Writer) enqueueLocked(timestamp time.Time, message *mcapgo.Message) {
	heap.Push(&w.queue, &queuedMessage{timestamp: timestamp, order: w.order, message: message})
	w.order++
	if timestamp.After(w.maxTime) {
		w.maxTime = timestamp
	}
}

func (w *Writer) flushReadyLocked(cutoff time.Time) error {
	for w.queue.Len() > 0 && !w.queue[0].timestamp.After(cutoff) {
		if err := w.flushNextLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) flushNextLocked() error {
	queued := heap.Pop(&w.queue).(*queuedMessage)
	if err := w.w.WriteMessage(queued.message); err != nil {
		heap.Push(&w.queue, queued)
		return err
	}
	return nil
}

func (w *Writer) timestampLocked(pts time.Duration) time.Time {
	if w.startTime.IsZero() {
		w.startTime = time.Now().Add(-pts)
	}
	return w.startTime.Add(pts)
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	for w.queue.Len() > 0 {
		if err := w.flushNextLocked(); err != nil {
			return err
		}
	}
	w.closed = true
	return w.w.Close()
}

func encodeTimestamp(ts time.Time) []byte {
	var data []byte
	data = protowire.AppendTag(data, 1, protowire.VarintType)
	data = protowire.AppendVarint(data, uint64(ts.Unix()))
	if nanos := ts.Nanosecond(); nanos != 0 {
		data = protowire.AppendTag(data, 2, protowire.VarintType)
		data = protowire.AppendVarint(data, uint64(nanos))
	}
	return data
}

func encodeCompressedVideo(ts time.Time, frameID string, data []byte) []byte {
	var msg []byte
	msg = protowire.AppendTag(msg, 1, protowire.BytesType)
	msg = protowire.AppendBytes(msg, encodeTimestamp(ts))
	if frameID != "" {
		msg = protowire.AppendTag(msg, 2, protowire.BytesType)
		msg = protowire.AppendString(msg, frameID)
	}
	msg = protowire.AppendTag(msg, 3, protowire.BytesType)
	msg = protowire.AppendBytes(msg, data)
	msg = protowire.AppendTag(msg, 4, protowire.BytesType)
	msg = protowire.AppendString(msg, "h264")
	return msg
}

func encodeCompressedAudio(ts time.Time, data []byte) []byte {
	var msg []byte
	msg = protowire.AppendTag(msg, 1, protowire.BytesType)
	msg = protowire.AppendBytes(msg, encodeTimestamp(ts))
	msg = protowire.AppendTag(msg, 2, protowire.BytesType)
	msg = protowire.AppendBytes(msg, data)
	msg = protowire.AppendTag(msg, 3, protowire.BytesType)
	msg = protowire.AppendString(msg, "opus")
	return msg
}

func foxgloveDescriptorSet() ([]byte, error) {
	timestampDescriptor := protodesc.ToFileDescriptorProto((&timestamppb.Timestamp{}).ProtoReflect().Descriptor().ParentFile())
	videoDescriptor := foxgloveFileDescriptor(
		"foxglove/CompressedVideo.proto", "CompressedVideo",
		[]*descriptorpb.FieldDescriptorProto{
			messageField("timestamp", 1, ".google.protobuf.Timestamp"),
			scalarField("frame_id", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			scalarField("data", 3, descriptorpb.FieldDescriptorProto_TYPE_BYTES),
			scalarField("format", 4, descriptorpb.FieldDescriptorProto_TYPE_STRING),
		},
	)
	audioDescriptor := foxgloveFileDescriptor(
		"foxglove/CompressedAudio.proto", "CompressedAudio",
		[]*descriptorpb.FieldDescriptorProto{
			messageField("timestamp", 1, ".google.protobuf.Timestamp"),
			scalarField("data", 2, descriptorpb.FieldDescriptorProto_TYPE_BYTES),
			scalarField("format", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING),
		},
	)
	return proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		timestampDescriptor, videoDescriptor, audioDescriptor,
	}})
}

func foxgloveFileDescriptor(name, messageName string, fields []*descriptorpb.FieldDescriptorProto) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name: proto.String(name), Package: proto.String("foxglove"), Syntax: proto.String("proto3"),
		Dependency:  []string{"google/protobuf/timestamp.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String(messageName), Field: fields}},
	}
}

func messageField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	field := scalarField(name, number, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	field.TypeName = proto.String(typeName)
	return field
}

func scalarField(name string, number int32, fieldType descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name: proto.String(name), Number: proto.Int32(number), Type: fieldType.Enum(),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
	}
}

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

package mcap

import (
	"io"
	"time"

	"github.com/foxglove/mcap/go/mcap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const chunkSize = 4 << 20

// Writer writes protobuf messages to a chunked, indexed MCAP file; it is not safe for concurrent use
type Writer struct {
	w        *mcap.Writer
	schemas  map[protoreflect.FullName]uint16
	channels map[string]*channel
	nextID   uint16
}

type channel struct {
	id       uint16
	sequence uint32
}

// NewWriter writes the MCAP header and a "livekit" metadata record to dst
func NewWriter(dst io.Writer, metadata map[string]string) (*Writer, error) {
	w, err := mcap.NewWriter(dst, &mcap.WriterOptions{
		Chunked:     true,
		ChunkSize:   chunkSize,
		IncludeCRC:  true,
		Compression: mcap.CompressionNone,
	})
	if err != nil {
		return nil, err
	}
	if err = w.WriteHeader(&mcap.Header{Library: "livekit-egress"}); err != nil {
		return nil, err
	}
	if err = w.WriteMetadata(&mcap.Metadata{Name: "livekit", Metadata: metadata}); err != nil {
		return nil, err
	}
	return &Writer{
		w:        w,
		schemas:  make(map[protoreflect.FullName]uint16),
		channels: make(map[string]*channel),
		nextID:   1,
	}, nil
}

// AddChannel registers a channel under key carrying messages of msg's type, writing its schema on first use
func (w *Writer) AddChannel(key, topic string, msg proto.Message, metadata map[string]string) error {
	desc := msg.ProtoReflect().Descriptor()
	schemaID, ok := w.schemas[desc.FullName()]
	if !ok {
		data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto),
			protodesc.ToFileDescriptorProto(desc.ParentFile()),
		}})
		if err != nil {
			return err
		}
		schemaID = uint16(len(w.schemas) + 1)
		if err = w.w.WriteSchema(&mcap.Schema{ID: schemaID, Name: string(desc.FullName()), Encoding: "protobuf", Data: data}); err != nil {
			return err
		}
		w.schemas[desc.FullName()] = schemaID
	}

	c := &channel{id: w.nextID}
	if err := w.w.WriteChannel(&mcap.Channel{
		ID:              c.id,
		SchemaID:        schemaID,
		Topic:           topic,
		MessageEncoding: "protobuf",
		Metadata:        metadata,
	}); err != nil {
		return err
	}
	w.nextID++
	w.channels[key] = c
	return nil
}

// Write appends msg to the channel registered under key, logged and published at ts
func (w *Writer) Write(key string, ts time.Time, msg proto.Message) error {
	c := w.channels[key]
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	if err = w.w.WriteMessage(&mcap.Message{
		ChannelID:   c.id,
		Sequence:    c.sequence,
		LogTime:     uint64(ts.UnixNano()),
		PublishTime: uint64(ts.UnixNano()),
		Data:        data,
	}); err != nil {
		return err
	}
	c.sequence++
	return nil
}

// Close writes the summary section and footer
func (w *Writer) Close() error {
	return w.w.Close()
}

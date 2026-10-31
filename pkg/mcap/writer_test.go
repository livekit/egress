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
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/foxglove/mcap/go/mcap"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/livekit/egress/pkg/mcap/foxglove"
)

func TestWriterRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, map[string]string{"room": "test"})
	require.NoError(t, err)

	require.NoError(t, w.AddChannel("v1", "/video/a", &foxglove.CompressedVideo{}, nil))
	require.NoError(t, w.AddChannel("v2", "/video/b", &foxglove.CompressedVideo{}, nil))
	require.NoError(t, w.AddChannel("a1", "/audio/a", &foxglove.CompressedAudio{}, nil))

	start := time.Unix(1700000000, 0)
	for i := 0; i < 3; i++ {
		ts := start.Add(time.Duration(i) * 20 * time.Millisecond)
		require.NoError(t, w.Write("v1", ts, &foxglove.CompressedVideo{Timestamp: timestamppb.New(ts), Data: []byte{byte(i)}, Format: "h264"}))
		require.NoError(t, w.Write("a1", ts, &foxglove.CompressedAudio{Timestamp: timestamppb.New(ts), Data: []byte{byte(i)}, Format: "opus"}))
	}
	require.NoError(t, w.Write("v2", start, &foxglove.CompressedVideo{Data: []byte{9}, Format: "h264"}))
	require.NoError(t, w.Close())

	r, err := mcap.NewReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	info, err := r.Info()
	require.NoError(t, err)

	require.Len(t, info.Schemas, 2)
	require.Len(t, info.Channels, 3)
	require.Len(t, info.MetadataIndexes, 1)
	require.EqualValues(t, 7, info.Statistics.MessageCount)

	for _, s := range info.Schemas {
		fds := &descriptorpb.FileDescriptorSet{}
		require.NoError(t, proto.Unmarshal(s.Data, fds))
		_, err = protodesc.NewFiles(fds)
		require.NoError(t, err, s.Name)
	}

	it, err := r.Messages()
	require.NoError(t, err)
	sequences := make(map[string][]uint32)
	for {
		_, c, m, err := it.Next(nil)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		sequences[c.Topic] = append(sequences[c.Topic], m.Sequence)
	}
	require.Equal(t, map[string][]uint32{
		"/video/a": {0, 1, 2},
		"/video/b": {0},
		"/audio/a": {0, 1, 2},
	}, sequences)
}

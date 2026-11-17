// Copyright 2023 LiveKit, Inc.
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
	"encoding/binary"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
)

func box(boxType string, payload []byte) []byte {
	b := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], boxType)
	return append(b, payload...)
}

func concat(parts ...[]byte) []byte {
	var b []byte
	for _, part := range parts {
		b = append(b, part...)
	}
	return b
}

func writeSegment(t *testing.T, parts ...[]byte) string {
	t.Helper()

	filename := path.Join(t.TempDir(), "segment.m4s")
	require.NoError(t, os.WriteFile(filename, concat(parts...), 0644))
	return filename
}

func readFile(t *testing.T, filename string) []byte {
	t.Helper()

	b, err := os.ReadFile(filename)
	require.NoError(t, err)
	return b
}

func TestStripInitSegment(t *testing.T) {
	ftyp := box("ftyp", []byte("isom"))
	moov := box("moov", make([]byte, 64))
	init := append(append([]byte{}, ftyp...), moov...)

	t.Run("splits before the first fragment", func(t *testing.T) {
		fragment := concat(box("styp", []byte("msdh")), box("moof", make([]byte, 32)), box("mdat", make([]byte, 128)))
		filename := writeSegment(t, init, fragment)
		initPath := path.Join(path.Dir(filename), "init.mp4")

		require.NoError(t, stripInitSegment(filename, initPath))

		require.Equal(t, fragment, readFile(t, filename))
		require.Equal(t, init, readFile(t, initPath))
	})

	t.Run("segment without styp", func(t *testing.T) {
		fragment := concat(box("moof", make([]byte, 32)), box("mdat", make([]byte, 128)))
		filename := writeSegment(t, init, fragment)

		require.NoError(t, stripInitSegment(filename, ""))
		require.Equal(t, fragment, readFile(t, filename))
	})

	t.Run("fragment longer than the shift buffer", func(t *testing.T) {
		fragment := concat(box("styp", []byte("msdh")), box("mdat", make([]byte, 3*segmentShiftSize)))
		filename := writeSegment(t, init, fragment)

		require.NoError(t, stripInitSegment(filename, ""))
		require.Equal(t, fragment, readFile(t, filename))
	})

	t.Run("no init segment leaves the file alone", func(t *testing.T) {
		fragment := concat(box("styp", []byte("msdh")), box("moof", make([]byte, 32)))
		filename := writeSegment(t, fragment)

		require.Error(t, stripInitSegment(filename, ""))
		require.Equal(t, fragment, readFile(t, filename))
	})

	t.Run("segment without a fragment", func(t *testing.T) {
		filename := writeSegment(t, init)

		require.Error(t, stripInitSegment(filename, ""))
	})

	t.Run("truncated box", func(t *testing.T) {
		filename := writeSegment(t, ftyp, box("moov", make([]byte, 8))[:12])

		require.Error(t, stripInitSegment(filename, ""))
	})
}

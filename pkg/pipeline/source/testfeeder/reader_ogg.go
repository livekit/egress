// Copyright 2024 LiveKit, Inc.
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

package testfeeder

import (
	"encoding/binary"
	"io"
	"os"
)

const (
	oggPageHeaderLen = 27
	oggHeaderSig     = "OggS"

	// Opus uses 20ms frames at 48kHz = 960 samples per frame.
	opusSamplesPerFrame = 960
)

// OGGReader reads individual Opus frames from an OGG container file.
// OGG pages can contain multiple Opus packets; this reader splits them
// using the OGG segment table so each NextFrame call returns exactly one
// Opus frame suitable for RTP packetization.
type OGGReader struct {
	stream io.ReadCloser

	// Buffer of Opus packets extracted from the current OGG page.
	// Each call to NextFrame pops one packet from this buffer.
	packets [][]byte
}

// NewOGGReader opens an OGG file containing Opus audio and returns a FrameReader.
func NewOGGReader(path string) (*OGGReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	r := &OGGReader{stream: f}

	// Skip the OGG header pages (OpusHead + OpusTags).
	// These are identified by the beginning-of-stream flag or by having
	// granule position 0. We just skip until we find a page with audio data.
	for {
		packets, granule, err := r.readPage()
		if err != nil {
			f.Close()
			return nil, err
		}
		// Audio pages have granule position > 0
		if granule > 0 {
			r.packets = packets
			break
		}
	}

	return r, nil
}

func (r *OGGReader) NextFrame() ([]byte, uint32, error) {
	for len(r.packets) == 0 {
		packets, _, err := r.readPage()
		if err != nil {
			if err == io.EOF {
				return nil, 0, io.EOF
			}
			return nil, 0, err
		}
		r.packets = packets
	}

	pkt := r.packets[0]
	r.packets = r.packets[1:]
	return pkt, opusSamplesPerFrame, nil
}

func (r *OGGReader) ClockRate() uint32 {
	return opusClockRate
}

func (r *OGGReader) Close() error {
	return r.stream.Close()
}

// readPage reads one OGG page and splits its payload into individual packets
// using the segment table. Returns the packets and the page's granule position.
func (r *OGGReader) readPage() ([][]byte, uint64, error) {
	// Read page header (27 bytes)
	header := make([]byte, oggPageHeaderLen)
	if _, err := io.ReadFull(r.stream, header); err != nil {
		return nil, 0, err
	}

	if string(header[0:4]) != oggHeaderSig {
		return nil, 0, io.ErrUnexpectedEOF
	}

	granule := binary.LittleEndian.Uint64(header[6:14])
	segCount := int(header[26])

	// Read segment table
	segTable := make([]byte, segCount)
	if _, err := io.ReadFull(r.stream, segTable); err != nil {
		return nil, 0, err
	}

	// Compute total payload size and read it
	var totalSize int
	for _, s := range segTable {
		totalSize += int(s)
	}
	payload := make([]byte, totalSize)
	if _, err := io.ReadFull(r.stream, payload); err != nil {
		return nil, 0, err
	}

	// Split payload into packets using the segment table.
	// A packet spans consecutive segments; it ends when a segment is < 255.
	var packets [][]byte
	offset := 0
	packetStart := 0
	for _, s := range segTable {
		offset += int(s)
		if s < 255 {
			// End of packet
			if offset > packetStart {
				packets = append(packets, payload[packetStart:offset])
			}
			packetStart = offset
		}
	}
	// Handle trailing packet that ends exactly on a 255-byte boundary
	// (no terminating short segment). This is a continuation that spans
	// to the next page — skip it for now.

	return packets, granule, nil
}

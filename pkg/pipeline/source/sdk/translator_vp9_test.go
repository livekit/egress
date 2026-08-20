// Table tests for the VP9Translator LKTS trailer strip
// (egress-vp9-trailer-strip.patch, API-1558) — the encoder-unreachable cases
// the E2E harness cannot produce: malformed envelopes, descriptor-overlap
// windows, exact-fit boundaries, and the documented false-positive residual.
//
// Run via ../run_go_tests.sh (clones livekit/egress v1.13.0, applies the
// patch, copies this file into pkg/pipeline/source/sdk/, and runs `go test`
// inside livekit/gstreamer:1.24.12-dev — the package needs CGO GStreamer).
package sdk

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
)

// VP9 payload-descriptor flag bits (draft-ietf-payload-vp9):
// I|P|L|F|B|E|V|Z, MSB first.
const (
	descF = 0x10
	descB = 0x08
	descE = 0x04
	descV = 0x02
)

// lktsTrailer builds an n-byte trailer as the FFI's AppendTrailer emits it
// (rust-sdks @ 8a0fda59): content bytes XORed with 0xFF, then the envelope
// [n^0xFF]["LKTS"]. n=15 is the stamped/unstamped format, n=21 adds the
// frame_id TLV, n=255 is the max user_data format (len byte 0x00 on the wire).
func lktsTrailer(n int) []byte {
	tr := make([]byte, 0, n)
	for i := 0; i < n-5; i++ {
		tr = append(tr, byte(i)^0xFF)
	}
	tr = append(tr, byte(n)^0xFF)
	return append(tr, 'L', 'K', 'T', 'S')
}

// video builds n deterministic payload bytes that cannot alias the magic
// (never ends in 'S').
func video(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + 3)
	}
	if n > 0 && b[n-1] == 'S' {
		b[n-1] = 'T'
	}
	return b
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestVP9TranslatorTrailerStrip(t *testing.T) {
	endDesc := []byte{descB | descE}
	midDesc := []byte{descB} // start of frame, E clear
	// V=1: one extra scalability-structure byte; 0x00 = N_S=0, Y=0, G=0.
	ssDesc := []byte{descB | descE | descV, 0x00}
	// F=1 (flexible mode) adds no descriptor bytes without I/L.
	flexDesc := []byte{descF | descB | descE}

	tests := []struct {
		name    string
		payload []byte
		marker  bool
		want    []byte
	}{
		{
			name:    "strips 15B stamped trailer at E-packet tail",
			payload: concat(endDesc, video(100), lktsTrailer(15)),
			want:    concat(endDesc, video(100)),
		},
		{
			name:    "strips 21B frame_id trailer",
			payload: concat(endDesc, video(80), lktsTrailer(21)),
			want:    concat(endDesc, video(80)),
		},
		{
			name:    "strips 255B max-user_data trailer (len byte 0x00 on the wire)",
			payload: concat(endDesc, video(300), lktsTrailer(255)),
			want:    concat(endDesc, video(300)),
		},
		{
			name:    "strips on V=1 (scalability structure) end packet",
			payload: concat(ssDesc, video(60), lktsTrailer(15)),
			want:    concat(ssDesc, video(60)),
		},
		{
			name:    "strips on F=1 (flexible mode) end packet",
			payload: concat(flexDesc, video(60), lktsTrailer(15)),
			want:    concat(flexDesc, video(60)),
		},
		{
			// trailerLen == len(vp9.Payload): the guard admits it and the
			// strip leaves a descriptor-only packet. This is the exact-fit
			// boundary the plan characterizes (a trailer fragmented into its
			// own final packet).
			name:    "exact fit: trailer is the entire post-descriptor payload",
			payload: concat(endDesc, lktsTrailer(15)),
			want:    endDesc,
		},
		{
			name:    "no-op: plain video without magic",
			payload: concat(endDesc, video(100)),
			want:    concat(endDesc, video(100)),
		},
		{
			// The plan's marker-without-E case: trailers sit at layer-frame
			// ends (E), not RTP-marker packets; an E-clear packet is never
			// stripped even with a magic tail and the marker bit set.
			name:    "no-op: E clear, magic tail, marker set",
			payload: concat(midDesc, video(50), lktsTrailer(15)),
			marker:  true,
			want:    concat(midDesc, video(50), lktsTrailer(15)),
		},
		{
			name:    "no-op: malformed len byte (implied length < envelope)",
			payload: concat(endDesc, video(50), []byte{0x03 ^ 0xFF, 'L', 'K', 'T', 'S'}),
			want:    concat(endDesc, video(50), []byte{0x03 ^ 0xFF, 'L', 'K', 'T', 'S'}),
		},
		{
			name:    "no-op: len byte larger than the VP9 payload",
			payload: concat(endDesc, video(4), []byte{200 ^ 0xFF, 'L', 'K', 'T', 'S'}),
			want:    concat(endDesc, video(4), []byte{200 ^ 0xFF, 'L', 'K', 'T', 'S'}),
		},
		{
			// Magic window overlapping the descriptor: total payload is 5
			// bytes so payload[len-5] IS the descriptor byte; the implied
			// length (descriptor^0xFF) exceeds the 4-byte VP9 payload and the
			// guard rejects it.
			name:    "no-op: magic window overlaps the descriptor",
			payload: concat(endDesc, []byte{'L', 'K', 'T', 'S'}),
			want:    concat(endDesc, []byte{'L', 'K', 'T', 'S'}),
		},
		{
			name:    "no-op: shorter than the envelope",
			payload: []byte{'L', 'K', 'T', 'S'},
			want:    []byte{'L', 'K', 'T', 'S'},
		},
		{
			name:    "no-op: all-zero (padding-like) payload, E clear",
			payload: make([]byte, 64),
			want:    make([]byte, 64),
		},
		{
			// The documented residual: legit video whose tail happens to spell
			// a plausible envelope IS stripped. Requires the 4 magic bytes on
			// an E packet (~2^-32 for random bytes; the len-byte range check
			// barely filters on near-MTU payloads, where ~251/256 values are
			// admissible); recorded here so the trade-off stays visible, not
			// because it is desired.
			name:    "false positive: legit video ending in a plausible envelope is stripped",
			payload: concat(endDesc, video(50), []byte{15 ^ 0xFF, 'L', 'K', 'T', 'S'}),
			want:    concat(endDesc, video(40)),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewVP9Translator()
			pkt := &rtp.Packet{
				Header:  rtp.Header{Version: 2, Marker: tc.marker},
				Payload: append([]byte(nil), tc.payload...),
			}
			translator.Translate(pkt)
			if !bytes.Equal(pkt.Payload, tc.want) {
				t.Fatalf("payload mismatch\n got:  %x\n want: %x", pkt.Payload, tc.want)
			}
			// Byte-prefix property: the strip only ever truncates.
			if !bytes.HasPrefix(tc.payload, pkt.Payload) {
				t.Fatalf("result is not a prefix of the input")
			}
			// Idempotency: a second pass must not strip again.
			before := append([]byte(nil), pkt.Payload...)
			translator.Translate(pkt)
			if !bytes.Equal(pkt.Payload, before) {
				t.Fatalf("second Translate changed the payload\n got:  %x\n want: %x", pkt.Payload, before)
			}
		})
	}
}

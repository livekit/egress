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
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/livekit-server/pkg/sfu"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	blankFramesH264Sample = "livekit_par_switch_video_202x360_sar405x404_25fps.h264"
	blankFramesVP8Sample  = "livekit_avsync_p0_video_red_1080p24.vp8.ivf"
	blankFramesVP8Frames  = 48 // the first two seconds, starting at a keyframe

	blankFramesClipDuration = 2 * time.Second
	// as many as the SFU sends when it closes a downtrack
	blankFramesCount      = 6
	blankFramesLoopsAfter = 3

	ivfFileHeaderSize  = 32
	ivfFrameHeaderSize = 12
)

// blankFrameStream writes a two second clip in a loop, and the SFU's blank keyframes on demand.
type blankFrameStream interface {
	header() []byte
	clip(w io.Writer) error
	blanks(w io.Writer) error
}

type h264BlankFrameStream struct {
	sample []byte
}

func (s *h264BlankFrameStream) header() []byte { return nil }

func (s *h264BlankFrameStream) clip(w io.Writer) error {
	_, err := w.Write(s.sample)
	return err
}

func (s *h264BlankFrameStream) blanks(w io.Writer) error {
	for range blankFramesCount {
		for _, nal := range sfu.H264KeyFrame2x2 {
			if _, err := w.Write(append([]byte{0, 0, 0, 1}, nal...)); err != nil {
				return err
			}
		}
	}
	return nil
}

// vp8BlankFrameStream rewrites IVF frame timestamps so the loop plays continuously.
type vp8BlankFrameStream struct {
	fileHeader []byte
	frames     [][]byte
	timestamp  uint64
}

func newVP8BlankFrameStream(t *testing.T, ivf []byte) *vp8BlankFrameStream {
	s := &vp8BlankFrameStream{fileHeader: ivf[:ivfFileHeaderSize]}
	offset := ivfFileHeaderSize
	for range blankFramesVP8Frames {
		require.LessOrEqual(t, offset+ivfFrameHeaderSize, len(ivf), "IVF sample too short")
		size := int(binary.LittleEndian.Uint32(ivf[offset:]))
		offset += ivfFrameHeaderSize
		require.LessOrEqual(t, offset+size, len(ivf), "IVF sample too short")
		s.frames = append(s.frames, ivf[offset:offset+size])
		offset += size
	}
	return s
}

func (s *vp8BlankFrameStream) header() []byte { return s.fileHeader }

func (s *vp8BlankFrameStream) writeFrame(w io.Writer, frame []byte) error {
	header := make([]byte, ivfFrameHeaderSize, ivfFrameHeaderSize+len(frame))
	binary.LittleEndian.PutUint32(header, uint32(len(frame)))
	binary.LittleEndian.PutUint64(header[4:], s.timestamp)
	s.timestamp++
	_, err := w.Write(append(header, frame...))
	return err
}

func (s *vp8BlankFrameStream) clip(w io.Writer) error {
	for _, frame := range s.frames {
		if err := s.writeFrame(w, frame); err != nil {
			return err
		}
	}
	return nil
}

func (s *vp8BlankFrameStream) blanks(w io.Writer) error {
	for range blankFramesCount {
		if err := s.writeFrame(w, sfu.VP8KeyFrame8x8); err != nil {
			return err
		}
	}
	return nil
}

// testTrackBlankFrames checks a passthrough track egress keeps recording past the SFU's blank keyframes.
// They are sent mid-stream so that a stopped muxer shows as the egress ending before the track does.
func (r *Runner) testTrackBlankFrames(mimeType types.MimeType) func(t *testing.T, test *testCase) {
	return func(t *testing.T, test *testCase) {
		var stream blankFrameStream
		var webrtcMime string
		switch mimeType {
		case types.MimeTypeH264:
			sample, err := os.ReadFile(path.Join(mediaSamplesDir, blankFramesH264Sample))
			require.NoError(t, err)
			stream = &h264BlankFrameStream{sample: sample}
			webrtcMime = webrtc.MimeTypeH264
		case types.MimeTypeVP8:
			ivf, err := os.ReadFile(path.Join(mediaSamplesDir, blankFramesVP8Sample))
			require.NoError(t, err)
			stream = newVP8BlankFrameStream(t, ivf)
			webrtcMime = webrtc.MimeTypeVP8
		default:
			t.Fatalf("no blank frame stream for %s", mimeType)
		}

		room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
			APIKey:              r.ApiKey,
			APISecret:           r.ApiSecret,
			RoomName:            r.RoomName,
			ParticipantName:     "egress-blank-frames",
			ParticipantIdentity: fmt.Sprintf("blank-frames-%d", rand.Intn(100)),
		}, lksdk.NewRoomCallback())
		require.NoError(t, err)
		t.Cleanup(room.Disconnect)

		// the clip loops until the egress is active, then the blank frames arrive mid-stream
		pr, pw := io.Pipe()
		sendBlanks := make(chan struct{})
		go func() {
			defer pw.Close()
			if h := stream.header(); h != nil {
				if _, err := pw.Write(h); err != nil {
					return
				}
			}
			for {
				select {
				case <-sendBlanks:
					if err := stream.blanks(pw); err != nil {
						return
					}
					for range blankFramesLoopsAfter {
						if err := stream.clip(pw); err != nil {
							return
						}
					}
					return
				default:
				}
				if err := stream.clip(pw); err != nil {
					return
				}
			}
		}()

		done := make(chan struct{})
		track, err := lksdk.NewLocalReaderTrack(pr, webrtcMime,
			lksdk.ReaderTrackWithFrameDuration(frameDurations[mimeType]),
			lksdk.ReaderTrackWithOnWriteComplete(func() { close(done) }),
		)
		require.NoError(t, err)
		pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "blank-frames"})
		require.NoError(t, err)
		test.videoTrackID = pub.SID()
		test.videoCodec = mimeType

		req := r.buildRequest(test)
		p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
		require.NoError(t, err)
		p.GetFileConfig().OutputType = test.fileOptions.outputType

		egressID := r.startEgress(t, req)
		close(sendBlanks)

		// a muxer stopped by the blank frames ends the egress while the track is still publishing
		publishTimeout := time.After(30 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
	publishing:
		for {
			select {
			case <-done:
				break publishing
			case <-publishTimeout:
				t.Fatal("timed out publishing past the blank frames")
			case <-ticker.C:
				if info := r.endedEgress(egressID); info != nil {
					t.Fatalf("egress ended %s before the track finished publishing: %s", info.Status, info.Details)
				}
			}
		}
		require.NoError(t, room.LocalParticipant.UnpublishTrack(pub.SID()))

		var res *livekit.EgressInfo
		deadline := time.Now().Add(60 * time.Second)
		for res == nil && time.Now().Before(deadline) {
			if res = r.endedEgress(egressID); res == nil {
				time.Sleep(100 * time.Millisecond)
			}
		}
		require.NotNil(t, res, "timed out waiting for egress to end")
		require.Equal(t, livekit.EgressStatus_EGRESS_COMPLETE.String(), res.Status.String(),
			"egress did not survive the blank frames: %s", res.Error)

		minDuration := time.Duration(blankFramesLoopsAfter) * blankFramesClipDuration
		test.contentCheck = func(t *testing.T, _ string, info *FFProbeInfo) {
			actual, err := strconv.ParseFloat(info.Format.Duration, 64)
			require.NoError(t, err)
			require.Greater(t, actual, minDuration.Seconds(), "recording stops at the blank frames")
		}
		r.verifyFile(t, test, p, res)
	}
}

// endedEgress returns the egress's latest info once it has ended, or nil while it is still running
func (r *Runner) endedEgress(egressID string) *livekit.EgressInfo {
	r.updates.Lock()
	info := r.updates.EgressInfo
	r.updates.Unlock()
	if info == nil || info.EgressId != egressID {
		return nil
	}
	switch info.Status {
	case livekit.EgressStatus_EGRESS_COMPLETE,
		livekit.EgressStatus_EGRESS_FAILED,
		livekit.EgressStatus_EGRESS_ABORTED:
		return info
	}
	return nil
}

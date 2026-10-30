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

package builder

import (
	"slices"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/linkdata/deadlock"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type mcapFixture struct {
	pipeline *gstreamer.Pipeline
	bin      *MCAPBin
	tracks   map[string]*config.TrackSource

	mu      deadlock.Mutex
	samples map[string]int
	ended   []string
	errs    []error
	eos     atomic.Int32
}

func newMCAPFixture(t *testing.T, trackIDs ...string) *mcapFixture {
	t.Helper()
	initGStreamer(t)

	f := &mcapFixture{
		tracks:  make(map[string]*config.TrackSource),
		samples: make(map[string]int),
	}

	callbacks := &gstreamer.Callbacks{}
	callbacks.SetOnError(func(err error) {
		f.mu.Lock()
		f.errs = append(f.errs, err)
		f.mu.Unlock()
	})
	p, err := gstreamer.NewPipeline("mcap-test", 0, callbacks)
	require.NoError(t, err)
	f.pipeline = p

	conf := &config.PipelineConfig{}
	conf.Live = true
	for _, id := range trackIDs {
		conf.AudioTracks = append(conf.AudioTracks, f.newTrack(t, id))
	}

	f.bin, err = BuildMCAPBin(p, conf, f.writeSample, f.trackEnded)
	require.NoError(t, err)
	require.NoError(t, p.Link())
	p.UpgradeState(gstreamer.StateStarted)

	p.SetWatch(func(msg *gst.Message) bool {
		if msg.Type() == gst.MessageEOS {
			f.eos.Inc()
		}
		return true
	})
	go func() { _ = p.Run() }()
	t.Cleanup(p.Stop)

	return f
}

func (f *mcapFixture) newTrack(t *testing.T, trackID string) *config.TrackSource {
	t.Helper()
	src, err := app.NewAppSrc()
	require.NoError(t, err)
	ts := &config.TrackSource{
		TrackID:     trackID,
		TrackKind:   lksdk.TrackKindAudio,
		AppSrc:      src,
		MimeType:    types.MimeTypePCMU,
		PayloadType: 0,
		ClockRate:   8000,
	}
	f.tracks[trackID] = ts
	return ts
}

func (f *mcapFixture) writeSample(ts *config.TrackSource, _ time.Duration, _ []byte) error {
	f.mu.Lock()
	f.samples[ts.TrackID]++
	f.mu.Unlock()
	return nil
}

func (f *mcapFixture) trackEnded(trackID string) {
	f.mu.Lock()
	f.ended = append(f.ended, trackID)
	f.mu.Unlock()
}

func (f *mcapFixture) sampleCount(trackID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.samples[trackID]
}

func (f *mcapFixture) hasEnded(trackID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.ended, trackID)
}

// push sends 20ms PCMU packets into a track until stop is closed
func (f *mcapFixture) push(t *testing.T, trackID string, stop <-chan struct{}) {
	t.Helper()
	src := f.tracks[trackID].AppSrc
	go func() {
		for seq := uint16(0); ; seq++ {
			select {
			case <-stop:
				return
			default:
			}
			pkt := rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: 1},
				Payload: make([]byte, 160),
			}
			data, err := pkt.Marshal()
			if err != nil {
				return
			}
			buf := gst.NewBufferFromBytes(data)
			buf.SetPresentationTimestamp(gst.ClockTime(uint64(seq) * uint64(20*time.Millisecond)))
			if src.PushBuffer(buf) != gst.FlowOK {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

// Departing tracks must not end the file, even the last one; only the egress ending does
func TestMCAPBinFunnelEOS(t *testing.T) {
	f := newMCAPFixture(t, "TR_a", "TR_b", "TR_c")

	stop := map[string]chan struct{}{}
	for _, id := range []string{"TR_a", "TR_b", "TR_c"} {
		stop[id] = make(chan struct{})
		f.push(t, id, stop[id])
	}
	require.Eventually(t, func() bool {
		return f.sampleCount("TR_a") > 0 && f.sampleCount("TR_b") > 0 && f.sampleCount("TR_c") > 0
	}, 5*time.Second, 50*time.Millisecond, "samples did not reach the appsink tagged with their track")

	close(stop["TR_a"])
	f.bin.onTrackRemoved("TR_a")
	f.tracks["TR_a"].AppSrc.EndStream()
	require.Eventually(t, func() bool { return f.hasEnded("TR_a") }, time.Second, 20*time.Millisecond, "video-order removal waited for the fallback")

	close(stop["TR_b"])
	f.tracks["TR_b"].AppSrc.EndStream()
	f.bin.onTrackRemoved("TR_b")
	require.Eventually(t, func() bool { return f.hasEnded("TR_b") }, time.Second, 20*time.Millisecond, "audio-order removal waited for the fallback")

	before := f.sampleCount("TR_c")
	require.Eventually(t, func() bool {
		return f.sampleCount("TR_c") > before+10
	}, 5*time.Second, 50*time.Millisecond, "the remaining track stopped flowing after the others left")

	close(stop["TR_c"])
	f.tracks["TR_c"].AppSrc.EndStream()
	f.bin.onTrackRemoved("TR_c")
	require.Eventually(t, func() bool { return f.hasEnded("TR_c") }, time.Second, 20*time.Millisecond)
	require.Zero(t, f.eos.Load(), "a departing track ended the file")

	stop["TR_d"] = make(chan struct{})
	f.bin.onTrackAdded(f.newTrack(t, "TR_d"))
	f.push(t, "TR_d", stop["TR_d"])
	require.Eventually(t, func() bool { return f.sampleCount("TR_d") > 0 }, 5*time.Second, 50*time.Millisecond, "a track joining after every other left never reached the appsink")

	close(stop["TR_d"])
	f.tracks["TR_d"].AppSrc.EndStream()
	f.pipeline.SendEOS()
	require.Eventually(t, func() bool { return f.eos.Load() > 0 }, 5*time.Second, 50*time.Millisecond, "egress end never reached the appsink")

	f.mu.Lock()
	defer f.mu.Unlock()
	require.ElementsMatch(t, []string{"TR_a", "TR_b", "TR_c"}, f.ended)
	require.Empty(t, f.errs)
}

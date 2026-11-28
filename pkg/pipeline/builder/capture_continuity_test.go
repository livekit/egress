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
	"fmt"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"
)

func TestCaptureContinuityCountsGapsAndFills(t *testing.T) {
	initGStreamer(t)

	pipeline, err := gst.NewPipelineFromString(fmt.Sprintf(
		"appsrc name=%s format=time caps=audio/x-raw,format=S16LE,rate=48000,channels=1,layout=interleaved ! "+
			"audiorate name=audio_rate tolerance=0 ! fakesink "+
			"appsrc name=%s format=time caps=video/x-raw,format=I420,width=16,height=16,framerate=30/1 ! "+
			"videorate name=%s ! video/x-raw,framerate=30/1 ! fakesink",
		webAudioSrcName, webVideoSrcName, webVideoRateName,
	))
	require.NoError(t, err)

	c := newCaptureContinuity(pipeline)
	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })

	// audio: 10 ms buffers, then a 50 ms hole flagged DISCONT
	audioEl, err := pipeline.GetElementByName(webAudioSrcName)
	require.NoError(t, err)
	audio := app.SrcFromElement(audioEl)
	const audioBuf = 10 * time.Millisecond
	audioPTS := []time.Duration{0, 10, 20, 30, 40, 100, 110, 120}
	for i, ms := range audioPTS {
		buf := gst.NewBufferFromBytes(make([]byte, 480*2))
		buf.SetPresentationTimestamp(gst.ClockTime(ms * time.Millisecond))
		buf.SetDuration(gst.ClockTime(audioBuf))
		if i == 0 || ms == 100 {
			buf.SetFlags(gst.BufferFlagDiscont)
		}
		require.Equal(t, gst.FlowOK, audio.PushBuffer(buf))
	}
	audio.EndStream()

	// video: 30 fps frames with three frames missing
	videoEl, err := pipeline.GetElementByName(webVideoSrcName)
	require.NoError(t, err)
	video := app.SrcFromElement(videoEl)
	frame := time.Second / 30
	for _, n := range []int{0, 1, 2, 3, 7, 8, 9} {
		buf := gst.NewBufferFromBytes(make([]byte, 16*16*3/2))
		buf.SetPresentationTimestamp(gst.ClockTime(time.Duration(n) * frame))
		buf.SetDuration(gst.ClockTime(frame))
		require.Equal(t, gst.FlowOK, video.PushBuffer(buf))
	}
	video.EndStream()

	msg := pipeline.GetPipelineBus().TimedPopFiltered(gst.ClockTime(5*time.Second), gst.MessageEOS|gst.MessageError)
	require.NotNil(t, msg, "pipeline did not reach EOS")
	require.Equal(t, gst.MessageEOS, msg.Type(), msg.String())

	got := make(map[string]any)
	fields := c.fields()
	for i := 0; i+1 < len(fields); i += 2 {
		got[fields[i].(string)] = fields[i+1]
	}
	t.Logf("capture continuity: %v", got)

	require.Equal(t, uint64(1), got["audioCaptureGaps"], "the first buffer's DISCONT is not a gap")
	require.Equal(t, uint64(50*48), got["audioSamplesAdded"], "a 50 ms hole at 48 kHz")
	require.Equal(t, uint64(0), got["audioSamplesDropped"])
	require.Equal(t, uint64(0), got["videoCaptureGaps"])
	require.Equal(t, uint64(3), got["videoFramesDuplicated"], "three missing frames")
	require.Equal(t, uint64(0), got["videoFramesDropped"])
}

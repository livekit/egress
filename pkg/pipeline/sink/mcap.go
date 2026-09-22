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

package sink

import (
	"fmt"
	"os"
	"path"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	mcapwriter "github.com/livekit/egress/pkg/mcap"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

// MCAPSink terminates encoded Opus and H264 branches in appsinks and writes
// both streams into one MCAP file. The writer owns synchronization between the
// independent GStreamer streaming threads.
type MCAPSink struct {
	*config.FileConfig
	*uploader.Uploader

	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks
	bin       *builder.MCAPBin

	file   *os.File
	writer *mcapwriter.Writer

	eosReceived atomic.Bool
}

func newMCAPSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	o *config.FileConfig,
	callbacks *gstreamer.Callbacks,
	monitor *stats.HandlerMonitor,
) (*MCAPSink, error) {
	u, err := uploader.New(o.StorageConfig, conf.BackupConfig, monitor, conf.StorageObserver, conf.Info)
	if err != nil {
		return nil, err
	}

	s := &MCAPSink{
		FileConfig: o,
		Uploader:   u,
		conf:       conf,
		callbacks:  callbacks,
	}

	audioCallbacks := &app.SinkCallbacks{NewSampleFunc: s.onAudioSample}
	videoCallbacks := &app.SinkCallbacks{NewSampleFunc: s.onVideoSample}
	s.bin, err = builder.BuildMCAPBin(p, conf, audioCallbacks, videoCallbacks)
	if err != nil {
		return nil, err
	}
	if err = p.AddSinkBin(s.bin.Bin); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *MCAPSink) Start() error {
	f, err := os.Create(s.LocalFilepath)
	if err != nil {
		return errors.MarkDestinationError(err)
	}

	metadata := map[string]string{
		"egress_id":   s.conf.Info.EgressId,
		"room_id":     s.conf.Info.RoomId,
		"room_name":   s.conf.Info.RoomName,
		"source_type": string(s.conf.SourceType),
	}
	w, err := mcapwriter.NewWriter(f, mcapwriter.Options{
		Audio:    s.conf.AudioEnabled,
		Video:    s.conf.VideoEnabled,
		Metadata: metadata,
	})
	if err != nil {
		_ = f.Close()
		return errors.MarkDestinationError(err)
	}

	s.file = f
	s.writer = w
	return nil
}

func (s *MCAPSink) onAudioSample(appSink *app.Sink) gst.FlowReturn {
	return s.onSample(appSink, func(pts time.Duration, data []byte) error {
		return s.writer.WriteAudio(pts, data)
	})
}

func (s *MCAPSink) onVideoSample(appSink *app.Sink) gst.FlowReturn {
	return s.onSample(appSink, func(pts time.Duration, data []byte) error {
		return s.writer.WriteVideo(pts, "livekit_video", data)
	})
}

func (s *MCAPSink) onSample(appSink *app.Sink, write func(time.Duration, []byte) error) gst.FlowReturn {
	sample := appSink.PullSample()
	if sample == nil {
		return gst.FlowOK
	}
	buffer := sample.GetBuffer()
	if buffer == nil {
		return gst.FlowOK
	}
	pts := buffer.PresentationTimestamp()
	if pts == gst.ClockTimeNone {
		s.fail(fmt.Errorf("MCAP sample has no presentation timestamp"))
		return gst.FlowError
	}
	segment := sample.GetSegment()
	if segment == nil {
		s.fail(fmt.Errorf("MCAP sample has no segment"))
		return gst.FlowError
	}
	runningTime := gst.ClockTime(segment.ToRunningTime(gst.FormatTime, uint64(pts))).AsDuration()
	if runningTime == nil {
		s.fail(fmt.Errorf("MCAP sample timestamp is outside its segment"))
		return gst.FlowError
	}

	mapped := buffer.Map(gst.MapRead)
	if mapped == nil {
		s.fail(fmt.Errorf("failed to map MCAP sample"))
		return gst.FlowError
	}
	defer buffer.Unmap()

	if s.writer == nil {
		s.fail(fmt.Errorf("MCAP writer is not initialized"))
		return gst.FlowError
	}
	if err := write(*runningTime, mapped.Bytes()); err != nil {
		s.fail(err)
		return gst.FlowError
	}
	return gst.FlowOK
}

func (s *MCAPSink) fail(err error) {
	s.callbacks.OnError(psrpc.NewError(psrpc.Unavailable, errors.MarkDestinationError(err)))
}

// AddEOSProbe waits for EOS at every appsink. A single-bin probe would only
// observe the last branch, while MCAP has independent audio and video branches.
func (s *MCAPSink) AddEOSProbe() {
	pads := make([]*gst.Pad, 0, len(s.bin.SinkElements))
	for _, element := range s.bin.SinkElements {
		pad := element.GetStaticPad("sink")
		if pad == nil {
			logger.Errorw("failed to add MCAP EOS probe", nil, "element", element.GetName())
			continue
		}
		pads = append(pads, pad)
	}

	var expecting atomic.Int32
	expecting.Store(int32(len(pads)))
	if len(pads) == 0 {
		s.eosReceived.Store(true)
		return
	}

	for _, pad := range pads {
		pad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if event := info.GetEvent(); event != nil && event.Type() == gst.EventTypeEOS {
				if expecting.Dec() == 0 {
					logger.Debugw("eos received", "sink", s.bin.GetName())
					s.eosReceived.Store(true)
				}
				return gst.PadProbeRemove
			}
			return gst.PadProbeOK
		})
	}
}

func (s *MCAPSink) EOSReceived() bool {
	return s.eosReceived.Load()
}

func (s *MCAPSink) UploadManifest(filepath string) (string, bool, error) {
	if s.DisableManifest && !s.conf.Info.BackupStorageUsed {
		return "", false, nil
	}

	storagePath := path.Join(path.Dir(s.StorageFilepath), path.Base(filepath))
	location, _, err := s.Upload(filepath, storagePath, types.OutputTypeJSON, false)
	if err != nil {
		return "", false, err
	}
	return location, true, nil
}

func (s *MCAPSink) Close() error {
	if s.writer == nil || s.file == nil {
		return nil
	}
	if err := s.writer.Close(); err != nil {
		_ = s.file.Close()
		return errors.MarkDestinationError(err)
	}
	if err := s.file.Close(); err != nil {
		return errors.MarkDestinationError(err)
	}

	start := time.Now()
	location, size, err := s.Upload(s.LocalFilepath, s.StorageFilepath, s.OutputType, false)
	if err != nil {
		logger.Debugw("MCAP upload failed", err)
		return err
	}
	s.FileInfo.Location = location
	s.FileInfo.Size = size
	logger.Debugw("MCAP upload completed", "bytes", size, "duration", time.Since(start))

	if s.conf.Manifest != nil {
		s.conf.Manifest.AddFile(s.StorageFilepath, location)
	}
	return nil
}

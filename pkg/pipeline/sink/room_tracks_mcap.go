// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package sink

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	mcapwriter "github.com/livekit/egress/pkg/mcap"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// RoomTracksMCAPSink owns a self-contained GStreamer branch per subscribed
// LiveKit track. Unlike the regular media bins, these branches never meet at a
// selector, compositor, or audio mixer.
type RoomTracksMCAPSink struct {
	*config.FileConfig
	*uploader.Uploader

	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks
	bin       *gstreamer.Bin

	mu       sync.Mutex
	paths    map[string]string
	appSinks map[string]*gst.Element

	file   *os.File
	writer *mcapwriter.Writer

	eosReceived atomic.Bool
}

func newRoomTracksMCAPSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	o *config.FileConfig,
	callbacks *gstreamer.Callbacks,
	monitor *stats.HandlerMonitor,
) (*RoomTracksMCAPSink, error) {
	u, err := uploader.New(o.StorageConfig, conf.BackupConfig, monitor, conf.StorageObserver, conf.Info)
	if err != nil {
		return nil, err
	}
	s := &RoomTracksMCAPSink{
		FileConfig: o,
		Uploader:   u,
		conf:       conf,
		callbacks:  callbacks,
		bin:        p.NewBin("room_tracks_mcap"),
		paths:      make(map[string]string),
		appSinks:   make(map[string]*gst.Element),
	}
	for _, track := range conf.VideoTracks {
		if err = s.addTrack(track); err != nil {
			return nil, err
		}
	}
	for _, track := range conf.AudioTracks {
		if err = s.addTrack(track); err != nil {
			return nil, err
		}
	}
	callbacks.AddOnTrackAdded(func(track *config.TrackSource) {
		if err := s.addTrack(track); err != nil {
			callbacks.OnError(err)
		}
	})
	callbacks.AddOnTrackRemoved(s.removeTrack)
	if err = p.AddSourceBin(s.bin); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *RoomTracksMCAPSink) Start() error {
	f, err := os.Create(s.LocalFilepath)
	if err != nil {
		return errors.MarkDestinationError(err)
	}
	w, err := mcapwriter.NewWriter(f, mcapwriter.Options{
		DynamicTracks: true,
		Metadata: map[string]string{
			"egress_id": s.conf.Info.EgressId, "room_id": s.conf.Info.RoomId,
			"room_name": s.conf.Info.RoomName, "source_type": string(types.RequestTypeRoomTracks),
		},
	})
	if err != nil {
		_ = f.Close()
		return errors.MarkDestinationError(err)
	}
	s.file = f
	s.writer = w
	return nil
}

func (s *RoomTracksMCAPSink) addTrack(track *config.TrackSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.paths[track.TrackID]; ok {
		return nil
	}
	name := "room_track_" + sanitizeElementName(track.TrackID)
	var (
		bin     *gstreamer.Bin
		appSink *app.Sink
		err     error
	)
	switch track.TrackKind {
	case lksdk.TrackKindAudio:
		bin, appSink, err = s.buildAudioTrack(track, name)
	case lksdk.TrackKindVideo:
		bin, appSink, err = s.buildVideoTrack(track, name)
	default:
		return errors.ErrNotSupported(track.TrackKind.String())
	}
	if err != nil {
		return err
	}
	if err = s.bin.AddSourceBin(bin); err != nil {
		return err
	}
	s.paths[track.TrackID] = name
	s.appSinks[track.TrackID] = appSink.Element
	return nil
}

func (s *RoomTracksMCAPSink) removeTrack(trackID string) {
	s.mu.Lock()
	name := s.paths[trackID]
	delete(s.paths, trackID)
	delete(s.appSinks, trackID)
	s.mu.Unlock()
	if name != "" {
		if err := s.bin.RemoveSourceBin(name); err != nil {
			s.callbacks.OnError(err)
		}
	}
}

func (s *RoomTracksMCAPSink) buildVideoTrack(track *config.TrackSource, name string) (*gstreamer.Bin, *app.Sink, error) {
	bin := s.bin.NewBin(name)
	bin.SetEOSFunc(func() bool { return false })
	if err := configureRoomTrackAppSrc(track, s.conf.Live); err != nil {
		return nil, nil, err
	}
	elements := []*gst.Element{track.AppSrc.Element}

	var names []string
	switch track.MimeType {
	case types.MimeTypeH264:
		names = []string{"rtph264depay", "h264parse", "avdec_h264"}
	case types.MimeTypeVP8:
		names = []string{"rtpvp8depay", "vp8dec"}
	case types.MimeTypeVP9:
		names = []string{"rtpvp9depay", "vp9dec"}
	default:
		return nil, nil, errors.ErrNotSupported(string(track.MimeType))
	}
	for _, elementName := range names {
		element, err := gst.NewElement(elementName)
		if err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}
		elements = append(elements, element)
	}
	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	x264Enc, err := gst.NewElement("x264enc")
	if err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	x264Enc.SetArg("speed-preset", "veryfast")
	if err = x264Enc.SetProperty("bframes", uint(0)); err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	if err = x264Enc.SetProperty("bitrate", uint(s.conf.VideoBitrate)); err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	if err = x264Enc.SetProperty("key-int-max", uint(max(s.conf.Framerate, 1))); err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	profileCaps, err := capsFilter("video/x-h264,profile=baseline")
	if err != nil {
		return nil, nil, err
	}
	h264Parse, err := gst.NewElement("h264parse")
	if err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	if err = h264Parse.SetProperty("config-interval", -1); err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	outputCaps, err := capsFilter("video/x-h264,stream-format=byte-stream,alignment=au")
	if err != nil {
		return nil, nil, err
	}
	mediaTrack := s.mcapTrack(track, "video")
	appSink, err := app.NewAppSink()
	if err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	appSink.SetCallbacks(&app.SinkCallbacks{NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn {
		return s.onSample(appSink, func(pts time.Duration, data []byte) error {
			return s.writer.WriteVideoTrack(pts, mediaTrack, data)
		})
	}})
	if err = configureRoomTrackAppSink(appSink); err != nil {
		return nil, nil, err
	}
	elements = append(elements, videoConvert, x264Enc, profileCaps, h264Parse, outputCaps, appSink.Element)
	if err = bin.AddElements(elements...); err != nil {
		return nil, nil, err
	}
	return bin, appSink, nil
}

func (s *RoomTracksMCAPSink) buildAudioTrack(track *config.TrackSource, name string) (*gstreamer.Bin, *app.Sink, error) {
	bin := s.bin.NewBin(name)
	bin.SetEOSFunc(func() bool { return false })
	if err := configureRoomTrackAppSrc(track, s.conf.Live); err != nil {
		return nil, nil, err
	}
	elements := []*gst.Element{track.AppSrc.Element}
	var names []string
	switch track.MimeType {
	case types.MimeTypeOpus:
		names = []string{"rtpopusdepay", "opusparse"}
	case types.MimeTypePCMU:
		names = []string{"rtppcmudepay", "mulawdec", "audioconvert", "audioresample", "opusenc", "opusparse"}
	case types.MimeTypePCMA:
		names = []string{"rtppcmadepay", "alawdec", "audioconvert", "audioresample", "opusenc", "opusparse"}
	default:
		return nil, nil, errors.ErrNotSupported(string(track.MimeType))
	}
	for _, elementName := range names {
		element, err := gst.NewElement(elementName)
		if err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}
		elements = append(elements, element)
	}
	mediaTrack := s.mcapTrack(track, "audio")
	appSink, err := app.NewAppSink()
	if err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	appSink.SetCallbacks(&app.SinkCallbacks{NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn {
		return s.onSample(appSink, func(pts time.Duration, data []byte) error {
			return s.writer.WriteAudioTrack(pts, mediaTrack, data)
		})
	}})
	if err = configureRoomTrackAppSink(appSink); err != nil {
		return nil, nil, err
	}
	elements = append(elements, appSink.Element)
	if err = bin.AddElements(elements...); err != nil {
		return nil, nil, err
	}
	return bin, appSink, nil
}

func configureRoomTrackAppSrc(track *config.TrackSource, live bool) error {
	track.AppSrc.SetArg("format", "time")
	if err := track.AppSrc.SetProperty("is-live", live); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	media := "video"
	encoding := strings.TrimPrefix(strings.ToUpper(string(track.MimeType)), "VIDEO/")
	if track.TrackKind.String() == "audio" {
		media = "audio"
		encoding = strings.TrimPrefix(strings.ToUpper(string(track.MimeType)), "AUDIO/")
	}
	caps := fmt.Sprintf("application/x-rtp,media=%s,payload=%d,encoding-name=%s,clock-rate=%d", media, track.PayloadType, encoding, track.ClockRate)
	if err := track.AppSrc.SetProperty("caps", gst.NewCapsFromString(caps)); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

func configureRoomTrackAppSink(sink *app.Sink) error {
	// AppWriter waits for the appsrc PLAYING notification before pushing RTP.
	// Do not make pipeline preroll wait for this sink's first sample, or the two
	// sides form a PAUSED -> PLAYING deadlock.
	if err := sink.SetProperty("async", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := sink.SetProperty("sync", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := sink.SetProperty("enable-last-sample", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

func capsFilter(value string) (*gst.Element, error) {
	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(value)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return caps, nil
}

func (s *RoomTracksMCAPSink) mcapTrack(track *config.TrackSource, kind string) mcapwriter.Track {
	identity := sanitizeTopicSegment(track.ParticipantIdentity)
	name := sanitizeTopicSegment(track.TrackName)
	if name == "" {
		name = sanitizeTopicSegment(track.TrackID)
	}
	topic := fmt.Sprintf("/livekit/%s/%s/%s", identity, kind, name)
	return mcapwriter.Track{
		ID: track.TrackID, Topic: topic, FrameID: strings.TrimPrefix(topic, "/"),
		Metadata: map[string]string{
			"livekit.track_id": track.TrackID, "livekit.track_name": track.TrackName,
			"livekit.participant_identity": track.ParticipantIdentity,
			"livekit.source":               strings.ToLower(track.PublicationSource.String()),
			"livekit.input_codec":          string(track.MimeType),
		},
	}
}

func sanitizeTopicSegment(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}

func sanitizeElementName(value string) string {
	return strings.ReplaceAll(sanitizeTopicSegment(value), "-", "_")
}

func (s *RoomTracksMCAPSink) onSample(appSink *app.Sink, write func(time.Duration, []byte) error) gst.FlowReturn {
	sample := appSink.PullSample()
	if sample == nil {
		return gst.FlowOK
	}
	buffer := sample.GetBuffer()
	if buffer == nil {
		return gst.FlowOK
	}
	pts := buffer.PresentationTimestamp()
	segment := sample.GetSegment()
	if pts == gst.ClockTimeNone || segment == nil {
		s.fail(fmt.Errorf("room-tracks MCAP sample has no presentation timestamp or segment"))
		return gst.FlowError
	}
	runningTime := gst.ClockTime(segment.ToRunningTime(gst.FormatTime, uint64(pts))).AsDuration()
	if runningTime == nil {
		s.fail(fmt.Errorf("room-tracks MCAP sample timestamp is outside its segment"))
		return gst.FlowError
	}
	mapped := buffer.Map(gst.MapRead)
	if mapped == nil {
		s.fail(fmt.Errorf("failed to map room-tracks MCAP sample"))
		return gst.FlowError
	}
	defer buffer.Unmap()
	if s.writer == nil {
		s.fail(fmt.Errorf("room-tracks MCAP writer is not initialized"))
		return gst.FlowError
	}
	if err := write(*runningTime, mapped.Bytes()); err != nil {
		s.fail(err)
		return gst.FlowError
	}
	return gst.FlowOK
}

func (s *RoomTracksMCAPSink) fail(err error) {
	s.callbacks.OnError(psrpc.NewError(psrpc.Unavailable, errors.MarkDestinationError(err)))
}

func (s *RoomTracksMCAPSink) AddEOSProbe() {
	s.mu.Lock()
	sinks := make([]*gst.Element, 0, len(s.appSinks))
	for _, sink := range s.appSinks {
		sinks = append(sinks, sink)
	}
	s.mu.Unlock()
	if len(sinks) == 0 {
		s.eosReceived.Store(true)
		return
	}
	var expecting atomic.Int32
	expecting.Store(int32(len(sinks)))
	for _, element := range sinks {
		pad := element.GetStaticPad("sink")
		pad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if event := info.GetEvent(); event != nil && event.Type() == gst.EventTypeEOS {
				if expecting.Dec() == 0 {
					s.eosReceived.Store(true)
				}
				return gst.PadProbeRemove
			}
			return gst.PadProbeOK
		})
	}
}

func (s *RoomTracksMCAPSink) EOSReceived() bool { return s.eosReceived.Load() }

func (s *RoomTracksMCAPSink) UploadManifest(filepath string) (string, bool, error) {
	if s.DisableManifest && !s.conf.Info.BackupStorageUsed {
		return "", false, nil
	}
	storagePath := path.Join(path.Dir(s.StorageFilepath), path.Base(filepath))
	location, _, err := s.Upload(filepath, storagePath, types.OutputTypeJSON, false)
	return location, true, err
}

func (s *RoomTracksMCAPSink) Close() error {
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
	location, size, err := s.Upload(s.LocalFilepath, s.StorageFilepath, s.OutputType, false)
	if err != nil {
		logger.Debugw("room-tracks MCAP upload failed", err)
		return err
	}
	s.FileInfo.Location = location
	s.FileInfo.Size = size
	if s.conf.Manifest != nil {
		s.conf.Manifest.AddFile(s.StorageFilepath, location)
	}
	return nil
}

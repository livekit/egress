// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package builder

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
)

type MCAPBin struct {
	*gstreamer.Bin
	SinkElements []*gst.Element
}

func BuildMCAPBin(
	pipeline *gstreamer.Pipeline,
	p *config.PipelineConfig,
	audioCallbacks, videoCallbacks *app.SinkCallbacks,
) (*MCAPBin, error) {
	b := pipeline.NewBin("mcap")
	var (
		audioSink *app.Sink
		videoSink *app.Sink
		audioPath []*gst.Element
		videoPath []*gst.Element
		all       []*gst.Element
	)

	if p.AudioEnabled {
		if p.AudioOutCodec != types.MimeTypeOpus {
			return nil, errors.ErrIncompatible(types.OutputTypeMCAP, p.AudioOutCodec)
		}
		opusParse, err := gst.NewElement("opusparse")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		audioSink, err = app.NewAppSink()
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		audioSink.SetCallbacks(audioCallbacks)
		if err = configureMCAPAppSink(audioSink); err != nil {
			return nil, err
		}
		audioPath = []*gst.Element{opusParse, audioSink.Element}
		all = append(all, audioPath...)
	}

	if p.VideoEnabled {
		if p.VideoOutCodec != types.MimeTypeH264 {
			return nil, errors.ErrIncompatible(types.OutputTypeMCAP, p.VideoOutCodec)
		}
		h264Parse, err := gst.NewElement("h264parse")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = h264Parse.SetProperty("config-interval", -1); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = caps.SetProperty("caps", gst.NewCapsFromString(
			"video/x-h264,stream-format=byte-stream,alignment=au",
		)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		videoSink, err = app.NewAppSink()
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		videoSink.SetCallbacks(videoCallbacks)
		if err = configureMCAPAppSink(videoSink); err != nil {
			return nil, err
		}
		videoPath = []*gst.Element{h264Parse, caps, videoSink.Element}
		all = append(all, videoPath...)
	}

	if err := b.AddElements(all...); err != nil {
		return nil, err
	}
	b.SetLinkFunc(func(_ []*gst.Element) error {
		if len(audioPath) > 0 {
			if err := gst.ElementLinkMany(audioPath...); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}
		if len(videoPath) > 0 {
			if err := gst.ElementLinkMany(videoPath...); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}
		return nil
	})
	b.SetGetSrcPad(func(srcName string) *gst.Pad {
		switch srcName {
		case audioBinName:
			if audioSink != nil {
				return audioPath[0].GetStaticPad("sink")
			}
		case "video":
			if videoSink != nil {
				return videoPath[0].GetStaticPad("sink")
			}
		}
		return nil
	})

	result := &MCAPBin{Bin: b}
	if audioSink != nil {
		result.SinkElements = append(result.SinkElements, audioSink.Element)
	}
	if videoSink != nil {
		result.SinkElements = append(result.SinkElements, videoSink.Element)
	}
	return result, nil
}

func configureMCAPAppSink(sink *app.Sink) error {
	if err := sink.SetProperty("sync", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := sink.SetProperty("enable-last-sample", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

package web

import (
	"context"
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/pipeline/input/bin"
	"github.com/livekit/egress/pkg/pipeline/params"
)

func (s *WebInput) build(ctx context.Context, p *params.Params) error {
	<-p.GstReady

	audio, err := buildAudioElements(p)
	if err != nil {
		return err
	}
	video, err := buildVideoElements(p)
	if err != nil {
		return err
	}

	s.InputBin = bin.New(audio, video)
	return s.InputBin.Build(ctx, p)
}

func buildAudioElements(p *params.Params) ([]*gst.Element, error) {
	if !p.AudioEnabled {
		return nil, nil
	}

	pulseSrc, err := gst.NewElement("pulsesrc")
	if err != nil {
		return nil, err
	}
	if err = pulseSrc.SetProperty("device", fmt.Sprintf("%s.monitor", p.Info.EgressId)); err != nil {
		return nil, err
	}

	audioQueue, err := bin.BuildQueue()
	if err != nil {
		return nil, err
	}

	encoder, err := bin.BuildAudioEncoder(p)
	if err != nil {
		return nil, err
	}

	return append([]*gst.Element{pulseSrc, audioQueue}, encoder...), nil
}

func buildVideoElements(p *params.Params) ([]*gst.Element, error) {
	if !p.VideoEnabled {
		return nil, nil
	}

	xImageSrc, err := gst.NewElement("ximagesrc")
	if err != nil {
		return nil, err
	}
	if err = xImageSrc.SetProperty("display-name", p.Display); err != nil {
		return nil, err
	}
	if err = xImageSrc.SetProperty("use-damage", false); err != nil {
		return nil, err
	}
	if err = xImageSrc.SetProperty("show-pointer", false); err != nil {
		return nil, err
	}

	videoQueue, err := bin.BuildQueue()
	if err != nil {
		return nil, err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, err
	}

	videoFramerateCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	if err = videoFramerateCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,framerate=%d/1", p.Framerate),
	)); err != nil {
		return nil, err
	}

	encoder, err := bin.BuildVideoEncoder(p)
	if err != nil {
		return nil, err
	}
	return append([]*gst.Element{xImageSrc, videoQueue, videoConvert, videoFramerateCaps}, encoder...), nil
}

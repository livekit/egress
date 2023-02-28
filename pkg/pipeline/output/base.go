package output

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/builder"
)

type outputBase struct {
	audioQueue *gst.Element
	videoQueue *gst.Element
}

func (b *Bin) buildOutputBase(p *config.PipelineConfig) (*outputBase, error) {
	base := &outputBase{}

	if p.AudioEnabled {
		audioQueue, err := builder.BuildQueueWithLatency("audio_out_queue", p.Latency, true)
		if err != nil {
			return nil, err
		}
		if err = b.bin.Add(audioQueue); err != nil {
			return nil, err
		}
		base.audioQueue = audioQueue
	}

	if p.VideoEnabled {
		videoQueue, err := builder.BuildQueueWithLatency("video_out_queue", p.Latency, true)
		if err != nil {
			return nil, err
		}
		if err = b.bin.Add(videoQueue); err != nil {
			return nil, err
		}
		base.videoQueue = videoQueue
	}

	return base, nil
}

func (b *outputBase) CreateGhostPads() (audioPad, videoPad *gst.GhostPad) {
	if b.audioQueue != nil {
		audioPad = gst.NewGhostPad("audio", b.audioQueue.GetStaticPad("sink"))
	}
	if b.videoQueue != nil {
		videoPad = gst.NewGhostPad("video", b.videoQueue.GetStaticPad("sink"))
	}
	return
}

func (b *outputBase) LinkTees(audioTee, videoTee *gst.Element) error {
	if audioTee != nil {
		if err := builder.LinkPads(
			"audio tee", audioTee.GetRequestPad("src_%u"),
			"audio queue", b.audioQueue.GetStaticPad("sink"),
		); err != nil {
			return err
		}
	}
	if videoTee != nil {
		if err := builder.LinkPads(
			"video tee", videoTee.GetRequestPad("src_%u"),
			"video queue", b.videoQueue.GetStaticPad("sink"),
		); err != nil {
			return err
		}
	}
	return nil
}

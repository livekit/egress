// Copyright 2023 LiveKit, Inc.
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

package output

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
)

type outputBase struct {
	audioQueue *gst.Element
	videoQueue *gst.Element
}

func (b *Bin) buildOutputBase(p *config.PipelineConfig, egressType types.EgressType) (*outputBase, error) {
	base := &outputBase{}

	if p.AudioEnabled {
		audioQueue, err := builder.BuildQueue(fmt.Sprintf("audio_%s_queue", egressType), p.Latency, true)
		if err != nil {
			return nil, err
		}
		if err = b.bin.Add(audioQueue); err != nil {
			return nil, err
		}
		base.audioQueue = audioQueue
	}

	if p.VideoEnabled {
		videoQueue, err := builder.BuildQueue(fmt.Sprintf("video_%s_queue", egressType), p.Latency, true)
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

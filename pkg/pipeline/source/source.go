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

package source

import (
	"context"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
)

type Source interface {
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	GetStartedAt() int64
	GetEndedAt() int64
	Close()
}

func New(ctx context.Context, p *config.PipelineConfig, callbacks *gstreamer.Callbacks) (Source, error) {
	switch p.RequestType {
	case types.RequestTypeRoomComposite,
		types.RequestTypeWeb:
		return NewWebSource(ctx, p)

	case types.RequestTypeParticipant,
		types.RequestTypeTrackComposite,
		types.RequestTypeTrack:
		return NewSDKSource(ctx, p, callbacks)

	default:
		return nil, errors.ErrInvalidInput("request")
	}
}

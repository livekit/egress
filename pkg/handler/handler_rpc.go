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

package handler

import (
	"context"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/tracer"
)

func (h *Handler) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) (*livekit.EgressInfo, error) {
	ctx, span := tracer.Start(ctx, "Handler.UpdateStream")
	defer span.End()

	<-h.initialized.Watch()
	if h.controller == nil {
		return nil, errors.ErrEgressNotFound
	}

	err := h.controller.UpdateStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return h.controller.Info, nil
}

func (h *Handler) StopEgress(ctx context.Context, _ *livekit.StopEgressRequest) (*livekit.EgressInfo, error) {
	ctx, span := tracer.Start(ctx, "Handler.StopEgress")
	defer span.End()

	<-h.initialized.Watch()
	if h.controller == nil {
		return nil, errors.ErrEgressNotFound
	}

	h.controller.SendEOS(ctx)
	return h.controller.Info, nil
}

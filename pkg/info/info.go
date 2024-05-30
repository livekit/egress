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

package info

import (
	"net/http"
	"time"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/psrpc"
)

type EgressInfo livekit.EgressInfo

const (
	MsgStartNotReceived         = "Start signal not received"
	MsgLimitReached             = "Session limit reached"
	MsgLimitReachedWithoutStart = "Session limit reached before start signal"
	MsgStoppedBeforeStarted     = "Stop called before pipeline could start"
)

func (e *EgressInfo) UpdateStatus(status livekit.EgressStatus) {
	e.Status = status
	e.UpdatedAt = time.Now().UnixNano()
}

func (e *EgressInfo) SetLimitReached() {
	now := time.Now().UnixNano()
	e.Status = livekit.EgressStatus_EGRESS_LIMIT_REACHED
	e.Error = MsgLimitReached
	e.ErrorCode = int32(http.StatusRequestEntityTooLarge)
	e.UpdatedAt = now
	e.EndedAt = now
}

func (e *EgressInfo) SetAborted(msg string) {
	now := time.Now().UnixNano()
	e.Status = livekit.EgressStatus_EGRESS_ABORTED
	e.Error = msg
	e.ErrorCode = int32(http.StatusPreconditionFailed)
	e.UpdatedAt = now
	e.EndedAt = now
}

func (e *EgressInfo) SetFailed(err error) {
	now := time.Now().UnixNano()
	e.Status = livekit.EgressStatus_EGRESS_FAILED
	e.UpdatedAt = now
	e.EndedAt = now
	e.Error = err.Error()
	var perr psrpc.Error
	if errors.As(err, &perr) {
		// unknown is treated the same as an internal error (500)
		if !errors.Is(perr.Code(), psrpc.Unknown) {
			e.ErrorCode = int32(perr.ToHttp())
		}
	}
}

func (e *EgressInfo) SetComplete() {
	now := time.Now().UnixNano()
	e.Status = livekit.EgressStatus_EGRESS_COMPLETE
	e.UpdatedAt = now
	e.EndedAt = now
}

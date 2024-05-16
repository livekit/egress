package info

import (
	"errors"
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

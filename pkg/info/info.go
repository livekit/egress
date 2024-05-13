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
	e.Details = MsgLimitReached
	e.UpdatedAt = now
	e.EndedAt = now
}

func (e *EgressInfo) SetAborted(msg string) {
	now := time.Now().UnixNano()
	e.Status = livekit.EgressStatus_EGRESS_ABORTED
	if e.Details == "" {
		e.Details = msg
	} else {
		e.Details = e.Details + "; " + msg
	}
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
		e.ErrorCode = int32(perr.ToHttp())
	} else {
		e.ErrorCode = int32(http.StatusInternalServerError)
	}
}

func (e *EgressInfo) SetComplete() {
	now := time.Now().UnixNano()
	e.Status = livekit.EgressStatus_EGRESS_COMPLETE
	e.UpdatedAt = now
	e.EndedAt = now
}

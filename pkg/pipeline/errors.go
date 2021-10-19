package pipeline

import (
	"errors"
)

var (
	ErrPipelineNotFound     = errors.New("pipeline not initialized")
	ErrCannotAddToFile      = errors.New("cannot add rtmp output to file recording")
	ErrCannotRemoveFromFile = errors.New("cannot remove rtmp output from file recording")
	ErrGhostPadFailed       = errors.New("failed to add ghost pad to bin")
	ErrOutputAlreadyExists  = errors.New("output already exists")
	ErrOutputNotFound       = errors.New("output not found")

	GErrNoURI            = "No URI set before starting"
	GErrFailedToStart    = "Failed to start"
	GErrCouldNotConnect  = "Could not connect to RTMP stream"
	GErrStreamingStopped = "streaming stopped, reason error (-5)"
)

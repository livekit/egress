package pipeline

import "errors"

var (
	ErrCannotAddToFile      = errors.New("cannot add rtmp output to file recording")
	ErrCannotRemoveFromFile = errors.New("cannot remove rtmp output from file recording")
	ErrGhostPadFailed       = errors.New("failed to add ghost pad to bin")
	ErrOutputAlreadyExists  = errors.New("output already exists")
	ErrOutputNotFound       = errors.New("output not found")
)

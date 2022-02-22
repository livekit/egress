package errors

import (
	"errors"
	"fmt"
)

var (
	ErrNoConfig = errors.New("missing config")

	ErrInvalidInput        = errors.New("request missing required field")
	ErrInvalidURL          = errors.New("invalid output url")
	ErrInvalidRPC          = errors.New("invalid request")
	ErrGhostPadFailed      = errors.New("failed to add ghost pad to bin")
	ErrOutputAlreadyExists = errors.New("output already exists")
	ErrOutputNotFound      = errors.New("output not found")

	GErrNoURI            = "No URI set before starting"
	GErrFailedToStart    = "Failed to start"
	GErrCouldNotConnect  = "Could not connect to RTMP stream"
	GErrStreamingStopped = "streaming stopped, reason error (-5)"
)

func New(err string) error {
	return errors.New(err)
}

func ErrNotSupported(feature string) error {
	return fmt.Errorf("support for %s is coming soon", feature)
}

func ErrIncompatible(format, codec interface{}) error {
	return fmt.Errorf("format %v incompatible with codec %v", format, codec)
}

package errors

import (
	"errors"
	"fmt"
	"strings"

	"github.com/livekit/protocol/livekit"
)

var (
	ErrNoConfig            = errors.New("missing config")
	ErrInvalidRPC          = errors.New("invalid request")
	ErrGhostPadFailed      = errors.New("failed to add ghost pad to bin")
	ErrStreamAlreadyExists = errors.New("stream already exists")
	ErrStreamNotFound      = errors.New("stream not found")

	GErrNoURI            = "No URI set before starting"
	GErrFailedToStart    = "Failed to start"
	GErrCouldNotConnect  = "Could not connect to RTMP stream"
	GErrStreamingStopped = "streaming stopped, reason error (-5)"
)

func New(err string) error {
	return errors.New(err)
}

func ErrCouldNotParseConfig(err error) error {
	return fmt.Errorf("could not parse config: %v", err)
}

func ErrNotSupported(feature string) error {
	return fmt.Errorf("%s is not yet supported", feature)
}

func ErrIncompatible(format, codec interface{}) error {
	return fmt.Errorf("format %v incompatible with codec %v", format, codec)
}

func ErrInvalidInput(field string) error {
	return fmt.Errorf("request missing required field: %s", field)
}

func ErrInvalidUrl(url string, protocol livekit.StreamProtocol) error {
	return fmt.Errorf("invalid %s url: %s", strings.ToLower(protocol.String()), url)
}

func ErrTrackNotFound(trackID string) error {
	return fmt.Errorf("track %s not found", trackID)
}

func ErrPadLinkFailed(pad, status string) error {
	return fmt.Errorf("%s pad link failed: %s", pad, status)
}

func ErrUploadFailed(status string) error {
	return fmt.Errorf("file upload failed: %s", status)
}

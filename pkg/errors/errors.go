package errors

import (
	"errors"
	"fmt"

	"github.com/livekit/psrpc"
)

var (
	ErrNoConfig            = psrpc.NewError(psrpc.Internal, errors.New("missing config"))
	ErrInvalidRPC          = psrpc.NewError(psrpc.MalformedRequest, errors.New("invalid request"))
	ErrGhostPadFailed      = psrpc.NewError(psrpc.Internal, errors.New("failed to add ghost pad to bin"))
	ErrStreamAlreadyExists = psrpc.NewError(psrpc.AlreadyExists, errors.New("stream already exists"))
	ErrStreamNotFound      = psrpc.NewError(psrpc.NotFound, errors.New("stream not found"))
	ErrEgressNotFound      = psrpc.NewError(psrpc.NotFound, errors.New("egress not found"))
)

func New(err string) error {
	return errors.New(err)
}

func Is(err, target error) bool {
	return errors.Is(err, target)
}

type FatalError struct {
	err error
}

func (e *FatalError) Error() string {
	return fmt.Sprintf("FATAL: %s", e.err.Error())
}

func (e *FatalError) Unwrap() error {
	return e.err
}

func Fatal(err error) error {
	return &FatalError{err}
}

func IsFatal(err error) bool {
	e := &FatalError{}

	return errors.As(err, &e)
}

func ErrCouldNotParseConfig(err error) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "could not parse config: %v", err)
}

func ErrNotSupported(feature string) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "%s is not yet supported", feature)
}

func ErrIncompatible(format, codec interface{}) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "format %v incompatible with codec %v", format, codec)
}

func ErrInvalidInput(field string) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "request has missing or invalid field: %s", field)
}

func ErrInvalidUrl(url, protocol string) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "invalid %s url: %s", protocol, url)
}

func ErrTrackNotFound(trackID string) error {
	return psrpc.NewErrorf(psrpc.NotFound, "track %s not found", trackID)
}

func ErrPadLinkFailed(src, sink, status string) error {
	return psrpc.NewErrorf(psrpc.Internal, "failed to link %s to %s: %s", src, sink, status)
}

// This can have many reasons, some related to invalid paramemters, other because of system failure.
// Do not provide an error code until we have code to analyze the error from the underlying upload library further.
func ErrUploadFailed(location string, err error) error {
	return errors.New("%s upload failed: %v", location, err)
}

func ErrWebSocketClosed(addr string) error {
	return psrpc.NewErrorf(psrpc.Internal, "websocket already closed: %s", addr)
}

func ErrProcessStartFailed(err error) error {
	return psrpc.NewError(psrpc.Internal, err)
}

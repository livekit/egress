package errors

import (
	"errors"
	"fmt"
	"strings"

	"github.com/livekit/psrpc"
)

var (
	ErrNoConfig                   = psrpc.NewErrorf(psrpc.Internal, "missing config")
	ErrInvalidRPC                 = psrpc.NewErrorf(psrpc.MalformedRequest, "invalid request")
	ErrGhostPadFailed             = psrpc.NewErrorf(psrpc.Internal, "failed to add ghost pad to bin")
	ErrStreamAlreadyExists        = psrpc.NewErrorf(psrpc.AlreadyExists, "stream already exists")
	ErrNonStreamingPipeline       = psrpc.NewErrorf(psrpc.InvalidArgument, "UpdateStream called on non-streaming egress")
	ErrEgressNotFound             = psrpc.NewErrorf(psrpc.NotFound, "egress not found")
	ErrProfileNotFound            = psrpc.NewErrorf(psrpc.NotFound, "profile not found")
	ErrNoCompatibleCodec          = psrpc.NewErrorf(psrpc.InvalidArgument, "no supported codec is compatible with all outputs")
	ErrNoCompatibleFileOutputType = psrpc.NewErrorf(psrpc.InvalidArgument, "no supported file output type is compatible with the selected codecs")
)

func New(err string) error {
	return errors.New(err)
}

func Is(err, target error) bool {
	return errors.Is(err, target)
}

func As(err error, target any) bool {
	return errors.As(err, target)
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

func ErrInvalidUrl(url string, reason string) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "invalid url %s: %s", url, reason)
}

func ErrStreamNotFound(url string) error {
	return psrpc.NewErrorf(psrpc.NotFound, "stream %s not found", url)
}

func ErrTrackNotFound(trackID string) error {
	return psrpc.NewErrorf(psrpc.NotFound, "track %s not found", trackID)
}

func ErrPadLinkFailed(src, sink, status string) error {
	return psrpc.NewErrorf(psrpc.Internal, "failed to link %s to %s: %s", src, sink, status)
}

func ErrGstPipelineError(err error) error {
	return psrpc.NewError(psrpc.Internal, err)
}

// This can have many reasons, some related to invalid parameters, other because of system failure.
// Do not provide an error code until we have code to analyze the error from the underlying upload library further.
func ErrUploadFailed(location string, err error) error {
	return psrpc.NewErrorf(psrpc.Unknown, "%s upload failed: %v", location, err)
}

func ErrWebsocketClosed(addr string) error {
	return psrpc.NewErrorf(psrpc.Internal, "websocket already closed: %s", addr)
}

func ErrProcessStartFailed(err error) error {
	return psrpc.NewError(psrpc.Internal, err)
}

type ErrArray struct {
	errs []error
}

func (e *ErrArray) AppendErr(err error) {
	e.errs = append(e.errs, err)
}

func (e *ErrArray) ToError() psrpc.Error {
	if len(e.errs) == 0 {
		return nil
	}

	code := psrpc.Unknown
	var errStr []string

	// Return the code for the first error of type psrpc.Error
	for _, err := range e.errs {
		var psrpcErr psrpc.Error

		if code == psrpc.Unknown && errors.As(err, &psrpcErr) {
			code = psrpcErr.Code()
		}

		errStr = append(errStr, err.Error())
	}

	return psrpc.NewErrorf(code, "%s", strings.Join(errStr, "\n"))
}

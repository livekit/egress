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

package errors

import (
	"errors"
	"strings"

	"github.com/livekit/psrpc"
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

type ErrArray struct {
	errs []error
}

func (e *ErrArray) AppendErr(err error) {
	e.errs = append(e.errs, err)
}

func (e *ErrArray) Check(err error) {
	if err != nil {
		e.errs = append(e.errs, err)
	}
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

// internal errors

var (
	ErrNoConfig        = psrpc.NewErrorf(psrpc.Internal, "missing config")
	ErrGhostPadFailed  = psrpc.NewErrorf(psrpc.Internal, "failed to add ghost pad to bin")
	ErrBinAlreadyAdded = psrpc.NewErrorf(psrpc.Internal, "bin already added to pipeline")
	ErrWrongHierarchy  = psrpc.NewErrorf(psrpc.Internal, "pipeline can contain bins or elements, not both")
	ErrPipelineFrozen  = psrpc.NewErrorf(psrpc.Internal, "pipeline frozen")
	ErrSinkNotFound    = psrpc.NewErrorf(psrpc.Internal, "sink not found")
)

func ErrPadLinkFailed(src, sink, status string) error {
	return psrpc.NewErrorf(psrpc.Internal, "failed to link %s to %s: %s", src, sink, status)
}

func ErrGstPipelineError(err error) error {
	return psrpc.NewError(psrpc.Internal, err)
}

func ErrProcessFailed(process string, err error) error {
	return psrpc.NewErrorf(psrpc.Internal, "failed to launch %s: %v", process, err)
}

func ChromeError(err error) error {
	return psrpc.NewError(psrpc.Internal, err)
}

// other errors

var (
	ErrNonStreamingPipeline       = psrpc.NewErrorf(psrpc.InvalidArgument, "UpdateStream called on non-streaming egress")
	ErrNoCompatibleCodec          = psrpc.NewErrorf(psrpc.InvalidArgument, "no supported codec is compatible with all outputs")
	ErrNoCompatibleFileOutputType = psrpc.NewErrorf(psrpc.InvalidArgument, "no supported file output type is compatible with the selected codecs")
	ErrEgressNotFound             = psrpc.NewErrorf(psrpc.NotFound, "egress not found")
	ErrEgressAlreadyExists        = psrpc.NewErrorf(psrpc.AlreadyExists, "egress already exists")
	ErrSubscriptionFailed         = psrpc.NewErrorf(psrpc.Unavailable, "failed to subscribe to track")
	ErrNotEnoughCPU               = psrpc.NewErrorf(psrpc.Unavailable, "not enough CPU")
	ErrShuttingDown               = psrpc.NewErrorf(psrpc.Unavailable, "server is shutting down")
)

func PageLoadError(err string) error {
	if strings.HasPrefix(err, "page load error ") {
		err = err[16:]
	}
	return psrpc.NewErrorf(psrpc.InvalidArgument, "page load error: %s", err)
}

func TemplateError(err string) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "template error: %s", err)
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

func ErrUploadFailed(location string, err error) error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "%s upload failed: %v", location, err)
}

func ErrParticipantNotFound(identity string) error {
	return psrpc.NewErrorf(psrpc.NotFound, "participant %s not found", identity)
}

func ErrStreamNotFound(url string) error {
	return psrpc.NewErrorf(psrpc.NotFound, "stream %s not found", url)
}

func ErrTrackNotFound(trackID string) error {
	return psrpc.NewErrorf(psrpc.NotFound, "track %s not found", trackID)
}

func ErrFeatureDisabled(feature string) error {
	return psrpc.NewErrorf(psrpc.PermissionDenied, "%s is disabled for this account", feature)
}

func ErrCPUExhausted(usage float64) error {
	return psrpc.NewErrorf(psrpc.PermissionDenied, "CPU exhausted: %.2f cores used", usage)
}

func ErrOOM(usage float64) error {
	return psrpc.NewErrorf(psrpc.PermissionDenied, "OOM: %.2f GB used", usage)
}

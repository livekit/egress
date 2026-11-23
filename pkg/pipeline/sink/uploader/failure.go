// Copyright 2026 LiveKit, Inc.
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

package uploader

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/psrpc"
	"github.com/livekit/storage"
)

// uploadFailed reports a failed upload by its status and error code. The
// SDK's error stays in the chain.
func uploadFailed(storageName string, err error) error {
	return psrpc.NewError(psrpc.InvalidArgument,
		errors.WithReportedMessage(err, storageName+" upload failed: "+uploadFailure(err)))
}

func uploadFailure(err error) string {
	var status *storage.ErrorWithStatusCode
	if errors.As(err, &status) {
		return statusFailure(status.StatusCode, status.Code)
	}
	if reason := errors.NetworkFailure(err); reason != "" {
		return reason
	}
	return "request failed"
}

// statusFailure is an HTTP status with the store's error code, or the
// status's own name where the store gave none.
func statusFailure(status int, code string) string {
	switch {
	case status < http.StatusBadRequest:
		return fmt.Sprintf("unexpected status %d", status)
	case code != "":
		return fmt.Sprintf("%d %s", status, code)
	case http.StatusText(status) != "":
		return fmt.Sprintf("%d %s", status, http.StatusText(status))
	}
	return strconv.Itoa(status)
}

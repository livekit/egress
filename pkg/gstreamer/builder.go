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

package gstreamer

import (
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
)

func BuildQueue(name string, latency uint64, leaky bool) (*gst.Element, error) {
	queue, err := gst.NewElementWithName("queue", name)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if latency > 0 {
		if err = queue.SetProperty("max-size-time", latency); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = queue.SetProperty("max-size-bytes", uint(0)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = queue.SetProperty("max-size-buffers", uint(0)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}
	if leaky {
		queue.SetArg("leaky", "downstream")
	}

	return queue, nil
}

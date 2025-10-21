// Copyright 2025 LiveKit, Inc.
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
	"time"
)

// TimeProvider supplies the running time and playhead position of a pipeline.
type TimeProvider interface {
	RunningTime() (time.Duration, bool)
	PlayheadPosition() (time.Duration, bool)
}

var _ TimeProvider = (*nopTimeProvider)(nil)

type nopTimeProvider struct{}

// NopTimeProvider returns a TimeProvider that always reports unavailable times.
func NopTimeProvider() TimeProvider {
	return &nopTimeProvider{}
}

func (n *nopTimeProvider) RunningTime() (time.Duration, bool) {
	return 0, false
}

func (n *nopTimeProvider) PlayheadPosition() (time.Duration, bool) {
	return 0, false
}

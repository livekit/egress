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

package config

import (
	"time"

	"github.com/livekit/egress/pkg/types"
)

// minImageKeyframeInterval is the shortest capture interval decoded from
// keyframes only. Each capture may cost a keyframe request to the publisher,
// and the keyframe then reaches every subscriber of that layer.
const minImageKeyframeInterval = 2 * time.Second

// ImageKeyframeInterval returns how often an image-only egress should decode a
// keyframe, or zero when every frame must be decoded: the option is off, an
// output other than images needs the full stream, or a capture interval is
// below minImageKeyframeInterval. With several image outputs it returns the
// greatest common divisor of their intervals, so every output's capture slots
// fall on the schedule.
func (p *PipelineConfig) ImageKeyframeInterval() time.Duration {
	if !p.ImageKeyframeDecoding {
		return 0
	}

	var interval time.Duration
	for egressType, outputs := range p.Outputs {
		if egressType != types.EgressTypeImages {
			return 0
		}
		for _, o := range outputs {
			ic, ok := o.(*ImageConfig)
			if !ok {
				return 0
			}
			interval = gcd(interval, time.Duration(ic.CaptureInterval)*time.Second)
		}
	}
	if interval < minImageKeyframeInterval {
		return 0
	}
	return interval
}

func gcd(a, b time.Duration) time.Duration {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

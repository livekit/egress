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

//go:build integration

package test

import "github.com/livekit/egress/pkg/types"

const (
	runRoom           = 0b1 << 0
	runWeb            = 0b1 << 1
	runParticipant    = 0b1 << 2
	runTrackComposite = 0b1 << 3
	runTrack          = 0b1 << 4

	runAllRequests = 0b11111

	runFile     = 0b1 << 31
	runStream   = 0b1 << 30
	runSegments = 0b1 << 29
	runImages   = 0b1 << 28
	runMulti    = 0b1 << 27
	runEdge     = 0b1 << 26

	runAllOutputs = 0b111111 << 26
)

var runRequestType = map[types.RequestType]uint{
	types.RequestTypeRoomComposite:  runRoom,
	types.RequestTypeWeb:            runWeb,
	types.RequestTypeParticipant:    runParticipant,
	types.RequestTypeTrackComposite: runTrackComposite,
	types.RequestTypeTrack:          runTrack,
}

func (r *Runner) updateFlagset() {
	switch {
	case r.RoomTestsOnly:
		r.shouldRun |= runRoom
	case r.ParticipantTestsOnly:
		r.shouldRun |= runParticipant
	case r.WebTestsOnly:
		r.shouldRun |= runWeb
	case r.TrackCompositeTestsOnly:
		r.shouldRun |= runTrackComposite
	case r.TrackTestsOnly:
		r.shouldRun |= runTrack
	default:
		r.shouldRun |= runAllRequests
	}

	switch {
	case r.FileTestsOnly:
		r.shouldRun |= runFile
	case r.StreamTestsOnly:
		r.shouldRun |= runStream
	case r.SegmentTestsOnly:
		r.shouldRun |= runSegments
	case r.ImageTestsOnly:
		r.shouldRun |= runImages
	case r.MultiTestsOnly:
		r.shouldRun |= runMulti
	case r.EdgeCasesOnly:
		r.shouldRun |= runEdge
	default:
		r.shouldRun |= runAllOutputs
	}
}

func (r *Runner) should(runFlag uint) bool {
	return r.shouldRun&runFlag > 0
}

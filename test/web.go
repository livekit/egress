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

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func (r *Runner) testWeb(t *testing.T) {
	if !r.runWebTests() {
		return
	}

	r.sourceFramerate = 30
	r.testWebFile(t)
	r.testWebStream(t)
	r.testWebSegments(t)
	r.testWebMulti(t)
}

func (r *Runner) runWebTest(t *testing.T, name string, f func(t *testing.T)) {
	t.Run(name, func(t *testing.T) {
		r.awaitIdle(t)
		f(t)
	})
}

func (r *Runner) testWebFile(t *testing.T) {
	if !r.runFileTests() {
		return
	}

	r.runWebTest(t, "2A/Web/File", func(t *testing.T) {
		fileOutput := &livekit.EncodedFileOutput{
			Filepath: r.getFilePath("web_{time}"),
		}
		if r.GCPUpload != nil {
			fileOutput.Filepath = "web_{time}"
			fileOutput.Output = &livekit.EncodedFileOutput_Gcp{
				Gcp: r.GCPUpload,
			}
		}

		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url:         webUrl,
					VideoOnly:   true,
					FileOutputs: []*livekit.EncodedFileOutput{fileOutput},
				},
			},
		}

		r.runFileTest(t, req, &testCase{
			expectVideoEncoding: true,
		})
	})
}

func (r *Runner) testWebStream(t *testing.T) {
	if !r.runStreamTests() {
		return
	}

	r.runWebTest(t, "2B/Web/Stream", func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url: webUrl,
					StreamOutputs: []*livekit.StreamOutput{{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{badStreamUrl1, streamUrl1},
					}},
				},
			},
		}

		r.runStreamTest(t, req, &testCase{
			expectVideoEncoding: true,
		})
	})
}

func (r *Runner) testWebSegments(t *testing.T) {
	if !r.runSegmentTests() {
		return
	}

	r.runWebTest(t, "2C/Web/Segments", func(t *testing.T) {
		segmentOutput := &livekit.SegmentedFileOutput{
			FilenamePrefix: r.getFilePath("web_{time}"),
			PlaylistName:   "web_{time}.m3u8",
		}
		if r.AzureUpload != nil {
			segmentOutput.FilenamePrefix = "web_{time}"
			segmentOutput.Output = &livekit.SegmentedFileOutput_Azure{
				Azure: r.AzureUpload,
			}
		}

		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url:            webUrl,
					SegmentOutputs: []*livekit.SegmentedFileOutput{segmentOutput},
				},
			},
		}

		r.runSegmentsTest(t, req, &testCase{
			expectVideoEncoding: true,
		})
	})
}

func (r *Runner) testWebMulti(t *testing.T) {
	if !r.runMultiTests() {
		return
	}

	r.runWebTest(t, "2D/Web/Multi", func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),

			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url: webUrl,
					FileOutputs: []*livekit.EncodedFileOutput{{
						FileType: livekit.EncodedFileType_MP4,
						Filepath: r.getFilePath("web_multiple_{time}"),
					}},
					SegmentOutputs: []*livekit.SegmentedFileOutput{{
						FilenamePrefix: r.getFilePath("web_multiple_{time}"),
						PlaylistName:   "web_multiple_{time}",
					}},
				},
			},
		}

		r.runMultipleTest(t, req, true, false, true, livekit.SegmentedFileSuffix_INDEX)
	})
}

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
	"path"
	"testing"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

const webUrl = "https://videoplayer-2k23.vercel.app/videos/eminem"

func (r *Runner) testWeb(t *testing.T) {
	if !r.should(runWeb) {
		return
	}

	r.sourceFramerate = 30
	t.Run("Web", func(t *testing.T) {
		r.testWebFile(t)
		// r.testWebStream(t)
		r.testWebSegments(t)
		r.testWebMulti(t)
	})
}

func (r *Runner) testWebFile(t *testing.T) {
	if !r.should(runFile) {
		return
	}

	r.run(t, "File", func(t *testing.T) {
		var fileOutput *livekit.EncodedFileOutput
		if r.GCPUpload != nil {
			fileOutput = &livekit.EncodedFileOutput{
				Filepath: path.Join(uploadPrefix, "web_{time}"),
				Output: &livekit.EncodedFileOutput_Gcp{
					Gcp: r.GCPUpload,
				},
			}
		} else {
			fileOutput = &livekit.EncodedFileOutput{
				Filepath: path.Join(r.FilePrefix, "web_{time}"),
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
	if !r.should(runStream) {
		return
	}

	r.run(t, "Stream", func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url: webUrl,
					StreamOutputs: []*livekit.StreamOutput{{
						Protocol: livekit.StreamProtocol_SRT,
						Urls:     []string{srtPublishUrl1, badSrtUrl1},
					}},
				},
			},
		}

		r.runStreamTest(t, req, &testCase{
			expectVideoEncoding: true,
			outputType:          types.OutputTypeSRT,
		})
	})
}

func (r *Runner) testWebSegments(t *testing.T) {
	if !r.should(runSegments) {
		return
	}

	r.run(t, "Segments", func(t *testing.T) {
		var segmentOutput *livekit.SegmentedFileOutput
		if r.AzureUpload != nil {
			segmentOutput = &livekit.SegmentedFileOutput{
				FilenamePrefix: path.Join(uploadPrefix, "web_{time}"),
				PlaylistName:   "web_{time}.m3u8",
				Output: &livekit.SegmentedFileOutput_Azure{
					Azure: r.AzureUpload,
				},
			}
		} else {
			segmentOutput = &livekit.SegmentedFileOutput{
				FilenamePrefix: path.Join(r.FilePrefix, "web_{time}"),
				PlaylistName:   "web_{time}.m3u8",
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
	if !r.should(runMulti) {
		return
	}

	r.run(t, "Multi", func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),

			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url: webUrl,
					FileOutputs: []*livekit.EncodedFileOutput{{
						FileType: livekit.EncodedFileType_MP4,
						Filepath: path.Join(r.FilePrefix, "web_multiple_{time}"),
					}},
					SegmentOutputs: []*livekit.SegmentedFileOutput{{
						FilenamePrefix: path.Join(r.FilePrefix, "web_multiple_{time}"),
						PlaylistName:   "web_multiple_{time}",
					}},
				},
			},
		}

		r.runMultipleTest(t, req, true, false, true, false, livekit.SegmentedFileSuffix_INDEX)
	})
}

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

	r.runWebTest(t, "Web/File", func(t *testing.T) {
		fileOutput := &livekit.EncodedFileOutput{
			Filepath: getFilePath(r.ServiceConfig, "web_{time}"),
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
			expectVideoTranscoding: true,
		})
	})
}

func (r *Runner) testWebStream(t *testing.T) {
	if !r.runStreamTests() {
		return
	}

	r.runWebTest(t, "Web/Stream", func(t *testing.T) {
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
			expectVideoTranscoding: true,
		})
	})
}

func (r *Runner) testWebSegments(t *testing.T) {
	if !r.runSegmentTests() {
		return
	}

	r.runWebTest(t, "Web/Segments", func(t *testing.T) {
		segmentOutput := &livekit.SegmentedFileOutput{
			FilenamePrefix: getFilePath(r.ServiceConfig, "web_{time}"),
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
			expectVideoTranscoding: true,
		})
	})
}

func (r *Runner) testWebMulti(t *testing.T) {
	if !r.runMultiTests() {
		return
	}

	r.runWebTest(t, "Web/Multi", func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),

			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url: webUrl,
					FileOutputs: []*livekit.EncodedFileOutput{{
						FileType: livekit.EncodedFileType_MP4,
						Filepath: getFilePath(r.ServiceConfig, "web-multiple"),
					}},
					SegmentOutputs: []*livekit.SegmentedFileOutput{{
						FilenamePrefix: getFilePath(r.ServiceConfig, "web_multiple_{time}"),
						PlaylistName:   "web_multiple_{time}",
					}},
				},
			},
		}

		r.runMultipleTest(t, req, true, false, true, livekit.SegmentedFileSuffix_INDEX)
	})
}

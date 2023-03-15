//go:build integration

package test

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func testWebFile(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	fileOutput := &livekit.EncodedFileOutput{
		Filepath: getFilePath(conf.ServiceConfig, "web_{time}"),
	}
	if conf.GCPUpload != nil {
		fileOutput.Filepath = "web_{time}"
		fileOutput.Output = &livekit.EncodedFileOutput_Gcp{
			Gcp: conf.GCPUpload,
		}
	}

	req := &rpc.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &rpc.StartEgressRequest_Web{
			Web: &livekit.WebEgressRequest{
				Url:         webUrl,
				FileOutputs: []*livekit.EncodedFileOutput{fileOutput},
			},
		},
	}

	runFileTest(t, conf, req, &testCase{
		expectVideoTranscoding: true,
	})
}

func testWebStream(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	req := &rpc.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &rpc.StartEgressRequest_Web{
			Web: &livekit.WebEgressRequest{
				Url: webUrl,
				StreamOutputs: []*livekit.StreamOutput{{
					Protocol: livekit.StreamProtocol_RTMP,
					Urls:     []string{streamUrl1},
				}},
			},
		},
	}

	runStreamTest(t, conf, req, &testCase{
		expectVideoTranscoding: true,
	})
}

func testWebSegments(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	req := &rpc.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &rpc.StartEgressRequest_Web{
			Web: &livekit.WebEgressRequest{
				Url: webUrl,
				SegmentOutputs: []*livekit.SegmentedFileOutput{{
					FilenamePrefix: getFilePath(conf.ServiceConfig, "web_{time}"),
					PlaylistName:   "web_{time}.m3u8",
				}},
			},
		},
	}

	runSegmentsTest(t, conf, req, &testCase{
		expectVideoTranscoding: true,
	})
}

func testWebMulti(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	req := &rpc.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),

		Request: &rpc.StartEgressRequest_Web{
			Web: &livekit.WebEgressRequest{
				Url: webUrl,
				FileOutputs: []*livekit.EncodedFileOutput{{
					FileType: livekit.EncodedFileType_MP4,
					Filepath: getFilePath(conf.ServiceConfig, "web-multiple"),
				}},
				SegmentOutputs: []*livekit.SegmentedFileOutput{{
					FilenamePrefix: getFilePath(conf.ServiceConfig, "web_multiple_{time}"),
					PlaylistName:   "web_multiple_{time}",
				}},
			},
		},
	}

	runMultipleTest(t, conf, req, true, false, true, livekit.SegmentedFileSuffix_INDEX)
}

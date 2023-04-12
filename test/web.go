//go:build integration

package test

import (
	"testing"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func runWebTests(t *testing.T, conf *TestConfig) {
	if !conf.runWebTests {
		return
	}

	conf.sourceFramerate = 30

	if conf.runFileTests {
		runWebTest(t, conf, "Web/File", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
			testWebFile(t, conf)
		})
	}

	if conf.runStreamTests {
		runWebTest(t, conf, "Web/Stream", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
			testWebStream(t, conf)
		})
	}

	if conf.runSegmentTests {
		runWebTest(t, conf, "Web/Segments", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
			testWebSegments(t, conf)
		})
	}

	if conf.runMultiTests {
		runWebTest(t, conf, "Web/Multi", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
			testWebMulti(t, conf)
		})
	}
}

func testWebFile(t *testing.T, conf *TestConfig) {
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
	segmentOutput := &livekit.SegmentedFileOutput{
		FilenamePrefix: getFilePath(conf.ServiceConfig, "web_{time}"),
		PlaylistName:   "web_{time}.m3u8",
	}
	if conf.AzureUpload != nil {
		segmentOutput.FilenamePrefix = "web_{time}"
		segmentOutput.Output = &livekit.SegmentedFileOutput_Azure{
			Azure: conf.AzureUpload,
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

	runSegmentsTest(t, conf, req, &testCase{
		expectVideoTranscoding: true,
	})
}

func testWebMulti(t *testing.T, conf *TestConfig) {
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

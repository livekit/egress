//go:build integration

package test

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testWebFile(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	webRequest := &livekit.WebEgressRequest{
		Url: webUrl,
		Output: &livekit.WebEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				Filepath: getFilePath(conf.Config, "web_{time}"),
			},
		},
	}

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Web{
			Web: webRequest,
		},
	}

	runFileTest(t, conf, req, &testCase{})
}

func testWebStream(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Web{
			Web: &livekit.WebEgressRequest{
				Url: webUrl,
				Output: &livekit.WebEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{streamUrl1},
					},
				},
			},
		},
	}

	runStreamTest(t, conf, req, 0)
}

func testWebSegments(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	webRequest := &livekit.WebEgressRequest{
		Url: webUrl,
		Output: &livekit.WebEgressRequest_Segments{
			Segments: &livekit.SegmentedFileOutput{
				FilenamePrefix: getFilePath(conf.Config, "web_{time}"),
				PlaylistName:   "web_{time}.m3u8",
			},
		},
	}

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Web{
			Web: webRequest,
		},
	}

	runSegmentsTest(t, conf, req, 0)
}

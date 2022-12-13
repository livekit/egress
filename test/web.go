//go:build integration

package test

import (
	"testing"

	"github.com/livekit/protocol/livekit"
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

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Web{
			Web: &livekit.WebEgressRequest{
				Url: webUrl,
				Output: &livekit.WebEgressRequest_File{
					File: fileOutput,
				},
			},
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
				FilenamePrefix: getFilePath(conf.ServiceConfig, "web_{time}"),
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

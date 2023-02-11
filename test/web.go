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

	web := &livekit.WebEgressRequest{
		Url: webUrl,
	}
	if conf.V2 {
		web.FileOutputs = []*livekit.EncodedFileOutput{fileOutput}
	} else {
		web.Output = &livekit.WebEgressRequest_File{
			File: fileOutput,
		}
	}
	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Web{
			Web: web,
		},
	}

	runFileTest(t, conf, req, &testCase{
		expectVideoTranscoding: true,
	})
}

func testWebStream(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	web := &livekit.WebEgressRequest{
		Url: webUrl,
	}
	if conf.V2 {
		web.StreamOutputs = []*livekit.StreamOutput{{
			Protocol: livekit.StreamProtocol_RTMP,
			Urls:     []string{streamUrl1},
		}}
	} else {
		web.Output = &livekit.WebEgressRequest_Stream{
			Stream: &livekit.StreamOutput{
				Protocol: livekit.StreamProtocol_RTMP,
				Urls:     []string{streamUrl1},
			},
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Web{
			Web: web,
		},
	}

	runStreamTest(t, conf, req, &testCase{
		expectVideoTranscoding: true,
	})
}

func testWebSegments(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	web := &livekit.WebEgressRequest{
		Url: webUrl,
	}
	if conf.V2 {
		web.SegmentOutputs = []*livekit.SegmentedFileOutput{{
			FilenamePrefix: getFilePath(conf.ServiceConfig, "web_{time}"),
			PlaylistName:   "web_{time}.m3u8",
		}}
	} else {
		web.Output = &livekit.WebEgressRequest_Segments{
			Segments: &livekit.SegmentedFileOutput{
				FilenamePrefix: getFilePath(conf.ServiceConfig, "web_{time}"),
				PlaylistName:   "web_{time}.m3u8",
			},
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Web{
			Web: web,
		},
	}

	runSegmentsTest(t, conf, req, &testCase{
		expectVideoTranscoding: true,
	})
}

func testWebMulti(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),

		Request: &livekit.StartEgressRequest_Web{
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

	runMultipleTest(t, conf, req, true, false, true)
}

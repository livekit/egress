//go:build integration
// +build integration

package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/egress/pkg/pipeline/params"
)

func testTrackComposite(t *testing.T, conf *testConfig, room *lksdk.Room) {
	testTrackCompositeFile(t, conf, room, params.MimeTypeOpus, params.MimeTypeVP8, []*testCase{
		{
			name:     "tc-vp8-mp4",
			fileType: livekit.EncodedFileType_MP4,
			filename: fmt.Sprintf("tc-vp8-%v.mp4", time.Now().Unix()),
		},
		{
			name:      "tc-opus-ogg",
			audioOnly: true,
			fileType:  livekit.EncodedFileType_OGG,
			filename:  fmt.Sprintf("tc-opus-%v.ogg", time.Now().Unix()),
		},
	})

	testTrackCompositeFile(t, conf, room, params.MimeTypeOpus, params.MimeTypeH264, []*testCase{
		{
			name:     "tc-h264-mp4",
			fileType: livekit.EncodedFileType_MP4,
			filename: fmt.Sprintf("tc-h264-%v.mp4", time.Now().Unix()),
		},
		{
			name:      "tc-h264-only-mp4",
			videoOnly: true,
			fileType:  livekit.EncodedFileType_MP4,
			filename:  fmt.Sprintf("tc-h264-only-%v.mp4", time.Now().Unix()),
		},
	})
}

func testTrackCompositeFile(t *testing.T, conf *testConfig, room *lksdk.Room, audioCodec, videoCodec params.MimeType, cases []*testCase) {
	p := &sdkParams{
		audioTrackID: publishSampleToRoom(t, room, audioCodec, false),
		videoTrackID: publishSampleToRoom(t, room, videoCodec, conf.WithMuting),
		roomName:     room.Name,
	}

	for _, test := range cases {
		if !t.Run(test.name, func(t *testing.T) {
			runTrackCompositeFileTest(t, conf, p, test)
		}) {
			t.FailNow()
		}
	}

	require.NoError(t, room.LocalParticipant.UnpublishTrack(p.audioTrackID))
	require.NoError(t, room.LocalParticipant.UnpublishTrack(p.videoTrackID))
}

func runTrackCompositeFileTest(t *testing.T, conf *testConfig, params *sdkParams, test *testCase) {
	var audioTrackID, videoTrackID string
	if !test.videoOnly {
		audioTrackID = params.audioTrackID
	}
	if !test.audioOnly {
		videoTrackID = params.videoTrackID
	}

	filepath := getFilePath(conf.Config, test.filename)
	trackRequest := &livekit.TrackCompositeEgressRequest{
		RoomName:     params.roomName,
		AudioTrackId: audioTrackID,
		VideoTrackId: videoTrackID,
		Output: &livekit.TrackCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: filepath,
			},
		},
	}

	if test.options != nil {
		trackRequest.Options = &livekit.TrackCompositeEgressRequest_Advanced{
			Advanced: test.options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_TrackComposite{
			TrackComposite: trackRequest,
		},
	}

	runFileTest(t, conf, test, req, filepath)
}

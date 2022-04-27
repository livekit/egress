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

	"github.com/livekit/livekit-egress/pkg/config"
)

func testTrackComposite(t *testing.T, conf *config.Config, room *lksdk.Room) {
	testTrackCompositeFile(t, conf, room, "opus", "vp8", []*testCase{
		{
			name:       "tc-vp8-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			filePrefix: "tc-vp8",
		},
		{
			name:       "tc-opus-only-ogg",
			audioOnly:  true,
			fileType:   livekit.EncodedFileType_OGG,
			filePrefix: "tc-opus-only",
		},
	})

	testTrackCompositeFile(t, conf, room, "opus", "h264", []*testCase{
		{
			name:       "tc-h264-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			filePrefix: "tc-h264",
		},
		{
			name:       "tc-h264-only-mp4",
			videoOnly:  true,
			fileType:   livekit.EncodedFileType_MP4,
			filePrefix: "tc-h264-only",
		},
	})
}

func testTrackCompositeFile(t *testing.T, conf *config.Config, room *lksdk.Room, audioCodec, videoCodec string, cases []*testCase) {
	p := publishSamplesToRoom(t, room, audioCodec, videoCodec)

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

func runTrackCompositeFileTest(t *testing.T, conf *config.Config, params *sdkParams, test *testCase) {
	filepath, filename := getFileInfo(conf, test, "tc")

	var audioTrackID, videoTrackID string
	if !test.videoOnly {
		audioTrackID = params.audioTrackID
	}
	if !test.audioOnly {
		videoTrackID = params.videoTrackID
	}

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

	runFileTest(t, conf, test, req, filename)
}

func testTrack(t *testing.T, conf *config.Config, room *lksdk.Room) {
	for _, test := range []*testCase{
		// {
		// 	audioOnly:     true,
		// 	codec:         "opus",
		// 	fileExtension: "ogg",
		// },
		{
			videoOnly:     true,
			codec:         "vp8",
			fileExtension: "ivf",
		},
		{
			videoOnly:     true,
			codec:         "h264",
			fileExtension: "h264",
		},
	} {
		test.filePrefix = fmt.Sprintf("track-%s", test.codec)

		if !t.Run(test.filePrefix, func(t *testing.T) {
			runTrackFileTest(t, conf, room, test)
		}) {
			// t.FailNow()
		}
	}
}

func runTrackFileTest(t *testing.T, conf *config.Config, room *lksdk.Room, test *testCase) {
	var trackID string
	if test.audioOnly {
		p := publishSamplesToRoom(t, room, test.codec, "")
		trackID = p.audioTrackID
	} else {
		p := publishSamplesToRoom(t, room, "", test.codec)
		trackID = p.videoTrackID
	}

	time.Sleep(time.Second * 5)

	filepath, filename := getFileInfo(conf, test, "track")

	trackRequest := &livekit.TrackEgressRequest{
		RoomName: room.Name,
		TrackId:  trackID,
		Output: &livekit.TrackEgressRequest_File{
			File: &livekit.DirectFileOutput{
				Filepath: filepath,
			},
		},
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_Track{
			Track: trackRequest,
		},
	}

	runFileTest(t, conf, test, req, filename)

	require.NoError(t, room.LocalParticipant.UnpublishTrack(trackID))
}

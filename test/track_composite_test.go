//go:build integration
// +build integration

package test

import (
	"fmt"
	"testing"
	"time"

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
	})

	if !t.Run("tc-rtmp", func(t *testing.T) {
		testTrackCompositeStream(t, conf, room)
	}) {
		t.FailNow()
	}
}

func testTrackCompositeFile(t *testing.T, conf *testConfig, room *lksdk.Room, audioCodec, videoCodec params.MimeType, cases []*testCase) {
	audioTrackID := publishSampleToRoom(t, room, audioCodec, false)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(audioTrackID)
	})

	videoTrackID := publishSampleToRoom(t, room, videoCodec, conf.WithMuting)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(videoTrackID)
	})

	for _, test := range cases {
		if !t.Run(test.name, func(t *testing.T) {
			runTrackCompositeFileTest(t, conf, test, audioTrackID, videoTrackID)
		}) {
			t.FailNow()
		}
	}
}

func runTrackCompositeFileTest(t *testing.T, conf *testConfig, test *testCase, audioTrackID, videoTrackID string) {
	var aID, vID string
	if !test.audioOnly {
		vID = videoTrackID
	}
	if !test.videoOnly {
		aID = audioTrackID
	}

	filepath := getFilePath(conf.Config, test.filename)
	trackRequest := &livekit.TrackCompositeEgressRequest{
		RoomName:     conf.RoomName,
		AudioTrackId: aID,
		VideoTrackId: vID,
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

func testTrackCompositeStream(t *testing.T, conf *testConfig, room *lksdk.Room) {
	audioTrackID := publishSampleToRoom(t, room, params.MimeTypeOpus, false)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(audioTrackID)
	})

	videoTrackID := publishSampleToRoom(t, room, params.MimeTypeVP8, conf.WithMuting)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(videoTrackID)
	})

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_TrackComposite{
			TrackComposite: &livekit.TrackCompositeEgressRequest{
				RoomName:     conf.RoomName,
				AudioTrackId: audioTrackID,
				VideoTrackId: videoTrackID,
				Output: &livekit.TrackCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Urls: []string{streamUrl1},
					},
				},
			},
		},
	}

	runStreamTest(t, conf, req)
}

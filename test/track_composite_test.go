//go:build integration
// +build integration

package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/pipeline"
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

	testTrackCompositeStream(t, conf, room)
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
	filepath := getFilePath(conf.Config, test.filename)
	trackRequest := &livekit.TrackCompositeEgressRequest{
		RoomName:     conf.RoomName,
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

func testTrackCompositeStream(t *testing.T, conf *testConfig, room *lksdk.Room) {
	audioTrackID := publishSampleToRoom(t, room, params.MimeTypeOpus, false)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(audioTrackID)
	})

	videoTrackID := publishSampleToRoom(t, room, params.MimeTypeVP8, conf.WithMuting)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(videoTrackID)
	})

	url := "rtmp://localhost:1935/live/stream1"
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
						Urls: []string{url},
					},
				},
			},
		},
	}

	p, err := params.GetPipelineParams(conf.Config, req)
	require.NoError(t, err)

	rec, err := pipeline.New(conf.Config, p)
	require.NoError(t, err)

	defer func() {
		rec.Stop()
		time.Sleep(time.Millisecond * 100)
	}()

	resChan := make(chan *livekit.EgressInfo, 1)
	go func() {
		resChan <- rec.Run()
	}()

	// wait for recorder to start
	time.Sleep(time.Second * 30)

	// check stream
	verifyStreams(t, p, url)

	// add another, check both
	url2 := "rtmp://localhost:1935/live/stream2"
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:      req.EgressId,
		AddOutputUrls: []string{url2},
	}))
	verifyStreams(t, p, url, url2)

	// remove first, check second
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:         req.EgressId,
		RemoveOutputUrls: []string{url},
	}))
	verifyStreams(t, p, url2)

	// stop
	rec.Stop()
	res := <-resChan

	// egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// stream info
	require.Len(t, res.GetStream().Info, 2)
	for _, info := range res.GetStream().Info {
		require.NotEmpty(t, info.Url)
		require.NotZero(t, info.Duration)
	}
}

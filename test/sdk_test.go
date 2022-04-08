//go:build integration
// +build integration

package test

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/livekit-egress/pkg/config"
)

type sdkParams struct {
	audioTrackID string
	videoTrackID string
	roomName     string
	url          string
}

func TestTrackCompositeFile(t *testing.T) {
	conf := getTestConfig(t)
	if !strings.HasPrefix(conf.ApiKey, "API") {
		t.Skip("valid config required for track composite test")
	}

	roomName := os.Getenv("LIVEKIT_ROOM_NAME")
	if roomName == "" {
		roomName = "egress-integration"
	}

	room, err := lksdk.ConnectToRoom(conf.WsUrl, lksdk.ConnectInfo{
		APIKey:              conf.ApiKey,
		APISecret:           conf.ApiSecret,
		RoomName:            roomName,
		ParticipantName:     "sample",
		ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
	})
	require.NoError(t, err)
	defer room.Disconnect()

	p := publishSamplesToRoom(t, room)

	for _, test := range []*testCase{
		// {
		// 	name:      "track-opus-ogg",
		// 	fileType:  livekit.EncodedFileType_OGG,
		// 	audioOnly: true,
		// 	options: &livekit.EncodingOptions{
		// 		AudioCodec: livekit.AudioCodec_OPUS,
		// 	},
		// },
		{
			name:     "track-h264-main-mp4",
			fileType: livekit.EncodedFileType_MP4,
		},
	} {
		if !t.Run(test.name, func(t *testing.T) {
			runTrackCompositeFileTest(t, conf, p, test)
		}) {
			t.FailNow()
		}
	}
}

func runTrackCompositeFileTest(t *testing.T, conf *config.Config, params *sdkParams, test *testCase) {
	filepath, filename := getFileInfo(conf, test, "track")

	audioTrackID := params.audioTrackID
	var videoTrackID string
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

func publishSamplesToRoom(t *testing.T, room *lksdk.Room) *sdkParams {
	p := &sdkParams{roomName: room.Name}

	for i, filename := range []string{"/out/sample/matrix-trailer.ogg", "/out/sample/matrix-trailer.ivf"} {
		filename := filename
		var pub *lksdk.LocalTrackPublication
		track, err := lksdk.NewLocalFileTrack(filename, lksdk.FileTrackWithOnWriteComplete(func() {
			if pub != nil {
				_ = room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}))
		require.NoError(t, err)

		pub, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
		require.NoError(t, err)
		if i == 0 {
			p.audioTrackID = pub.SID()
		} else {
			p.videoTrackID = pub.SID()
		}
	}

	return p
}

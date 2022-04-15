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

	testTrackCompositeFile(t, conf, roomName, "opus", "vp8")
	testTrackCompositeFile(t, conf, roomName, "opus", "h264")
}

func testTrackCompositeFile(t *testing.T, conf *config.Config, roomName, audioCodec, videoCodec string) {
	room, err := lksdk.ConnectToRoom(conf.WsUrl, lksdk.ConnectInfo{
		APIKey:              conf.ApiKey,
		APISecret:           conf.ApiSecret,
		RoomName:            roomName,
		ParticipantName:     "sample",
		ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
	})
	require.NoError(t, err)
	defer room.Disconnect()

	p := publishSamplesToRoom(t, room, audioCodec, videoCodec)

	for _, test := range []*testCase{
		{
			name:     fmt.Sprintf("track-%s-mp4", videoCodec),
			fileType: livekit.EncodedFileType_MP4,
		},
	} {
		if !t.Run(test.name, func(t *testing.T) {
			done := make(chan struct{})
			defer close(done)
			go printLoadAvg(t, test.name, done)
			runTrackCompositeFileTest(t, conf, p, test)
			time.Sleep(time.Millisecond * 100)
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

var (
	samples = map[string]string{
		"opus": "/out/sample/matrix-trailer.ogg",
		"vp8":  "/out/sample/matrix-trailer.ivf",
		"h264": "/out/sample/matrix-trailer.h264",
	}

	frameDurations = map[string]time.Duration{
		"vp8":  time.Microsecond * 41708,
		"h264": time.Microsecond * 41708,
	}
)

func publishSamplesToRoom(t *testing.T, room *lksdk.Room, audioCodec, videoCodec string) *sdkParams {
	p := &sdkParams{roomName: room.Name}

	publish := func(filename string, frameDuration time.Duration) string {
		var pub *lksdk.LocalTrackPublication
		opts := []lksdk.FileSampleProviderOption{
			lksdk.FileTrackWithOnWriteComplete(func() {
				if pub != nil {
					_ = room.LocalParticipant.UnpublishTrack(pub.SID())
				}
			}),
		}

		if frameDuration != 0 {
			opts = append(opts, lksdk.FileTrackWithFrameDuration(frameDuration))
		}

		track, err := lksdk.NewLocalFileTrack(filename, opts...)
		require.NoError(t, err)

		pub, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
		require.NoError(t, err)

		return pub.SID()
	}

	if samples[audioCodec] != "" {
		p.audioTrackID = publish(samples[audioCodec], frameDurations[audioCodec])
	}

	if samples[videoCodec] != "" {
		p.videoTrackID = publish(samples[videoCodec], frameDurations[videoCodec])
	}

	return p
}

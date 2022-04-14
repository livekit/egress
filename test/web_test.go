//go:build integration
// +build integration

package test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"

	"github.com/livekit/livekit-egress/pkg/pipeline"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

func TestRoomCompositeFile(t *testing.T) {
	t.Skip()

	conf := getTestConfig(t)
	for _, test := range []*testCase{
		{
			name:     "room-h264-high-mp4",
			inputUrl: videoTestInput,
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_HIGH,
				Height:       720,
				Width:        1280,
				VideoBitrate: 4500,
			},
		},
		{
			name:     "room-h264-baseline-mp4",
			inputUrl: staticTestInput,
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_BASELINE,
				Height:       720,
				Width:        1280,
				VideoBitrate: 1500,
			},
		},
	} {
		if !t.Run(test.name, func(t *testing.T) {
			done := make(chan struct{})
			defer close(done)
			go printLoadAvg(t, test.name, done)
			runRoomCompositeFileTest(t, conf, test)
		}) {
			t.FailNow()
		}
	}

	// TODO: get rid of this error, probably by calling Ref() on something
	//  (test.test:9038): GStreamer-CRITICAL **: 23:46:45.257:
	//  gst_mini_object_unref: assertion 'GST_MINI_OBJECT_REFCOUNT_VALUE (mini_object) > 0' failed
	t.Run("room-opus-ogg-simultaneous", func(t *testing.T) {
		done := make(chan struct{})
		go printLoadAvg(t, "room-opus-simul-ogg", done)

		finished := make(chan struct{})
		go func() {
			runRoomCompositeFileTest(t, conf, &testCase{
				inputUrl:  audioTestInput,
				fileType:  livekit.EncodedFileType_OGG,
				audioOnly: true,
				options: &livekit.EncodingOptions{
					AudioCodec: livekit.AudioCodec_OPUS,
				},
				filePrefix: "room-opus-simul-1",
			})
			close(finished)
		}()

		runRoomCompositeFileTest(t, conf, &testCase{
			inputUrl:  audioTestInput2,
			fileType:  livekit.EncodedFileType_OGG,
			audioOnly: true,
			options: &livekit.EncodingOptions{
				AudioCodec: livekit.AudioCodec_OPUS,
			},
			filePrefix: "room-opus-simul-2",
		})

		<-finished
		close(done)
		time.Sleep(time.Millisecond * 100)
	})
}

func runRoomCompositeFileTest(t *testing.T, conf *config.Config, test *testCase) {
	filepath, filename := getFileInfo(conf, test, "room")

	roomName := os.Getenv("LIVEKIT_ROOM_NAME")
	if roomName == "" {
		roomName = "web-composite-file"
	}

	webRequest := &livekit.RoomCompositeEgressRequest{
		RoomName:  roomName,
		Layout:    "speaker-dark",
		AudioOnly: test.audioOnly,
		Output: &livekit.RoomCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: filepath,
			},
		},
	}

	if test.options != nil {
		webRequest.Options = &livekit.RoomCompositeEgressRequest_Advanced{
			Advanced: test.options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: webRequest,
		},
	}

	runFileTest(t, conf, test, req, filename)
}

func TestRoomCompositeStream(t *testing.T) {
	t.Skip()

	done := make(chan struct{})
	go printLoadAvg(t, "web-composite-stream-1", done)

	conf := getTestConfig(t)
	url := "rtmp://localhost:1935/live/stream1"
	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName:      "web-composite-stream",
				Layout:        "speaker-dark",
				CustomBaseUrl: videoTestInput,
				Output: &livekit.RoomCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{url},
					},
				},
				Options: &livekit.RoomCompositeEgressRequest_Advanced{
					Advanced: &livekit.EncodingOptions{
						AudioCodec: livekit.AudioCodec_AAC,
					},
				},
			},
		},
	}

	p, err := params.GetPipelineParams(conf, req)
	require.NoError(t, err)
	p.CustomInputURL = videoTestInput
	rec, err := pipeline.FromParams(conf, p)
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

	close(done)
	done = make(chan struct{})
	go printLoadAvg(t, "web-composite-stream-2", done)

	// add another, check both
	url2 := "rtmp://localhost:1935/live/stream2"
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:      req.EgressId,
		AddOutputUrls: []string{url2},
	}))
	verifyStreams(t, p, url, url2)

	close(done)
	done = make(chan struct{})
	go printLoadAvg(t, "web-composite-stream-3", done)

	// remove first, check second
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:         req.EgressId,
		RemoveOutputUrls: []string{url},
	}))
	verifyStreams(t, p, url2)

	// stop
	rec.Stop()
	res := <-resChan

	// check results
	require.Empty(t, res.Error)
	require.Len(t, res.GetStream().Info, 2)
	for _, info := range res.GetStream().Info {
		require.NotEmpty(t, info.Url)
		require.NotZero(t, info.StartedAt)
		require.NotZero(t, info.EndedAt)
	}

	close(done)
	time.Sleep(time.Millisecond * 100)
}

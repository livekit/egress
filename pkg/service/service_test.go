package service

import (
	"context"
	"testing"
	"time"

	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/recording"
	"github.com/livekit/protocol/utils"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-recorder/pkg/config"
	"github.com/livekit/livekit-recorder/pkg/messaging"
	"github.com/livekit/livekit-recorder/pkg/pipeline"
)

func TestService(t *testing.T) {
	conf := config.TestConfig()
	bus, err := messaging.NewMessageBus(conf)
	require.NoError(t, err)

	svc := NewService(conf, bus)
	go func() {
		require.NoError(t, svc.Run())
	}()

	// wait for service to start
	time.Sleep(time.Millisecond * 100)

	var id1, id2, id3 string
	t.Run("Reservation", func(t *testing.T) {
		require.Equal(t, Available, svc.Status())
		id1, err = recording.ReserveRecorder(bus)
		require.NoError(t, err)
		require.Equal(t, Reserved, svc.Status())
	})

	t.Run("Double reservation fails", func(t *testing.T) {
		// second reservation should fail
		_, err = recording.ReserveRecorder(bus)
		require.Error(t, err)
	})

	t.Run("Start recording", func(t *testing.T) {
		require.NoError(t, recording.RPC(context.Background(), bus, id1, &livekit.RecordingRequest{
			RequestId: utils.RandomSecret(),
			Request: &livekit.RecordingRequest_Start{
				Start: startRecordingRequest(true),
			},
		}))
	})

	t.Run("RPC validation", func(t *testing.T) {
		require.Equal(t, pipeline.ErrCannotAddToFile,
			recording.RPC(context.Background(), bus, id1, &livekit.RecordingRequest{
				RequestId: utils.RandomSecret(),
				Request: &livekit.RecordingRequest_AddOutput{
					AddOutput: &livekit.AddOutputRequest{
						RecordingId: id1,
						RtmpUrl:     "rtmp://fake-url.com?stream-id=xyz",
					},
				},
			}),
		)
	})

	t.Run("Recording completes", func(t *testing.T) {
		time.Sleep(time.Millisecond * 3100)
		require.Equal(t, Available, svc.Status())
	})

	t.Run("RPCs", func(t *testing.T) {
		id2, err = recording.ReserveRecorder(bus)
		require.NoError(t, err)
		require.NoError(t, recording.RPC(context.Background(), bus, id2, &livekit.RecordingRequest{
			RequestId: utils.RandomSecret(),
			Request: &livekit.RecordingRequest_Start{
				Start: startRecordingRequest(false),
			},
		}))

		require.NoError(t, recording.RPC(context.Background(), bus, id2, &livekit.RecordingRequest{
			RequestId: utils.RandomSecret(),
			Request: &livekit.RecordingRequest_AddOutput{
				AddOutput: &livekit.AddOutputRequest{
					RecordingId: id2,
					RtmpUrl:     "rtmp://fake-url.com?stream-id=xyz",
				},
			},
		}))
	})

	t.Run("Stop recording", func(t *testing.T) {
		require.NoError(t, recording.RPC(context.Background(), bus, id2, &livekit.RecordingRequest{
			RequestId: utils.RandomSecret(),
			Request: &livekit.RecordingRequest_End{
				End: &livekit.EndRecordingRequest{
					RecordingId: id2,
				},
			},
		}))
		status := svc.Status()
		require.True(t, status == Stopping || status == Available)
	})

	t.Run("Kill service", func(t *testing.T) {
		id3, err = recording.ReserveRecorder(bus)
		require.NoError(t, err)
		require.NoError(t, recording.RPC(context.Background(), bus, id3, &livekit.RecordingRequest{
			RequestId: utils.RandomSecret(),
			Request: &livekit.RecordingRequest_Start{
				Start: startRecordingRequest(false),
			},
		}))

		svc.Stop(true)
		time.Sleep(time.Millisecond * 100)
		status := svc.Status()
		// status will show available for a very small amount of time on shutdown
		require.True(t, status == Stopping || status == Available)
	})
}

func startRecordingRequest(s3 bool) *livekit.StartRecordingRequest {
	req := &livekit.StartRecordingRequest{
		Input: &livekit.StartRecordingRequest_Template{Template: &livekit.RecordingTemplate{
			Layout: "speaker-dark",
			Room: &livekit.RecordingTemplate_Token{
				Token: "fake-recording-token",
			},
		}},
	}

	if s3 {
		req.Output = &livekit.StartRecordingRequest_S3Url{
			S3Url: "s3://livekit/test.mp4",
		}
	} else {
		req.Output = &livekit.StartRecordingRequest_Rtmp{
			Rtmp: &livekit.RtmpOutput{
				Urls: []string{"rtmp://stream.io/test"},
			},
		}
	}

	return req
}

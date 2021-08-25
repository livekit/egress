package service

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/livekit/protocol/utils"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recorder/service/pkg/config"
	"github.com/livekit/livekit-recorder/service/pkg/logger"
	livekit "github.com/livekit/livekit-recorder/service/proto"
)

func TestWorker(t *testing.T) {
	logger.Init("debug")

	ctx := context.Background()
	conf := config.TestConfig()
	rc, err := StartRedis(conf)
	require.NoError(t, err)

	worker := InitializeWorker(conf, rc)
	go func() {
		err := worker.Start()
		require.NoError(t, err)
	}()

	// wait for worker to start
	time.Sleep(time.Millisecond * 100)

	t.Run("Submit", func(t *testing.T) {
		submit(t, ctx, rc, worker)
		// wait to finish
		time.Sleep(time.Millisecond * 3100)
		require.Equal(t, Available, worker.Status())
	})

	t.Run("Reserved", func(t *testing.T) {
		submit(t, ctx, rc, worker)
		submitReserved(t, rc)
		// wait to finish
		time.Sleep(time.Millisecond * 3100)
		require.Equal(t, Available, worker.Status())
	})

	t.Run("Stop", func(t *testing.T) {
		id := submit(t, ctx, rc, worker)
		// server ends recording
		require.NoError(t, rc.Publish(ctx, utils.EndRecordingChannel(id), nil).Err())
		time.Sleep(time.Millisecond * 50)
		// check that recording has ended early
		require.Equal(t, Available, worker.Status())
	})

	t.Run("Kill", func(t *testing.T) {
		submit(t, ctx, rc, worker)
		// worker is killed
		worker.Stop(true)
		time.Sleep(time.Millisecond * 100)
		// check that recording has ended early
		require.Equal(t, Available, worker.Status())
	})
}

func submit(t *testing.T, ctx context.Context, rc *redis.Client, worker *Worker) string {
	// send recording reservation
	req := &livekit.RecordingReservation{
		SubmittedAt: time.Now().UnixNano(),
		Request: &livekit.StartRecordingRequest{
			Input: &livekit.RecordingInput{
				Template: &livekit.RecordingTemplate{
					Layout: "speaker-light",
					WsUrl:  "wss://testing.livekit.io",
					Token:  "token",
				},
			},
			Output: &livekit.RecordingOutput{
				File: "recording.mp4",
			},
		},
	}

	// server sends reservation
	id, err := reserveRecorder(context.Background(), rc, req)
	require.NoError(t, err)

	// check that worker is reserved
	require.Equal(t, Reserved, worker.Status())

	// start recording
	require.NoError(t, rc.Publish(ctx, utils.StartRecordingChannel(id), nil).Err())
	time.Sleep(time.Millisecond * 50)

	// check that worker is recording
	require.Equal(t, Recording, worker.Status())

	return id
}

func submitReserved(t *testing.T, rc *redis.Client) {
	// send recording reservation
	req := &livekit.RecordingReservation{
		SubmittedAt: time.Now().UnixNano(),
		Request: &livekit.StartRecordingRequest{
			Input: &livekit.RecordingInput{
				Template: &livekit.RecordingTemplate{
					Layout: "speaker-light",
					WsUrl:  "wss://testing.livekit.io",
					Token:  "token",
				},
			},
			Output: &livekit.RecordingOutput{
				File: "recording.mp4",
			},
		},
	}

	// server sends reservation
	_, err := reserveRecorder(context.Background(), rc, req)
	require.Error(t, err)
}

func reserveRecorder(ctx context.Context, rc *redis.Client, req *livekit.RecordingReservation) (string, error) {
	id := utils.NewGuid(utils.RecordingPrefix)
	req.Id = id
	b, err := proto.Marshal(req)
	if err != nil {
		return "", err
	}

	sub := rc.Subscribe(ctx, utils.ReservationResponseChannel(id))
	defer sub.Close()

	err = rc.Publish(ctx, utils.ReservationChannel, string(b)).Err()
	if err != nil {
		return "", err
	}

	select {
	case <-sub.Channel():
		return id, nil
	case <-time.After(utils.RecorderTimeout):
		return "", errors.New("no recorders available")
	}
}

package service

import (
	"context"
	"testing"
	"time"

	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/utils"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recorder/service/pkg/config"
	"github.com/livekit/livekit-recorder/service/pkg/logger"
)

func TestWorker(t *testing.T) {
	logger.Init("debug")

	conf := config.TestConfig()
	rc, err := NewMessageBus(conf)
	require.NoError(t, err)

	worker := InitializeWorker(conf, rc)
	go func() {
		err := worker.Start()
		require.NoError(t, err)
	}()

	// wait for worker to start
	time.Sleep(time.Millisecond * 100)

	t.Run("Submit", func(t *testing.T) {
		require.Equal(t, Available, worker.Status())
		submit(t, rc, worker)
		// wait to finish
		time.Sleep(time.Millisecond * 3100)
		require.Equal(t, Available, worker.Status())
	})

	t.Run("Reserved", func(t *testing.T) {
		require.Equal(t, Available, worker.Status())
		submit(t, rc, worker)
		submitReserved(t, rc)
		// wait to finish
		time.Sleep(time.Millisecond * 3100)
		require.Equal(t, Available, worker.Status())
	})

	t.Run("Stop", func(t *testing.T) {
		require.Equal(t, Available, worker.Status())
		id := submit(t, rc, worker)
		// server ends recording
		require.NoError(t, rc.Publish(context.Background(), utils.EndRecordingChannel(id), nil))
		time.Sleep(time.Millisecond * 50)
		// check that recording has ended early
		require.Equal(t, Available, worker.Status())
	})

	t.Run("Kill", func(t *testing.T) {
		require.Equal(t, Available, worker.Status())
		submit(t, rc, worker)
		// worker is killed
		worker.Stop(true)
		time.Sleep(time.Millisecond * 50)
		// check that recording has ended early
		require.Equal(t, Available, worker.Status())
	})
}

func submit(t *testing.T, rc utils.MessageBus, worker *Worker) string {
	// send recording reservation
	req := &livekit.RecordingReservation{
		SubmittedAt: time.Now().UnixNano(),
		Request: &livekit.StartRecordingRequest{
			Input: &livekit.RecordingInput{
				Template: &livekit.RecordingTemplate{
					Layout: "speaker-light",
					Token:  "token",
				},
			},
			Output: &livekit.RecordingOutput{
				S3Path: "bucket/recording.mp4",
			},
		},
	}

	// server sends reservation
	id, err := reserveRecorder(rc, req)
	require.NoError(t, err)

	// check that worker is reserved
	require.Equal(t, Reserved, worker.Status())

	// start recording
	require.NoError(t, rc.Publish(context.Background(), utils.StartRecordingChannel(id), nil))
	time.Sleep(time.Millisecond * 50)

	// check that worker is recording
	require.Equal(t, Recording, worker.Status())

	return id
}

func submitReserved(t *testing.T, rc utils.MessageBus) {
	// send recording reservation
	req := &livekit.RecordingReservation{
		SubmittedAt: time.Now().UnixNano(),
		Request: &livekit.StartRecordingRequest{
			Input: &livekit.RecordingInput{
				Template: &livekit.RecordingTemplate{
					Layout: "speaker-light",
					Token:  "token",
				},
			},
			Output: &livekit.RecordingOutput{
				S3Path: "bucket/recording.mp4",
			},
		},
	}

	// server sends reservation
	_, err := reserveRecorder(rc, req)
	require.Error(t, err)
}

func reserveRecorder(rc utils.MessageBus, req *livekit.RecordingReservation) (string, error) {
	id := utils.NewGuid(utils.RecordingPrefix)
	req.Id = id
	b, err := proto.Marshal(req)
	if err != nil {
		return "", err
	}

	sub, _ := rc.Subscribe(context.Background(), utils.ReservationResponseChannel(id))
	defer sub.Close()

	err = rc.Publish(context.Background(), utils.ReservationChannel, string(b))
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

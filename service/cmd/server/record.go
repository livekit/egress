package main

import (
	"context"
	"fmt"
	"time"

	"github.com/livekit/protocol/utils"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recorder/service/pkg/config"
	"github.com/livekit/livekit-recorder/service/pkg/logger"
	"github.com/livekit/livekit-recorder/service/pkg/service"
	livekit "github.com/livekit/livekit-recorder/service/proto"
)

// Acts as a livekit server against a recorder-service docker container with shared redis
func startRecording(c *cli.Context) error {
	logger.Init("debug")

	ctx := context.Background()
	conf := config.TestConfig()
	rc, err := service.StartRedis(conf)
	if err != nil {
		return err
	}

	req := &livekit.RecordingReservation{
		Id:          utils.NewGuid(utils.RecordingPrefix),
		SubmittedAt: time.Now().UnixNano(),
		Input: &livekit.RecordingInput{
			Template: &livekit.RecordingTemplate{
				Type:  "speaker-dark",
				WsUrl: c.String("ws-url"),
				Token: c.String("token"),
			},
		},
		Output: &livekit.RecordingOutput{
			S3: &livekit.RecordingS3Output{
				AccessKey: c.String("aws-key"),
				Secret:    c.String("aws-secret"),
				Bucket:    c.String("bucket"),
				Key:       c.String("key"),
			},
		},
	}

	b, err := proto.Marshal(req)
	if err != nil {
		return err
	}

	sub := rc.Subscribe(ctx, utils.ReservationResponseChannel(req.Id))
	defer sub.Close()

	if err = rc.Publish(ctx, utils.ReservationChannel, string(b)).Err(); err != nil {
		return err
	}

	select {
	case <-sub.Channel():
		if err = rc.Publish(ctx, utils.StartRecordingChannel(req.Id), nil).Err(); err != nil {
			return err
		}
	case <-time.After(utils.RecorderTimeout):
		return errors.New("no response from recorder service")
	}

	fmt.Println("Recording ID:", req.Id)
	return nil
}

func stopRecording(c *cli.Context) error {
	logger.Init("debug")

	ctx := context.Background()
	conf := config.TestConfig()
	rc, err := service.StartRedis(conf)
	if err != nil {
		return err
	}

	return rc.Publish(ctx, utils.EndRecordingChannel(c.String("id")), nil).Err()
}

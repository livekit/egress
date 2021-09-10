package main

import (
	"context"
	"fmt"
	"time"

	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/utils"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recorder/service/pkg/config"
	"github.com/livekit/livekit-recorder/service/pkg/service"
)

// Acts as a livekit server against a recorder-service docker container with shared redis
func startRecording(c *cli.Context) error {
	initLogger("debug")

	conf := config.TestConfig()
	rc, err := service.NewMessageBus(conf)
	if err != nil {
		return err
	}

	req := &livekit.RecordingReservation{
		Id:          utils.NewGuid(utils.RecordingPrefix),
		SubmittedAt: time.Now().UnixNano(),
		Request: &livekit.StartRecordingRequest{
			Input: &livekit.RecordingInput{
				Template: &livekit.RecordingTemplate{
					Layout: "speaker-dark",
					Token:  c.String("token"),
				},
			},
			Output: &livekit.RecordingOutput{
				S3Path: c.String("s3"),
			},
		},
	}

	b, err := proto.Marshal(req)
	if err != nil {
		return err
	}

	ctx := context.Background()
	sub, _ := rc.Subscribe(ctx, utils.ReservationResponseChannel(req.Id))
	defer sub.Close()

	if err = rc.Publish(ctx, utils.ReservationChannel, string(b)); err != nil {
		return err
	}

	select {
	case <-sub.Channel():
		if err = rc.Publish(ctx, utils.StartRecordingChannel(req.Id), nil); err != nil {
			return err
		}
	case <-time.After(utils.RecorderTimeout):
		return errors.New("no response from recorder service")
	}

	fmt.Println("Recording ID:", req.Id)
	return nil
}

func stopRecording(c *cli.Context) error {
	initLogger("debug")

	conf := config.TestConfig()
	rc, err := service.NewMessageBus(conf)
	if err != nil {
		return err
	}

	return rc.Publish(context.Background(), utils.EndRecordingChannel(c.String("id")), nil)
}

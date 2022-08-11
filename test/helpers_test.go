//go:build integration

package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/params"
	lksdk "github.com/livekit/server-sdk-go"
)

const muteDuration = time.Second * 10

var (
	samples = map[params.MimeType]string{
		params.MimeTypeOpus: "/out/sample/matrix-trailer.ogg",
		params.MimeTypeH264: "/out/sample/matrix-trailer.h264",
		params.MimeTypeVP8:  "/out/sample/matrix-trailer.ivf",
	}

	frameDurations = map[params.MimeType]time.Duration{
		params.MimeTypeH264: time.Microsecond * 41708,
		params.MimeTypeVP8:  time.Microsecond * 41708,
	}
)

func publishSamplesToRoom(t *testing.T, room *lksdk.Room, audioCodec, videoCodec params.MimeType, withMuting bool) (audioTrackID, videoTrackID string) {
	audioTrackID = publishSampleToRoom(t, room, audioCodec, false)
	videoTrackID = publishSampleToRoom(t, room, videoCodec, withMuting)
	time.Sleep(time.Second)
	return
}

func publishSampleToRoom(t *testing.T, room *lksdk.Room, codec params.MimeType, withMuting bool) string {
	filename := samples[codec]
	frameDuration := frameDurations[codec]

	var pub *lksdk.LocalTrackPublication
	done := make(chan struct{})
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			close(done)
			if pub != nil {
				_ = room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}),
	}

	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
	}

	track, err := lksdk.NewLocalFileTrack(filename, opts...)
	require.NoError(t, err)

	pub, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
	require.NoError(t, err)

	trackID := pub.SID()
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(trackID)
	})

	if withMuting {
		go func() {
			muted := false
			time.Sleep(muteDuration)
			for {
				select {
				case <-done:
					return
				default:
					pub.SetMuted(!muted)
					muted = !muted
					time.Sleep(muteDuration)
				}
			}
		}()
	}

	return trackID
}

func getFilePath(conf *config.Config, filename string) string {
	if conf.FileUpload != nil {
		return filename
	}

	return fmt.Sprintf("/out/output/%s", filename)
}

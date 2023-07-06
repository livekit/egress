//go:build integration

package test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	muteDuration = time.Second * 10

	streamUrl1      = "rtmp://localhost:1935/live/stream"
	redactedUrl1    = "rtmp://localhost:1935/live/{st...am}"
	streamUrl2      = "rtmp://localhost:1935/live/stream_key"
	redactedUrl2    = "rtmp://localhost:1935/live/{str...key}"
	badStreamUrl1   = "rtmp://sfo.contribute.live-video.net/app/fake1"
	redactedBadUrl1 = "rtmp://sfo.contribute.live-video.net/app/{f...1}"
	badStreamUrl2   = "rtmp://localhost:1936/live/stream"
	redactedBadUrl2 = "rtmp://localhost:1936/live/{st...am}"
	webUrl          = "https://videoplayer-2k23.vercel.app/videos/eminem"
)

var (
	samples = map[types.MimeType]string{
		types.MimeTypeOpus: "/workspace/test/sample/matrix-trailer.ogg",
		types.MimeTypeH264: "/workspace/test/sample/matrix-trailer.h264",
		types.MimeTypeVP8:  "/workspace/test/sample/matrix-trailer.ivf",
	}

	frameDurations = map[types.MimeType]time.Duration{
		types.MimeTypeH264: time.Microsecond * 41708,
		types.MimeTypeVP8:  time.Microsecond * 41708,
	}
)

type testCase struct {
	name      string
	audioOnly bool
	videoOnly bool
	filename  string

	// used by room and track composite tests
	fileType livekit.EncodedFileType
	options  *livekit.EncodingOptions
	preset   livekit.EncodingOptionsPreset

	// used by segmented file tests
	playlist       string
	filenameSuffix livekit.SegmentedFileSuffix

	// used by track and track composite tests
	audioCodec types.MimeType
	videoCodec types.MimeType

	// used by track tests
	outputType types.OutputType

	expectVideoTranscoding bool
}

func (r *Runner) awaitIdle(t *testing.T) {
	r.svc.KillAll()
	for i := 0; i < 30; i++ {
		status := r.getStatus(t)
		if len(status) == 1 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("service not idle after 30s")
}

func (r *Runner) publishSamplesToRoom(t *testing.T, audioCodec, videoCodec types.MimeType) (audioTrackID, videoTrackID string) {
	withVideoMuting := false
	if audioCodec == "" {
		withVideoMuting = r.Muting
	}
	if videoCodec != "" {
		videoTrackID = r.publishSampleToRoom(t, videoCodec, withVideoMuting)
	}
	if audioCodec != "" {
		audioTrackID = r.publishSampleToRoom(t, audioCodec, r.Muting)
	}

	time.Sleep(time.Second)
	return
}

func (r *Runner) publishSampleToRoom(t *testing.T, codec types.MimeType, withMuting bool) string {
	filename := samples[codec]
	frameDuration := frameDurations[codec]

	var pub *lksdk.LocalTrackPublication
	done := make(chan struct{})
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			close(done)
			if pub != nil {
				_ = r.room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}),
	}

	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
	}

	track, err := lksdk.NewLocalFileTrack(filename, opts...)
	require.NoError(t, err)

	pub, err = r.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
	require.NoError(t, err)

	trackID := pub.SID()
	t.Cleanup(func() {
		_ = r.room.LocalParticipant.UnpublishTrack(trackID)
	})

	if withMuting {
		go func() {
			muted := false
			time.Sleep(time.Second * 15)
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

func (r *Runner) startEgress(t *testing.T, req *rpc.StartEgressRequest) string {
	// send start request
	info, err := r.client.StartEgress(context.Background(), "", req)

	// check returned egress info
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_Web:
		require.Empty(t, info.RoomName)
	default:
		require.Equal(t, r.RoomName, info.RoomName)
	}

	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING.String(), info.Status.String())

	// check status
	if r.HealthPort != 0 {
		status := r.getStatus(t)
		require.Contains(t, status, info.EgressId)
	}

	// wait
	time.Sleep(time.Second * 5)

	// check active update
	r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_ACTIVE)

	return info.EgressId
}

func (r *Runner) checkUpdate(t *testing.T, egressID string, status livekit.EgressStatus) *livekit.EgressInfo {
	info := r.getUpdate(t, egressID)

	require.Equal(t, status.String(), info.Status.String())
	require.Equal(t, info.Status == livekit.EgressStatus_EGRESS_FAILED, info.Error != "")

	return info
}

func (r *Runner) checkStreamUpdate(t *testing.T, egressID string, expected map[string]livekit.StreamInfo_Status) {
	info := r.getUpdate(t, egressID)

	require.Equal(t, len(expected), len(info.StreamResults))
	for _, s := range info.StreamResults {
		require.Equal(t, expected[s.Url], s.Status)
		require.Equal(t, s.Status == livekit.StreamInfo_FAILED, s.Error != "")
	}
}

func (r *Runner) getUpdate(t *testing.T, egressID string) *livekit.EgressInfo {
	for {
		select {
		case info := <-r.updates:
			if info.EgressId == egressID {
				return info
			}

		case <-time.After(time.Minute):
			t.Fatal("no update from results channel")
			return nil
		}
	}
}

func (r *Runner) getStatus(t *testing.T) map[string]interface{} {
	b, err := r.svc.Status()
	require.NoError(t, err)

	status := make(map[string]interface{})
	err = json.Unmarshal(b, &status)
	require.NoError(t, err)

	return status
}

func (r *Runner) createDotFile(t *testing.T, egressID string) {
	dot, err := r.svc.GetGstPipelineDotFile(egressID)
	require.NoError(t, err)

	filename := strings.ReplaceAll(t.Name()[11:], "/", "_")
	filepath := fmt.Sprintf("%s/%s.dot", r.LocalOutputDirectory, filename)
	f, err := os.Create(filepath)
	require.NoError(t, err)
	defer f.Close()

	_, err = f.WriteString(dot)
	require.NoError(t, err)
}

func (r *Runner) stopEgress(t *testing.T, egressID string) *livekit.EgressInfo {
	// send stop request
	info, err := r.client.StopEgress(context.Background(), egressID, &livekit.StopEgressRequest{
		EgressId: egressID,
	})

	// check returned egress info
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.StartedAt)
	require.Equal(t, livekit.EgressStatus_EGRESS_ENDING.String(), info.Status.String())

	// check ending update
	r.checkUpdate(t, egressID, livekit.EgressStatus_EGRESS_ENDING)

	// get final info
	res := r.checkUpdate(t, egressID, livekit.EgressStatus_EGRESS_COMPLETE)

	// check status
	if r.HealthPort != 0 {
		status := r.getStatus(t)
		require.Len(t, status, 1)
	}

	return res
}

func getFilePath(conf *config.ServiceConfig, filename string) string {
	if conf.S3 != nil || conf.Azure != nil || conf.GCP != nil || conf.AliOSS != nil {
		return filename
	}

	return fmt.Sprintf("%s/%s", conf.LocalOutputDirectory, filename)
}

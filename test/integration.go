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
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	webUrl       = "https://download.blender.org/peach/bigbuckbunny_movies/BigBuckBunny_320x180.mp4"
	setAtRuntime = "set-at-runtime"
)

var uploadPrefix = fmt.Sprintf("integration/%s", time.Now().Format("2006-01-02"))

func (r *Runner) RunTests(t *testing.T) {
	allTests := make([]*testCase, 0, len(generatedTests)+len(edgeTests))
	allTests = append(allTests, generatedTests...)
	allTests = append(allTests, edgeTests...)

	for i, test := range allTests {
		if !r.shouldRunTest(i, test.name) {
			continue
		}
		if !r.run(t, test) {
			return
		}
	}
}

func (r *Runner) run(t *testing.T, tc *testCase) bool {
	cfg := tc.applyOptions()

	// Set source framerate based on request type
	switch cfg.RequestType {
	case types.RequestTypeRoomComposite, types.RequestTypeWeb:
		r.sourceFramerate = 30
	case types.RequestTypeParticipant, types.RequestTypeTrackComposite, types.RequestTypeTrack, types.RequestTypeMedia:
		r.sourceFramerate = 23.97
	case types.RequestTypeTemplate:
		if cfg.AudioOnly && cfg.Layout == "" && cfg.CustomBaseUrl == "" {
			r.sourceFramerate = 23.97
		} else {
			r.sourceFramerate = 30
		}
	}

	r.awaitIdle(t)
	r.ensureRoomForCfg(t, cfg)

	r.testNumber++
	t.Run(fmt.Sprintf("%d/%s", r.testNumber, tc.name), func(t *testing.T) {
		// Publish tracks
		var audioTrackID, videoTrackID string
		if !cfg.MultiParticipant {
			audioMuting := r.Muting
			videoMuting := r.Muting && cfg.AudioCodec == ""
			audioTrackID = r.publishSample(t, cfg.AudioCodec, 0, 0, audioMuting)
			videoTrackID = r.publishSample(t, cfg.VideoCodec, 0, 0, videoMuting)
		} else {
			mp := r.publishAllParticipants(t)
			audioTrackID = mp.audioTrackID
			videoTrackID = mp.videoTrackID
			if cfg.Layout == layoutSpeaker || cfg.Layout == layoutSingleSpeaker {
				cancelRotation := r.startMuteRotation(mp.audioPubs)
				t.Cleanup(cancelRotation)
			}
		}

		// Ensure at least one output is configured (some generated tests only
		// cover encoding options without an explicit output type dimension)
		if len(cfg.FileOutputs) == 0 && len(cfg.StreamOutputs) == 0 && len(cfg.SegmentOutputs) == 0 && len(cfg.ImageOutputs) == 0 {
			cfg.FileOutputs = append(cfg.FileOutputs, fileOutputConfig{
				Filename: "default_{time}.mp4",
			})
		}

		// Build request
		req := cfg.Build(BuildParams{
			RoomName:     r.RoomName,
			AudioTrackID: audioTrackID,
			VideoTrackID: videoTrackID,
			FilePrefix:   r.FilePrefix,
			ApiKey:       r.ApiKey,
			ApiSecret:    r.ApiSecret,
			WsUrl:        r.WsUrl,
			UploadConfig: r.getUploadConfig(),
		})

		// Inject participant identity for Participant requests
		if p := req.GetParticipant(); p != nil {
			p.Identity = string(r.room.LocalParticipant.Identity())
		}

		// Inject runtime values for v2 requests (audio routes, participant video)
		r.injectRuntimeValues(cfg, req, audioTrackID)

		logger.Infow("test publish summary",
			"test", tc.name,
			"room", r.RoomName,
			"audioCodec", cfg.AudioCodec,
			"audioTrackID", audioTrackID,
			"videoCodec", cfg.VideoCodec,
			"videoTrackID", videoTrackID,
		)

		// Edge cases take over from here
		if tc.custom != nil {
			tc.custom(r, t, req, cfg)
			return
		}

		// Standard test: start, wait, stop, verify all outputs
		r.runStandardTest(t, cfg, req)
	})

	return !r.Short
}

func (r *Runner) runStandardTest(t *testing.T, cfg *TestConfig, req *rpc.StartEgressRequest) {
	// For stream-only tests, use the full stream test flow (add/remove URLs, verify)
	if len(cfg.StreamOutputs) > 0 && len(cfg.FileOutputs) == 0 && len(cfg.SegmentOutputs) == 0 && len(cfg.ImageOutputs) == 0 {
		r.runStreamTest(t, cfg, req)
		return
	}

	egressID := r.startEgress(t, req)

	time.Sleep(time.Second * 10)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// For multi-output with streams, add a dynamic stream URL mid-test
	if len(cfg.StreamOutputs) > 0 && (len(cfg.FileOutputs) > 0 || len(cfg.SegmentOutputs) > 0 || len(cfg.ImageOutputs) > 0) {
		p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
		require.NoError(t, err)

		_, err = r.client.UpdateStream(context.Background(), egressID, &livekit.UpdateStreamRequest{
			EgressId:      egressID,
			AddOutputUrls: []string{rtmpUrl3},
		})
		require.NoError(t, err)

		time.Sleep(time.Second * 10)
		r.verifyStreams(t, nil, p, rtmpUrl3)
		r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
			rtmpUrl3Redacted: livekit.StreamInfo_ACTIVE,
		})
		time.Sleep(time.Second * 10)
	} else {
		time.Sleep(time.Second * 15)
	}

	res := r.stopEgress(t, egressID)

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	if len(cfg.FileOutputs) > 0 {
		r.verifyFile(t, cfg, p, res)
	}
	if len(cfg.SegmentOutputs) > 0 {
		suffix := cfg.SegmentOutputs[0].Suffix
		r.verifySegments(t, cfg, p, suffix, res, cfg.SegmentOutputs[0].LivePlaylist != "")
	}
	if len(cfg.ImageOutputs) > 0 {
		r.verifyImages(t, p, res)
	}
}

func (r *Runner) ensureRoomForCfg(t *testing.T, cfg *TestConfig) {
	desiredRoom := r.RoomBaseName
	var codecs []livekit.Codec
	switch cfg.AudioCodec {
	case types.MimeTypePCMU:
		desiredRoom = fmt.Sprintf("%s-pcmu", r.RoomBaseName)
		codecs = []livekit.Codec{{Mime: string(types.MimeTypePCMU)}}
	case types.MimeTypePCMA:
		desiredRoom = fmt.Sprintf("%s-pcma", r.RoomBaseName)
		codecs = []livekit.Codec{{Mime: string(types.MimeTypePCMA)}}
	}
	if desiredRoom == "" || desiredRoom == r.RoomName {
		return
	}
	r.connectRoom(t, desiredRoom, codecs)
}

func (r *Runner) injectRuntimeValues(_ *TestConfig, req *rpc.StartEgressRequest, audioTrackID string) {
	replay := req.GetReplay()
	if replay == nil {
		return
	}
	media := replay.GetMedia()
	if media == nil {
		return
	}
	if pv := media.GetParticipantVideo(); pv != nil && pv.Identity == setAtRuntime {
		pv.Identity = string(r.room.LocalParticipant.Identity())
	}
	if media.Audio != nil {
		for _, route := range media.Audio.Routes {
			if tr, ok := route.Match.(*livekit.AudioRoute_TrackId); ok && tr.TrackId == setAtRuntime {
				tr.TrackId = audioTrackID
			}
			if pi, ok := route.Match.(*livekit.AudioRoute_ParticipantIdentity); ok && pi.ParticipantIdentity == setAtRuntime {
				pi.ParticipantIdentity = string(r.room.LocalParticipant.Identity())
			}
		}
	}
}

func (r *Runner) awaitIdle(t *testing.T) {
	r.svc.KillAll()
	for i := 0; i < 30; i++ {
		if r.svc.IsIdle() && len(r.room.LocalParticipant.TrackPublications()) == 0 {
			return
		}
		time.Sleep(time.Second)
	}

	if !r.svc.IsIdle() {
		t.Fatal("service not idle after 30s")
	} else if len(r.room.LocalParticipant.TrackPublications()) != 0 {
		t.Fatal("room still has tracks after 30s")
	}
}

func (r *Runner) startEgress(t *testing.T, req *rpc.StartEgressRequest) string {
	info := r.sendRequest(t, req)

	if r.HealthPort != 0 {
		status := r.getStatus(t)
		require.Contains(t, status, info.EgressId)
	}

	time.Sleep(time.Second * 5)

	r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_ACTIVE)

	return info.EgressId
}

func (r *Runner) sendRequest(t *testing.T, req *rpc.StartEgressRequest) *livekit.EgressInfo {
	info, err := r.StartEgress(context.Background(), req)

	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_Web:
		require.Empty(t, info.RoomName)
	case *rpc.StartEgressRequest_Replay:
		replayReq := req.Request.(*rpc.StartEgressRequest_Replay).Replay
		if _, ok := replayReq.Source.(*livekit.ExportReplayRequest_Web); ok {
			require.Empty(t, info.RoomName)
		}
	default:
		require.Equal(t, r.RoomName, info.RoomName)
	}

	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING.String(), info.Status.String())
	return info
}

func (r *Runner) checkUpdate(t *testing.T, egressID string, status livekit.EgressStatus) *livekit.EgressInfo {
	info := r.getUpdate(t, egressID)

	require.Equal(t, status.String(), info.Status.String(), info.Error)
	require.Equal(t, info.Status == livekit.EgressStatus_EGRESS_FAILED, info.Error != "")

	return info
}

func (r *Runner) checkStreamUpdate(t *testing.T, egressID string, expected map[string]livekit.StreamInfo_Status) {
	for {
		info := r.getUpdate(t, egressID)
		if len(expected) != len(info.StreamResults) {
			continue
		}
		require.Equal(t, len(expected), len(info.StreamResults))

		checkNext := false
		for _, s := range info.StreamResults {
			require.Equal(t, s.Status == livekit.StreamInfo_FAILED, s.Error != "")
			if expected[s.Url] > s.Status {
				logger.Debugw(fmt.Sprintf("stream status %s, expecting %s", s.Status.String(), expected[s.Url].String()))
				checkNext = true
				continue
			}
			require.Equal(t, expected[s.Url], s.Status)
		}

		if !checkNext {
			return
		}
	}
}

func (r *Runner) getUpdate(t *testing.T, egressID string) *livekit.EgressInfo {
	for {
		select {
		case info := <-r.updates:
			if info.EgressId == egressID {
				return info
			}

		case <-time.After(time.Second * 30):
			r.createDotFile(t, egressID)
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
	filepath := fmt.Sprintf("%s/%s.dot", r.FilePrefix, filename)
	f, err := os.Create(filepath)
	require.NoError(t, err)
	defer f.Close()

	_, err = f.WriteString(dot)
	require.NoError(t, err)
}

func (r *Runner) stopEgress(t *testing.T, egressID string) *livekit.EgressInfo {
	info, err := r.client.StopEgress(context.Background(), egressID, &livekit.StopEgressRequest{
		EgressId: egressID,
	})

	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.StartedAt)
	require.Equal(t, livekit.EgressStatus_EGRESS_ENDING.String(), info.Status.String())

	r.checkUpdate(t, egressID, livekit.EgressStatus_EGRESS_ENDING)

	res := r.checkUpdate(t, egressID, livekit.EgressStatus_EGRESS_COMPLETE)

	if r.HealthPort != 0 {
		status := r.getStatus(t)
		require.Len(t, status, 1)
	}

	return res
}

func (r *Runner) startMuteRotation(pubs [3]*lksdk.LocalTrackPublication) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())

	pubs[1].SetMuted(true)
	pubs[2].SetMuted(true)

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		current := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pubs[current].SetMuted(true)
				current = (current + 1) % 3
				pubs[current].SetMuted(false)
			}
		}
	}()

	return cancel
}

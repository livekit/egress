// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
)

var uploadPrefix = fmt.Sprintf("integration/%s", time.Now().Format("2006-01-02"))

func (r *Runner) RunTests(t *testing.T) {
	// run tests
	r.testFile(t)
	r.testStream(t)
	r.testSegments(t)
	r.testImages(t)
	r.testMulti(t)
	r.testEdgeCases(t)
}

func (r *Runner) run(t *testing.T, test *testCase) bool {
	if !r.should(runRequestType[test.requestType]) {
		return true
	}

	switch test.requestType {
	case types.RequestTypeRoomComposite, types.RequestTypeWeb:
		r.sourceFramerate = 30
	case types.RequestTypeParticipant, types.RequestTypeTrackComposite, types.RequestTypeTrack, types.RequestTypeMedia:
		r.sourceFramerate = 23.97
	case types.RequestTypeTemplate:
		if test.audioOnly && test.layout == "" && test.templateCustomBaseUrl == "" {
			r.sourceFramerate = 23.97
		} else {
			r.sourceFramerate = 30
		}
	}

	r.awaitIdle(t)
	r.setRoomNameForTest(test)

	r.testNumber++
	t.Run(fmt.Sprintf("%d/%s", r.testNumber, test.name), func(t *testing.T) {
		test.plan = planTest(test)
		r.executePlan(t, test)

		logger.Infow("test publish summary",
			"test", test.name,
			"room", r.RoomName,
			"audioCodec", test.audioCodec,
			"audioTrackID", test.audioTrackID,
			"videoCodec", test.videoCodec,
			"videoTrackID", test.videoTrackID,
		)

		if test.custom != nil {
			test.custom(t, test)
		} else {
			r.executeTest(t, test)
		}
	})

	return !r.Short
}

func (r *Runner) executeTest(t *testing.T, test *testCase) {
	// build request
	req := r.buildRequest(test)

	egressID := r.startEgress(t, req)
	start := time.Now()

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	// create dot files if needed
	time.Sleep(time.Until(start.Add(time.Second * 10)))
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// test stream updates and RPCs
	if test.streamOptions != nil {
		ctx := context.Background()

		urls := streamUrls[test.streamOptions.outputType]

		// verify
		time.Sleep(time.Until(start.Add(time.Second * 15)))
		r.verifyStreams(t, test, p, urls[0][2])
		r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
			urls[0][1]: livekit.StreamInfo_ACTIVE,
			urls[1][1]: livekit.StreamInfo_FAILED,
		})

		// add one good stream url and one bad
		_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
			EgressId:      egressID,
			AddOutputUrls: []string{urls[2][0], urls[3][0]},
		})
		require.NoError(t, err)

		// verify
		time.Sleep(time.Until(start.Add(time.Second * 20)))
		r.verifyStreams(t, test, p, urls[0][2], urls[2][2])
		r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
			urls[0][1]: livekit.StreamInfo_ACTIVE,
			urls[1][1]: livekit.StreamInfo_FAILED,
			urls[2][1]: livekit.StreamInfo_ACTIVE,
			urls[3][1]: livekit.StreamInfo_FAILED,
		})

		// remove one of the stream urls
		_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
			EgressId:         egressID,
			RemoveOutputUrls: []string{urls[0][0]},
		})
		require.NoError(t, err)

		// verify the remaining stream
		time.Sleep(time.Until(start.Add(time.Second * 25)))
		r.verifyStreams(t, test, p, urls[2][2])
		r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
			urls[0][1]: livekit.StreamInfo_FINISHED,
			urls[1][1]: livekit.StreamInfo_FAILED,
			urls[2][1]: livekit.StreamInfo_ACTIVE,
			urls[3][1]: livekit.StreamInfo_FAILED,
		})
	}

	// stop after 30s
	time.Sleep(time.Until(start.Add(time.Second * 30)))
	res := r.stopEgress(t, egressID)

	// validate file
	if test.fileOptions != nil {
		if p.GetFileConfig().OutputType == types.OutputTypeUnknownFile {
			p.GetFileConfig().OutputType = test.fileOptions.outputType
		}

		var expectedVideoEncoding bool
		switch test.requestType {
		case types.RequestTypeTrack:
			expectedVideoEncoding = false
		case types.RequestTypeParticipant:
			expectedVideoEncoding = true
		default:
			expectedVideoEncoding = !test.audioOnly
		}
		require.Equal(t, expectedVideoEncoding, p.VideoEncoding)

		r.verifyFile(t, test, p, res)
	}

	// validate segments
	if test.segmentOptions != nil {
		require.Len(t, res.GetSegmentResults(), 1)
		segments := res.GetSegmentResults()[0]

		require.Greater(t, segments.Size, int64(0))
		require.NotContains(t, segments.PlaylistName, "{")
		require.NotContains(t, segments.PlaylistLocation, "{")
		if segments.LivePlaylistName != "" {
			require.NotContains(t, segments.LivePlaylistName, "{")
		}
		if segments.LivePlaylistLocation != "" {
			require.NotContains(t, segments.LivePlaylistLocation, "{")
		}

		require.Equal(t, !test.audioOnly, p.VideoEncoding)

		r.verifySegments(t, test, p, test.segmentOptions.suffix, res, false)
	}

	// validate stream
	if test.streamOptions != nil {
		urls := streamUrls[test.streamOptions.outputType]

		// verify egress info
		require.Empty(t, res.Error)
		require.NotZero(t, res.StartedAt)
		require.NotZero(t, res.EndedAt)

		// check stream info
		require.Len(t, res.StreamResults, 4)
		for _, info := range res.StreamResults {
			require.NotZero(t, info.StartedAt)
			require.NotZero(t, info.EndedAt)

			switch info.Url {
			case urls[0][1]:
				require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
				require.Greater(t, float64(info.Duration)/1e9, 15.0)

			case urls[2][1]:
				require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
				require.Greater(t, float64(info.Duration)/1e9, 10.0)

			default:
				require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())
			}
		}
	}

	// validate images
	if test.imageOptions != nil {
		r.verifyImages(t, p, res)
	}
}

func (r *Runner) setRoomNameForTest(test *testCase) {
	desiredRoom := r.RoomBaseName
	switch test.audioCodec {
	case types.MimeTypePCMU:
		desiredRoom = fmt.Sprintf("%s-pcmu", r.RoomBaseName)
	case types.MimeTypePCMA:
		desiredRoom = fmt.Sprintf("%s-pcma", r.RoomBaseName)
	}
	if desiredRoom == "" {
		return
	}
	r.RoomName = desiredRoom
}

func (r *Runner) awaitIdle(t *testing.T) {
	r.svc.KillAll()
	for i := 0; i < 30; i++ {
		if r.svc.IsIdle() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("service not idle after 30s")
}

func (r *Runner) startEgress(t *testing.T, req *rpc.StartEgressRequest) string {
	info := r.sendRequest(t, req)

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

func (r *Runner) sendRequest(t *testing.T, req *rpc.StartEgressRequest) *livekit.EgressInfo {
	// send start request
	info, err := r.StartEgress(context.Background(), req)

	// check returned egress info
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

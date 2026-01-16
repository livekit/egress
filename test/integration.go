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

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

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

func (r *Runner) run(t *testing.T, test *testCase, f func(*testing.T, *testCase)) bool {
	if !r.should(runRequestType[test.requestType]) {
		return true
	}

	switch test.requestType {
	case types.RequestTypeRoomComposite, types.RequestTypeWeb:
		r.sourceFramerate = 30
	case types.RequestTypeParticipant, types.RequestTypeTrackComposite, types.RequestTypeTrack:
		r.sourceFramerate = 23.97
	}

	r.awaitIdle(t)
	r.ensureRoomForTest(t, test)

	r.testNumber++
	t.Run(fmt.Sprintf("%d/%s", r.testNumber, test.name), func(t *testing.T) {
		audioMuting := r.Muting
		videoMuting := r.Muting && test.audioCodec == ""

		test.audioTrackID = r.publishSample(t, test.audioCodec, test.audioDelay, test.audioUnpublish, audioMuting)
		if test.audioRepublish != 0 {
			r.publishSample(t, test.audioCodec, test.audioRepublish, 0, audioMuting)
		}
		test.videoTrackID = r.publishSample(t, test.videoCodec, test.videoDelay, test.videoUnpublish, videoMuting)
		if test.videoRepublish != 0 {
			r.publishSample(t, test.videoCodec, test.videoRepublish, 0, videoMuting)
		}

		logger.Infow("test publish summary",
			"test", test.name,
			"room", r.RoomName,
			"audioCodec", test.audioCodec,
			"audioTrackID", test.audioTrackID,
			"videoCodec", test.videoCodec,
			"videoTrackID", test.videoTrackID,
		)

		f(t, test)
	})

	return !r.Short
}

func (r *Runner) ensureRoomForTest(t *testing.T, test *testCase) {
	desiredRoom := r.RoomBaseName
	var codecs []webrtc.RTPCodecParameters
	switch test.audioCodec {
	case types.MimeTypePCMU:
		desiredRoom = fmt.Sprintf("%s-pcmu", r.RoomBaseName)
		codecs = []webrtc.RTPCodecParameters{{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypePCMU,
				ClockRate: 8000,
				Channels:  1,
			},
			PayloadType: 0,
		}}
	case types.MimeTypePCMA:
		desiredRoom = fmt.Sprintf("%s-pcma", r.RoomBaseName)
		codecs = []webrtc.RTPCodecParameters{{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypePCMA,
				ClockRate: 8000,
				Channels:  1,
			},
			PayloadType: 8,
		}}
	}

	if desiredRoom == "" || desiredRoom == r.RoomName {
		return
	}

	r.connectRoom(t, desiredRoom, codecs)
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

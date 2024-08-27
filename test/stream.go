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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

const (
	badRtmpUrl1         = "rtmp://xxx.contribute.live-video.net/app/fake1"
	badRtmpUrl1Redacted = "rtmp://xxx.contribute.live-video.net/app/{f...1}"
	badRtmpUrl2         = "rtmp://localhost:1936/live/stream"
	badRtmpUrl2Redacted = "rtmp://localhost:1936/live/{st...am}"
	badSrtUrl1          = "srt://localhost:8891?streamid=publish:wrongport&pkt_size=1316"
	badSrtUrl2          = "srt://localhost:8891?streamid=publish:badstream&pkt_size=1316"
)

var (
	streamKey1          = utils.NewGuid("")
	streamKey2          = utils.NewGuid("")
	rtmpUrl1            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey1)
	rtmpUrl1Redacted, _ = utils.RedactStreamKey(rtmpUrl1)
	rtmpUrl2            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey2)
	rtmpUrl2Redacted, _ = utils.RedactStreamKey(rtmpUrl2)
	srtPublishUrl1      = fmt.Sprintf("srt://localhost:8890?streamid=publish:%s&pkt_size=1316", streamKey1)
	srtReadUrl1         = fmt.Sprintf("srt://localhost:8890?streamid=read:%s", streamKey1)
	srtPublishUrl2      = fmt.Sprintf("srt://localhost:8890?streamid=publish:%s&pkt_size=1316", streamKey2)
	srtReadUrl2         = fmt.Sprintf("srt://localhost:8890?streamid=read:%s", streamKey2)
)

// [[publish, redacted, verification]]
var streamUrls = map[types.OutputType][][]string{
	types.OutputTypeRTMP: {
		{rtmpUrl1, rtmpUrl1Redacted, rtmpUrl1},
		{badRtmpUrl1, badRtmpUrl1Redacted, ""},
		{rtmpUrl2, rtmpUrl2Redacted, rtmpUrl2},
		{badRtmpUrl2, badRtmpUrl2Redacted, ""},
	},
	types.OutputTypeSRT: {
		{srtPublishUrl1, srtPublishUrl1, srtReadUrl1},
		{badSrtUrl1, badSrtUrl1, ""},
		{srtPublishUrl2, srtPublishUrl2, srtReadUrl2},
		{badSrtUrl2, badSrtUrl2, ""},
	},
}

func (r *Runner) runStreamTest(t *testing.T, req *rpc.StartEgressRequest, test *testCase) {
	ctx := context.Background()
	urls := streamUrls[test.outputType]
	egressID := r.startEgress(t, req)

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)
	require.Equal(t, test.expectVideoEncoding, p.VideoEncoding)
	if test.expectVideoEncoding {
		require.Equal(t, config.StreamKeyframeInterval, p.KeyFrameInterval)
	}

	// verify
	time.Sleep(time.Second * 5)
	r.verifyStreams(t, p, urls[0][2])
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
	time.Sleep(time.Second * 5)

	// verify
	r.verifyStreams(t, p, urls[0][2], urls[2][2])
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

	time.Sleep(time.Second * 5)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// verify the remaining stream
	r.verifyStreams(t, p, urls[2][2])
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		urls[0][1]: livekit.StreamInfo_FINISHED,
		urls[1][1]: livekit.StreamInfo_FAILED,
		urls[2][1]: livekit.StreamInfo_ACTIVE,
		urls[3][1]: livekit.StreamInfo_FAILED,
	})

	// stop
	time.Sleep(time.Second * 5)
	res := r.stopEgress(t, egressID)

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

func (r *Runner) verifyStreams(t *testing.T, p *config.PipelineConfig, urls ...string) {
	for _, url := range urls {
		verify(t, url, p, nil, types.EgressTypeStream, false, r.sourceFramerate, false)
	}
}

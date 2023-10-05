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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func (r *Runner) runImagesTest(t *testing.T, req *rpc.StartEgressRequest, test *testCase) {
	egressID := r.startEgress(t, req)

	time.Sleep(time.Second * 10)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// stop
	time.Sleep(time.Second * 15)
	res := r.stopEgress(t, egressID)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	r.verifyImages(t, p, test.imageFilenameSuffix, res)
}

func (r *Runner) verifyImages(t *testing.T, p *config.PipelineConfig, filenameSuffix livekit.ImageFileSuffix, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// segments info
	require.Len(t, res.GetImageResults(), 1)
	images := res.GetImageResults()[0]

	require.Greater(t, images.ImageCount, int64(0))

	// r.verifyImagesOutput(t, p, filenameSuffix, segments.PlaylistName, segments.PlaylistLocation, int(segments.SegmentCount), res, m3u8.PlaylistTypeEvent)
	// r.verifyManifest(t, p, segments.PlaylistName)
}

//func (r *Runner) verifyManifest(t *testing.T, p *config.PipelineConfig, plName string) {
//	localPlaylistPath := fmt.Sprintf("%s/%s", r.FilePrefix, plName)
//
//	if uploadConfig := p.GetSegmentConfig().UploadConfig; uploadConfig != nil {
//		download(t, uploadConfig, localPlaylistPath+".json", plName+".json")
//	}
//}

//func (r *Runner) verifySegmentOutput(t *testing.T, p *config.PipelineConfig, filenameSuffix livekit.SegmentedFileSuffix, plName string, plLocation string, segmentCount int, res *livekit.EgressInfo, plType m3u8.PlaylistType) {
//	require.NotEmpty(t, plName)
//	require.NotEmpty(t, plLocation)

//	storedPlaylistPath := plName
//	localPlaylistPath := plName

//	// download from cloud storage
//	if uploadConfig := p.GetSegmentConfig().UploadConfig; uploadConfig != nil {
//		localPlaylistPath = fmt.Sprintf("%s/%s", r.FilePrefix, storedPlaylistPath)
//		download(t, uploadConfig, localPlaylistPath, storedPlaylistPath)
//		if plType == m3u8.PlaylistTypeEvent {
//			// Only download segments once
//			base := storedPlaylistPath[:len(storedPlaylistPath)-5]
//			for i := 0; i < int(segmentCount); i++ {
//				cloudPath := fmt.Sprintf("%s_%05d.ts", base, i)
//				localPath := fmt.Sprintf("%s/%s", r.FilePrefix, cloudPath)
//				download(t, uploadConfig, localPath, cloudPath)
//			}
//		}
//	}

// verify
//	verify(t, localPlaylistPath, p, res, types.EgressTypeSegments, r.Muting, r.sourceFramerate, plType == m3u8.PlaylistTypeLive)
//}

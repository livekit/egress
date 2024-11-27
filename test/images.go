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
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func (r *Runner) testImages(t *testing.T) {
	if !r.should(runImages) {
		return
	}

	t.Run("Images", func(t *testing.T) {
		for _, test := range []*testCase{

			// ---- Room Composite -----

			{
				name:        "RoomComposite",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeH264,
					layout:     "speaker",
				},
				encodingOptions: &livekit.EncodingOptions{
					Width:  640,
					Height: 360,
				},
				imageOptions: &imageOptions{
					prefix: "r_{room_name}_{time}",
					suffix: livekit.ImageFileSuffix_IMAGE_SUFFIX_TIMESTAMP,
				},
			},

			// ---- Track Composite ----

			{
				name:        "TrackComposite/H264",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeH264,
				},
				imageOptions: &imageOptions{
					prefix: "tc_{publisher_identity}_h264",
				},
			},
		} {
			if !r.run(t, test, r.runImagesTest) {
				return
			}
		}
	})
}

func (r *Runner) runImagesTest(t *testing.T, test *testCase) {
	req := r.build(test)

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

	r.verifyImages(t, p, res)
}

func (r *Runner) verifyImages(t *testing.T, p *config.PipelineConfig, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// image info
	require.Len(t, res.GetImageResults(), 1)
	images := res.GetImageResults()[0]

	require.Greater(t, images.ImageCount, int64(0))

	imageConfig := p.GetImageConfigs()[0]
	for i := range images.ImageCount {
		storagePath := fmt.Sprintf("%s_%05d%s", images.FilenamePrefix, i, imageConfig.ImageExtension)
		localPath := path.Join(r.FilePrefix, path.Base(storagePath))
		download(t, imageConfig.StorageConfig, localPath, storagePath, true)
	}
}

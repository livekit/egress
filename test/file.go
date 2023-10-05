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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func (r *Runner) runFileTest(t *testing.T, req *rpc.StartEgressRequest, test *testCase) {
	// start
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
	if p.GetFileConfig().OutputType == types.OutputTypeUnknownFile {
		p.GetFileConfig().OutputType = test.outputType
	}

	require.Equal(t, test.expectVideoEncoding, p.VideoEncoding)

	// verify
	r.verifyFile(t, p, res)
}

func (r *Runner) verifyFile(t *testing.T, p *config.PipelineConfig, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// file info
	fileRes := res.GetFile()
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	require.NotEmpty(t, fileRes.Location)
	require.Greater(t, fileRes.Size, int64(0))
	require.Greater(t, fileRes.Duration, int64(0))

	storagePath := fileRes.Filename
	localPath := fileRes.Filename
	require.NotEmpty(t, storagePath)
	require.False(t, strings.Contains(storagePath, "{"))

	// download from cloud storage
	if uploadConfig := p.GetFileConfig().UploadConfig; uploadConfig != nil {
		localPath = fmt.Sprintf("%s/%s", r.FilePrefix, storagePath)
		download(t, uploadConfig, localPath, storagePath)
		download(t, uploadConfig, localPath+".json", storagePath+".json")
	}

	// verify
	verify(t, localPath, p, res, types.EgressTypeFile, r.Muting, r.sourceFramerate, false)
}

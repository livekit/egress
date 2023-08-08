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

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
)

func (p *Pipeline) GetGstPipelineDebugDot() string {
	return p.pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
}

func (p *Pipeline) uploadDebugFiles() {
	u, err := uploader.New(p.Debug.ToUploadConfig(), "")
	if err != nil {
		logger.Errorw("failed to create uploader", err)
		return
	}

	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		p.uploadDotFile(u)
	}()
	go func() {
		defer wg.Done()
		p.uploadPProf(u)
	}()
	go func() {
		defer wg.Done()
		p.uploadTrackFiles(u)
	}()
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(time.Second * 3):
		logger.Errorw("failed to upload debug files", errors.New("timed out"))
	}
}

func (p *Pipeline) uploadTrackFiles(u uploader.Uploader) {
	var dir string
	if p.Debug.ToUploadConfig() == nil {
		dir = p.Debug.PathPrefix
	} else {
		dir = p.TmpDir
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".csv") {
			local := path.Join(dir, f.Name())
			storage := path.Join(p.Debug.PathPrefix, f.Name())
			_, _, err = u.Upload(local, storage, types.OutputTypeBlob, false)
			if err != nil {
				logger.Errorw("failed to upload debug file", err)
				return
			}
		}
	}
}

func (p *Pipeline) uploadDotFile(u uploader.Uploader) {
	dot := p.GetGstPipelineDebugDot()
	p.uploadDebugFile(u, []byte(dot), ".dot")
}

func (p *Pipeline) uploadPProf(u uploader.Uploader) {
	b, err := pprof.GetProfileData(context.Background(), "goroutine", 0, 0)
	if err != nil {
		logger.Errorw("failed to get profile data", err)
		return
	}
	p.uploadDebugFile(u, b, ".prof")
}

func (p *Pipeline) uploadDebugFile(u uploader.Uploader, data []byte, fileExtension string) {
	filename := fmt.Sprintf("%s%s", p.Info.EgressId, fileExtension)
	local := path.Join(config.TmpDir, filename)
	storage := path.Join(p.Debug.PathPrefix, filename)

	f, err := os.Create(local)
	if err != nil {
		logger.Errorw("failed to create debug file", err)
		return
	}
	defer f.Close()

	_, err = f.Write(data)
	if err != nil {
		logger.Errorw("failed to write debug file", err)
		return
	}

	_, _, err = u.Upload(local, storage, types.OutputTypeBlob, false)
	if err != nil {
		logger.Errorw("failed to upload debug file", err)
		return
	}
}

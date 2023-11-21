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

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
)

func (c *Controller) GetGstPipelineDebugDot() string {
	return c.p.DebugBinToDotData(gst.DebugGraphShowAll)
}

func (c *Controller) uploadDebugFiles() {
	u, err := uploader.New(c.Debug.ToUploadConfig(), "", c.monitor)
	if err != nil {
		logger.Errorw("failed to create uploader", err)
		return
	}

	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		c.uploadDotFile(u)
	}()
	go func() {
		defer wg.Done()
		c.uploadPProf(u)
	}()
	go func() {
		defer wg.Done()
		c.uploadTrackFiles(u)
	}()
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Infow("debug files uploaded")
		return
	case <-time.After(time.Second * 3):
		logger.Errorw("failed to upload debug files", errors.New("timed out"))
	}
}

func (c *Controller) uploadTrackFiles(u uploader.Uploader) {
	var dir string
	if c.Debug.ToUploadConfig() == nil {
		dir = c.Debug.PathPrefix
	} else {
		dir = c.TmpDir
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".csv") {
			local := path.Join(dir, f.Name())
			storage := path.Join(c.Debug.PathPrefix, f.Name())
			_, _, err = u.Upload(local, storage, types.OutputTypeBlob, false, "track")
			if err != nil {
				logger.Errorw("failed to upload debug file", err)
				return
			}
		}
	}
}

func (c *Controller) uploadDotFile(u uploader.Uploader) {
	dot := c.GetGstPipelineDebugDot()
	c.uploadDebugFile(u, []byte(dot), ".dot")
}

func (c *Controller) uploadPProf(u uploader.Uploader) {
	b, err := pprof.GetProfileData(context.Background(), "goroutine", 0, 0)
	if err != nil {
		logger.Errorw("failed to get profile data", err)
		return
	}
	c.uploadDebugFile(u, b, ".prof")
}

func (c *Controller) uploadDebugFile(u uploader.Uploader, data []byte, fileExtension string) {
	var dir string
	if c.Debug.ToUploadConfig() == nil {
		dir = c.Debug.PathPrefix
	} else {
		dir = c.TmpDir
	}

	filename := fmt.Sprintf("%s%s", c.Info.EgressId, fileExtension)
	local := path.Join(dir, filename)
	storage := path.Join(c.Debug.PathPrefix, filename)

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

	_, _, err = u.Upload(local, storage, types.OutputTypeBlob, false, "debug")
	if err != nil {
		logger.Errorw("failed to upload debug file", err)
		return
	}
}

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
	"time"

	"github.com/go-gst/go-gst/gst"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
)

func (c *Controller) GetGstPipelineDebugDot() (string, error) {
	dot := make(chan string, 1)
	go func() {
		dot <- c.p.DebugBinToDotData(gst.DebugGraphShowAll)
	}()

	select {
	case d := <-dot:
		return d, nil
	case <-time.After(3 * time.Second):
		return "", status.New(codes.DeadlineExceeded, "timed out requesting pipeline debug info").Err()
	}
}

func (c *Controller) generateDotFile() {
	dot, err := c.GetGstPipelineDebugDot()
	if err != nil {
		return
	}

	f, err := os.Create(path.Join(c.TmpDir, fmt.Sprintf("%s.dot", c.Info.EgressId)))
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.WriteString(dot)
}

func (c *Controller) generatePProf() {
	b, err := pprof.GetProfileData(context.Background(), "goroutine", 0, 0)
	if err != nil {
		logger.Errorw("failed to get profile data", err)
		return
	}

	f, err := os.Create(path.Join(c.TmpDir, fmt.Sprintf("%s.prof", c.Info.EgressId)))
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.Write(b)
}

var debugFileExtensions = map[string]struct{}{
	"csv":  {},
	"dot":  {},
	"prof": {},
	"log":  {},
}

func (c *Controller) uploadDebugFiles() {
	files, err := os.ReadDir(c.TmpDir)
	if err != nil {
		logger.Errorw("failed to read tmp dir", err)
		return
	}

	var u *uploader.Uploader

	for _, f := range files {
		s := strings.Split(f.Name(), ".")
		if _, ok := debugFileExtensions[s[len(s)-1]]; !ok {
			continue
		}

		if u == nil {
			u, err = uploader.New(&c.Debug.StorageConfig, nil, c.monitor, nil)
			if err != nil {
				logger.Errorw("failed to create uploader", err)
				return
			}
		}

		local := path.Join(c.TmpDir, f.Name())
		storage := path.Join(c.Info.EgressId, f.Name())
		_, _, err = u.Upload(local, storage, types.OutputTypeBlob, false)
		if err != nil {
			logger.Errorw("failed to upload debug file", err)
			return
		}
	}
}

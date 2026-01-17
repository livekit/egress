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

func sanitizeDebugFilenameComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func (c *Controller) writeDotFile(filename, contents string) {
	f, err := os.Create(path.Join(c.TmpDir, filename))
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.WriteString(contents)
}

func (c *Controller) generateDotFile(reason string) {
	dot, err := c.GetGstPipelineDebugDot()
	if err != nil {
		logger.Errorw("failed to get gst pipeline debug dot", err)
		return
	}

	// always write the canonical file name for easy discovery
	c.writeDotFile(fmt.Sprintf("%s.dot", c.Info.EgressId), dot)

	if reason == "" {
		logger.Errorw("failed to get gst pipeline debug dot, reason is empty", nil)
		return
	}

	// make sure all dot captures for the egressID are written with timestamp suffix
	var suffixParts []string
	if ext := sanitizeDebugFilenameComponent(reason); ext != "" {
		suffixParts = append(suffixParts, ext)
	}
	suffixParts = append(suffixParts, time.Now().UTC().Format("20060102T150405Z"))

	filename := fmt.Sprintf("%s_%s.dot", c.Info.EgressId, strings.Join(suffixParts, "_"))
	c.writeDotFile(filename, dot)
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

var debugFileDataTypes = map[string]types.OutputType{
	"csv":  "text/csv",
	"dot":  types.OutputTypeBlob,
	"prof": types.OutputTypeBlob,
	"log":  "text/plain",
}

func (c *Controller) uploadDebugFiles() {
	files, err := os.ReadDir(c.TmpDir)
	if err != nil {
		logger.Errorw("failed to read tmp dir", err)
		return
	}

	var u *uploader.Uploader

	for _, f := range files {
		info, err := f.Info()
		if err != nil || info.Size() == 0 {
			continue
		}

		s := strings.Split(f.Name(), ".")
		outputType, ok := debugFileDataTypes[s[len(s)-1]]
		if !ok {
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
		_, _, err = u.Upload(local, storage, outputType, false)
		if err != nil {
			logger.Errorw("failed to upload debug file", err, "filename", local)
			return
		}
	}
}

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path"
	"sync"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/pprof"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
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
	wg.Add(2)

	go func() {
		defer wg.Done()
		p.uploadDotFile(u)
	}()
	go func() {
		defer wg.Done()
		p.uploadPProf(u)
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

func (p *Pipeline) uploadDotFile(u uploader.Uploader) {
	dot := p.GetGstPipelineDebugDot()
	p.uploadDebugFile(u, []byte(dot), ".dot")
}

func (p *Pipeline) uploadPProf(u uploader.Uploader) {
	b, err := pprof.GetProfileData(context.Background(), "heap", 0, 0)
	if err != nil {
		logger.Errorw("failed to get profile data", err)
		return
	}
	p.uploadDebugFile(u, b, ".prof")
}

func (p *Pipeline) uploadDebugFile(u uploader.Uploader, data []byte, fileExtension string) {
	filename := fmt.Sprintf("%s%s", p.Info.EgressId, fileExtension)
	filepath := path.Join(config.TmpDir, filename)

	f, err := os.Create(filepath)
	if err != nil {
		logger.Errorw("failed to create dotfile", err)
		return
	}
	defer f.Close()

	_, err = f.Write(data)
	if err != nil {
		logger.Errorw("failed to write dotfile", err)
		return
	}

	_, _, err = u.Upload(filepath, filename, types.OutputTypeBlob, false)
	if err != nil {
		logger.Errorw("failed to upload dotfile", err)
		return
	}
}

package recorder

import (
	"fmt"
	"strings"
	"time"

	"github.com/livekit/protocol/logger"
	livekit "github.com/livekit/protocol/proto"

	"github.com/livekit/livekit-recorder/pkg/config"
	"github.com/livekit/livekit-recorder/pkg/display"
	"github.com/livekit/livekit-recorder/pkg/pipeline"
)

type Recorder struct {
	ID string

	conf     *config.Config
	req      *livekit.StartRecordingRequest
	display  *display.Display
	pipeline *pipeline.Pipeline

	url      string
	filename string
	filepath string
}

func NewRecorder(conf *config.Config, recordingID string) *Recorder {
	return &Recorder{
		ID:   recordingID,
		conf: conf,
	}
}

// Run blocks until completion
func (r *Recorder) Run() *livekit.RecordingResult {
	res := &livekit.RecordingResult{Id: r.ID}

	r.display = display.New()
	options := r.req.Options
	err := r.display.Launch(r.url, int(options.Width), int(options.Height), int(options.Depth))
	if err != nil {
		logger.Errorw("error launching display", err)
		res.Error = err.Error()
		return res
	}

	if r.req == nil {
		res.Error = "recorder not initialized"
		return res
	}

	r.pipeline, err = r.getPipeline(r.req)
	if err != nil {
		logger.Errorw("error building pipeline", err)
		res.Error = err.Error()
		return res
	}

	// wait for START_RECORDING console log
	if strings.HasPrefix(r.url, "https://recorder.livekit.io") {
		r.display.WaitForRoom()
	}
	// stop on END_RECORDING console log
	go func(d *display.Display) {
		<-d.EndMessage()
		r.Stop()
	}(r.display)

	start := time.Now()
	err = r.pipeline.Start()
	if err != nil {
		logger.Errorw("error running pipeline", err)
		res.Error = err.Error()
		return res
	}
	res.Duration = time.Since(start).Milliseconds() / 1000

	if r.filename != "" && r.conf.FileOutput.S3 != nil {
		if err = r.upload(); err != nil {
			res.Error = err.Error()
			return res
		}
		res.DownloadUrl = fmt.Sprintf("s3://%s/%s", r.conf.FileOutput.S3.Bucket, r.filepath)
	} else if r.filename != "" && r.conf.FileOutput.Azblob != nil {
		if err = r.uploadAzblob(); err != nil {
			res.Error = err.Error()
			return res
		}
		res.DownloadUrl = fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s",
			r.conf.FileOutput.Azblob.AccountName,
			r.conf.FileOutput.Azblob.ContainerName,
			r.filepath,
		)
	}

	return res
}

func (r *Recorder) getPipeline(req *livekit.StartRecordingRequest) (*pipeline.Pipeline, error) {
	switch req.Output.(type) {
	case *livekit.StartRecordingRequest_Rtmp:
		return pipeline.NewRtmpPipeline(req.Output.(*livekit.StartRecordingRequest_Rtmp).Rtmp.Urls, req.Options)
	case *livekit.StartRecordingRequest_Filepath:
		return pipeline.NewFilePipeline(r.filename, req.Options)
	}
	return nil, ErrNoOutput
}

func (r *Recorder) AddOutput(url string) error {
	logger.Debugw("add output", "url", url)
	if r.pipeline == nil {
		return pipeline.ErrPipelineNotFound
	}
	return r.pipeline.AddOutput(url)
}

func (r *Recorder) RemoveOutput(url string) error {
	logger.Debugw("remove output", "url", url)
	if r.pipeline == nil {
		return pipeline.ErrPipelineNotFound
	}
	return r.pipeline.RemoveOutput(url)
}

func (r *Recorder) Stop() {
	if p := r.pipeline; p != nil {
		p.Close()
	}
}

// should only be called after pipeline completes
func (r *Recorder) Close() {
	r.display.Close()
}

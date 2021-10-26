package recorder

import (
	"fmt"
	"time"

	"github.com/livekit/protocol/logger"
	livekit "github.com/livekit/protocol/proto"

	"github.com/livekit/livekit-recorder/pkg/config"
	"github.com/livekit/livekit-recorder/pkg/display"
	"github.com/livekit/livekit-recorder/pkg/pipeline"
)

type Recorder struct {
	conf *config.Config

	req      *livekit.StartRecordingRequest
	url      string
	filename string

	display  *display.Display
	pipeline *pipeline.Pipeline
}

func NewRecorder(conf *config.Config) *Recorder {
	return &Recorder{
		conf: conf,
	}
}

// Run blocks until completion
func (r *Recorder) Run(recordingId string) *livekit.RecordingResult {
	r.display = display.New()
	options := r.req.Options
	err := r.display.Launch(r.url, int(options.Width), int(options.Height), int(options.Depth))

	res := &livekit.RecordingResult{Id: recordingId}
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

	start := time.Now()
	err = r.pipeline.Start()
	if err != nil {
		logger.Errorw("error running pipeline", err)
		res.Error = err.Error()
		return res
	}
	res.Duration = time.Since(start).Milliseconds() / 1000

	if r.filename != "" && r.conf.FileOutput.S3 != nil {
		url := fmt.Sprintf("s3://%s/%s", r.conf.FileOutput.S3.Bucket, r.filename)
		if err = r.upload(url); err != nil {
			res.Error = err.Error()
			return res
		}
		res.DownloadUrl = url
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

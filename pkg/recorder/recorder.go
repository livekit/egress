package recorder

import (
	"fmt"
	"sync"
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

	// result info
	sync.Mutex
	result     *livekit.RecordingInfo
	startTimes map[string]time.Time
}

func NewRecorder(conf *config.Config, recordingID string) *Recorder {
	return &Recorder{
		ID:   recordingID,
		conf: conf,
		result: &livekit.RecordingInfo{
			Id: recordingID,
		},
		startTimes: make(map[string]time.Time),
	}
}

// Run blocks until completion
func (r *Recorder) Run() *livekit.RecordingInfo {
	r.display = display.New()
	options := r.req.Options
	err := r.display.Launch(r.conf, r.url, int(options.Width), int(options.Height), int(options.Depth))
	if err != nil {
		logger.Errorw("error launching display", err)
		r.result.Error = err.Error()
		return r.result
	}

	if r.req == nil {
		r.result.Error = "recorder not initialized"
		return r.result
	}

	r.pipeline, err = r.getPipeline(r.req)
	if err != nil {
		logger.Errorw("error building pipeline", err)
		r.result.Error = err.Error()
		return r.result
	}

	// wait for START_RECORDING console log if using template
	switch r.req.Input.(type) {
	case *livekit.StartRecordingRequest_Template:
		r.display.WaitForRoom()
	}

	// stop on END_RECORDING console log
	go func(d *display.Display) {
		<-d.EndMessage()
		r.Stop()
	}(r.display)

	start := time.Now()
	switch output := r.req.Output.(type) {
	case *livekit.StartRecordingRequest_Rtmp:
		r.Lock()
		for _, url := range output.Rtmp.Urls {
			r.startTimes[url] = start
		}
		r.Unlock()
	}

	err = r.pipeline.Start()
	if err != nil {
		logger.Errorw("error running pipeline", err)
		r.result.Error = err.Error()
		return r.result
	}

	switch r.req.Output.(type) {
	case *livekit.StartRecordingRequest_Rtmp:
		for url, startTime := range r.startTimes {
			r.result.Rtmp = append(r.result.Rtmp, &livekit.RtmpResult{
				StreamUrl: url,
				Duration:  time.Since(startTime).Milliseconds() / 1000,
			})
		}
	case *livekit.StartRecordingRequest_Filepath:
		r.result.File = &livekit.FileResult{
			Duration: time.Since(start).Milliseconds() / 1000,
		}

		if r.conf.FileOutput.S3 != nil {
			if err = r.uploadS3(); err != nil {
				r.result.Error = err.Error()
				return r.result
			}
			r.result.File.DownloadUrl = fmt.Sprintf("s3://%s/%s", r.conf.FileOutput.S3.Bucket, r.filepath)
		} else if r.conf.FileOutput.Azblob != nil {
			if err = r.uploadAzure(); err != nil {
				r.result.Error = err.Error()
				return r.result
			}
			r.result.File.DownloadUrl = fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s",
				r.conf.FileOutput.Azblob.AccountName,
				r.conf.FileOutput.Azblob.ContainerName,
				r.filepath)
		}
	}

	return r.result
}

func (r *Recorder) getPipeline(req *livekit.StartRecordingRequest) (*pipeline.Pipeline, error) {
	switch output := req.Output.(type) {
	case *livekit.StartRecordingRequest_Rtmp:
		return pipeline.NewRtmpPipeline(output.Rtmp.Urls, req.Options)
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

	start := time.Now()
	if err := r.pipeline.AddOutput(url); err != nil {
		return err
	}

	r.Lock()
	r.startTimes[url] = start
	r.Unlock()

	return nil
}

func (r *Recorder) RemoveOutput(url string) error {
	logger.Debugw("remove output", "url", url)
	if r.pipeline == nil {
		return pipeline.ErrPipelineNotFound
	}
	if err := r.pipeline.RemoveOutput(url); err != nil {
		return err
	}

	r.Lock()
	if start, ok := r.startTimes[url]; ok {
		r.result.Rtmp = append(r.result.Rtmp, &livekit.RtmpResult{
			StreamUrl: url,
			Duration:  time.Since(start).Milliseconds() / 1000,
		})
		delete(r.startTimes, url)
	}
	r.Unlock()

	return nil
}

func (r *Recorder) Stop() {
	if p := r.pipeline; p != nil {
		p.Close()
	}
}

// should only be called after pipeline completes
func (r *Recorder) Close() {
	if r.display != nil {
		r.display.Close()
	}
}

package recorder

import (
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

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
	abort    chan struct{}

	isTemplate bool
	url        string
	filename   string
	filepath   string

	// result info
	mu        sync.Mutex
	result    *livekit.RecordingInfo
	startedAt map[string]time.Time
}

func NewRecorder(conf *config.Config, recordingID string) *Recorder {
	return &Recorder{
		ID:    recordingID,
		conf:  conf,
		abort: make(chan struct{}),
		result: &livekit.RecordingInfo{
			Id: recordingID,
		},
		startedAt: make(map[string]time.Time),
	}
}

// Run blocks until completion
func (r *Recorder) Run() *livekit.RecordingInfo {
	var err error

	// check for request
	if r.req == nil {
		r.result.Error = "recorder not initialized"
		return r.result
	}

	// launch display
	r.display, err = display.Launch(r.conf, r.url, r.req.Options, r.isTemplate)
	if err != nil {
		logger.Errorw("error launching display", err)
		r.result.Error = err.Error()
		return r.result
	}

	// create pipeline
	r.pipeline, err = r.createPipeline(r.req)
	if err != nil {
		logger.Errorw("error building pipeline", err)
		r.result.Error = err.Error()
		return r.result
	}

	// if using template, listen for START_RECORDING and END_RECORDING messages
	if r.isTemplate {
		logger.Infow("Waiting for room to start")
		select {
		case <-r.display.RoomStarted():
			logger.Infow("Room started")
		case <-r.abort:
			r.pipeline.Abort()
			logger.Infow("Recording aborted while waiting for room")
			r.result.Error = "Recording aborted"
			return r.result
		}

		// stop on END_RECORDING console log
		go func(d *display.Display) {
			<-d.RoomEnded()
			r.Stop()
		}(r.display)
	}

	var startedAt time.Time
	go func() {
		r.mu.Lock()
		defer r.mu.Unlock()

		startedAt = r.pipeline.GetStartTime()
		switch output := r.req.Output.(type) {
		case *livekit.StartRecordingRequest_Rtmp:
			for _, url := range output.Rtmp.Urls {
				r.startedAt[url] = startedAt
			}
		}
	}()

	// run pipeline
	err = r.pipeline.Run()
	if err != nil {
		logger.Errorw("error running pipeline", err)
		r.result.Error = err.Error()
		return r.result
	}

	switch r.req.Output.(type) {
	case *livekit.StartRecordingRequest_Rtmp:
		for url, startTime := range r.startedAt {
			r.result.Rtmp = append(r.result.Rtmp, &livekit.RtmpResult{
				StreamUrl: url,
				Duration:  time.Since(startTime).Milliseconds() / 1000,
			})
		}
	case *livekit.StartRecordingRequest_Filepath:
		r.result.File = &livekit.FileResult{
			Duration: time.Since(startedAt).Milliseconds() / 1000,
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
		} else if r.conf.FileOutput.GCPConfig != nil {
			if err = r.uploadGCP(); err != nil {
				r.result.Error = err.Error()
				return r.result
			}
			r.result.File.DownloadUrl = fmt.Sprintf("gs://%s/%s", r.conf.FileOutput.GCPConfig.Bucket, r.filepath)
		}
	}

	return r.result
}

func (r *Recorder) createPipeline(req *livekit.StartRecordingRequest) (*pipeline.Pipeline, error) {
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

	if err := r.pipeline.AddOutput(url); err != nil {
		return err
	}
	startedAt := time.Now()

	r.mu.Lock()
	r.startedAt[url] = startedAt
	r.mu.Unlock()

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
	endedAt := time.Now()

	r.mu.Lock()
	if startedAt, ok := r.startedAt[url]; ok {
		r.result.Rtmp = append(r.result.Rtmp, &livekit.RtmpResult{
			StreamUrl: url,
			Duration:  endedAt.Sub(startedAt).Milliseconds() / 1000,
		})
		delete(r.startedAt, url)
	}
	r.mu.Unlock()

	return nil
}

func (r *Recorder) Stop() {
	select {
	case <-r.abort:
		return
	default:
		close(r.abort)
		if p := r.pipeline; p != nil {
			p.Close()
		}
	}
}

// should only be called after pipeline completes
func (r *Recorder) Close() {
	if r.display != nil {
		r.display.Close()
	}
}

package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/livekit/protocol/logger"
	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/recording"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recorder/pkg/recorder"
)

func (s *Service) handleRecording(rec *recorder.Recorder) {
	// subscribe to request channel
	requests, err := s.bus.Subscribe(s.ctx, recording.RequestChannel(rec.ID))
	if err != nil {
		return
	}
	defer requests.Close()

	// ready to accept requests
	err = s.handleResponse(rec.ID, "", nil)
	if err != nil {
		return
	}

	// listen for rpcs
	logger.Debugw("waiting for requests", "recordingId", rec.ID)
	result := make(chan *livekit.RecordingResult, 1)
	for {
		select {
		case <-s.kill:
			// kill signal received, stop recorder
			if status := s.status.Load(); status != Stopping {
				s.status.Store(Stopping)
				rec.Stop()
			}
		case res := <-result:
			// recording stopped, send results to result channel
			LogResult(res)
			if err = s.bus.Publish(s.ctx, recording.ResultChannel, res); err != nil {
				logger.Errorw("failed to write results", err)
			}

			// clean up
			rec.Close()
			return
		case msg := <-requests.Channel():
			// unmarshal request
			req := &livekit.RecordingRequest{}
			err = proto.Unmarshal(requests.Payload(msg), req)
			if err != nil {
				logger.Errorw("failed to read request", err, "recordingId", rec.ID)
				continue
			}

			s.handleRequest(rec, req, result)
		}
	}
}

func (s *Service) handleRequest(rec *recorder.Recorder, req *livekit.RecordingRequest, result chan *livekit.RecordingResult) {
	logger.Debugw("handling request", "recordingId", rec.ID, "requestId", req.RequestId)
	var err error
	switch req.Request.(type) {
	case *livekit.RecordingRequest_Start:
		if status := s.status.Load(); status != Reserved {
			err = fmt.Errorf("tried calling start with state %s", status)
			break
		}

		// launch recorder
		start := req.Request.(*livekit.RecordingRequest_Start).Start
		err = rec.Validate(start)
		if err != nil {
			break
		}

		s.status.Store(Recording)
		go func() {
			// blocks until recorder is finished
			result <- rec.Run()
		}()
	case *livekit.RecordingRequest_AddOutput:
		if status := s.status.Load(); status != Recording {
			err = fmt.Errorf("tried calling AddOutput with status %s", status)
			break
		}
		err = rec.AddOutput(req.Request.(*livekit.RecordingRequest_AddOutput).AddOutput.RtmpUrl)
	case *livekit.RecordingRequest_RemoveOutput:
		if status := s.status.Load(); status != Recording {
			err = fmt.Errorf("tried calling RemoveOutput with status %s", status)
			break
		}
		err = rec.RemoveOutput(req.Request.(*livekit.RecordingRequest_RemoveOutput).RemoveOutput.RtmpUrl)
	case *livekit.RecordingRequest_End:
		if status := s.status.Load(); status != Recording {
			err = fmt.Errorf("tried calling End with status %s", status)
			break
		}
		s.status.Store(Stopping)
		rec.Stop()
	}

	_ = s.handleResponse(rec.ID, req.RequestId, err)
}

func (s *Service) handleResponse(recordingId, requestId string, err error) error {
	logger.Debugw("sending response", "recordingId", recordingId, "requestId", requestId)

	var message string
	if err != nil {
		logger.Errorw("error handling request", err,
			"recordingId", recordingId, "requestId", requestId)
		message = err.Error()
	}

	return s.bus.Publish(s.ctx, recording.ResponseChannel(recordingId), &livekit.RecordingResponse{
		RequestId: requestId,
		Error:     message,
	})
}

func LogResult(res *livekit.RecordingResult) {
	if res.Error != "" {
		logger.Errorw("recording failed", errors.New(res.Error), "recordingId", res.Id)
	} else {
		values := []interface{}{"recordingId", res.Id, "duration", time.Duration(res.Duration * 1e9)}
		if res.DownloadUrl != "" {
			values = append(values, "url", res.DownloadUrl)
		}
		logger.Infow("recording complete", values...)
	}
}

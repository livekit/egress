// +build !test

package pipeline

import (
	"errors"
	"fmt"
	"strings"

	"github.com/livekit/protocol/logger"
	"github.com/tinyzimmer/go-gst/gst"
)

var (
	ErrCannotAddToFile      = errors.New("cannot add rtmp output to file recording")
	ErrCannotRemoveFromFile = errors.New("cannot remove rtmp output from file recording")
	ErrGhostPadFailed       = errors.New("failed to add ghost pad to bin")
	ErrOutputAlreadyExists  = errors.New("output already exists")
	ErrOutputNotFound       = errors.New("output not found")

	GErrNoURI            = "No URI set before starting"
	GErrFailedToStart    = "Failed to start"
	GErrCouldNotConnect  = "Could not connect to RTMP stream"
	GErrStreamingStopped = "streaming stopped, reason error (-5)"
)

// handleError returns true if the error has been handled, false if the pipeline should quit
func (p *Pipeline) handleError(gErr *gst.GError) bool {
	element, reason, ok := parseDebugInfo(gErr.DebugString())
	if !ok {
		logger.Errorw("failed to parse pipeline error", errors.New(gErr.Error()),
			"code", gErr.Code(),
			"message", gErr.Message(),
			"debug", gErr.DebugString(),
		)
		return false
	}

	switch reason {
	case GErrNoURI, GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.output.RemoveSinkByName(element); err != nil {
			logger.Errorw("failed to remove sink", err)
			return false
		}
		p.removed[element] = true
		return true
	case GErrFailedToStart:
		// returned after an added rtmp sink failed to start
		// should be preceded by GErrNoURI on the same sink
		return p.removed[element]
	case GErrStreamingStopped:
		// returned by queue after rtmp sink could not connect
		// should be preceded by GErrCouldNotConnect on associated sink
		if strings.HasPrefix(element, "queue_") {
			return p.removed[fmt.Sprint("sink_", element[6:])]
		}
		return false
	default:
		// input failure or file write failure. Fatal
		logger.Errorw("unrecognized pipeline error", errors.New(gErr.Error()),
			"code", gErr.Code(),
			"message", gErr.Message(),
			"debug", gErr.DebugString(),
		)
		return false
	}
}

// Debug info comes in the following format:
// file.c(line): method_name (): /GstPipeline:pipeline/GstBin:bin_name/GstElement:element_name:\nError message
func parseDebugInfo(debug string) (element string, reason string, ok bool) {
	end := strings.Index(debug, ":\n")
	if end == -1 {
		return
	}
	start := strings.LastIndex(debug[:end], ":")
	if start == -1 {
		return
	}
	element = debug[start+1 : end]
	reason = debug[end+2:]
	if strings.HasPrefix(reason, GErrCouldNotConnect) {
		reason = GErrCouldNotConnect
	}
	ok = true
	return
}

package pipeline

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/input"
	"github.com/livekit/egress/pkg/pipeline/output"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
)

// gst.Init needs to be called before using gst but after gst package loads
var initialized = false

const (
	pipelineSource    = "pipeline"
	fileKey           = "file"
	eosTimeout        = time.Second * 15
	maxPendingUploads = 100

	FragmentOpenedMessage = "splitmuxsink-fragment-opened"
	FragmentClosedMessage = "splitmuxsink-fragment-closed"
	FragmentLocation      = "location"
	FragmentRunningTime   = "running-time"
)

type Pipeline struct {
	*params.Params

	// gstreamer
	pipeline *gst.Pipeline
	in       *input.Bin
	out      *output.Bin
	loop     *glib.MainLoop

	// internal
	mu             sync.Mutex
	playing        bool
	startedAt      map[string]int64
	removed        map[string]bool
	closed         chan struct{}
	eosTimer       *time.Timer
	playlistWriter *sink.PlaylistWriter
	endedSegments  chan segmentUpdate
	segmentsWg     sync.WaitGroup

	// callbacks
	onStatusUpdate func(*livekit.EgressInfo)
}

type segmentUpdate struct {
	endTime   int64
	localPath string
}

func New(conf *config.Config, p *params.Params) (*Pipeline, error) {
	if !initialized {
		gst.Init(nil)
		initialized = true
	}

	// create input bin
	in, err := input.Build(conf, p)
	if err != nil {
		return nil, err
	}

	// create output bin
	out, err := output.Build(p)
	if err != nil {
		return nil, err
	}

	// create pipeline
	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, err
	}

	// add bins to pipeline
	if err = pipeline.Add(in.Element()); err != nil {
		return nil, err
	}

	// link input elements
	if err = in.Link(); err != nil {
		return nil, err
	}

	// link output elements. There is no "out" for HLS
	if out != nil {
		if err = pipeline.Add(out.Element()); err != nil {
			return nil, err
		}

		if err = out.Link(); err != nil {
			return nil, err
		}
		// link bins
		if err := in.Bin().Link(out.Element()); err != nil {
			return nil, err
		}
	}

	var playlistWriter *sink.PlaylistWriter
	if p.OutputType == params.OutputTypeHLS {
		playlistWriter, err = sink.NewPlaylistWriter(p)
		if err != nil {
			return nil, err
		}
	}

	return &Pipeline{
		Params:         p,
		pipeline:       pipeline,
		in:             in,
		out:            out,
		playlistWriter: playlistWriter,
		startedAt:      make(map[string]int64),
		removed:        make(map[string]bool),
		closed:         make(chan struct{}),
	}, nil
}

func (p *Pipeline) GetInfo() *livekit.EgressInfo {
	return p.Info
}

func (p *Pipeline) OnStatusUpdate(f func(info *livekit.EgressInfo)) {
	p.onStatusUpdate = f
}

func (p *Pipeline) Run() *livekit.EgressInfo {
	p.Info.StartedAt = time.Now().UnixNano()
	defer func() {
		p.Info.EndedAt = time.Now().UnixNano()

		// update status
		if p.Info.Error != "" {
			p.Info.Status = livekit.EgressStatus_EGRESS_FAILED
		} else if p.Info.Status != livekit.EgressStatus_EGRESS_ABORTED {
			p.Info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		}
	}()

	// wait until room is ready
	start := p.in.StartRecording()
	if start != nil {
		select {
		case <-p.closed:
			p.in.Close()
			p.Info.Status = livekit.EgressStatus_EGRESS_ABORTED
			return p.Info
		case <-start:
			// continue
		}
	}

	// close when room ends
	go func() {
		<-p.in.EndRecording()
		p.SendEOS()
	}()

	// add watch
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(p.messageWatch)

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		p.Logger.Errorw("failed to set pipeline state", err)
		p.Info.Error = err.Error()
		return p.Info
	}

	if p.EgressType == params.EgressTypeSegmentedFile {
		p.startSegmentWorker()
		defer close(p.endedSegments)
	}

	// run main loop
	p.loop.Run()

	// close input source
	p.in.Close()

	// update endedAt from sdk source
	switch s := p.in.Source.(type) {
	case *source.SDKSource:
		p.updateDuration(s.GetEndTime())
	}

	// return if there was an error
	if p.Info.Error != "" {
		return p.Info
	}

	// upload file
	if p.EgressType == params.EgressTypeFile {
		var err error
		p.FileInfo.Location, p.FileInfo.Size, err = p.storeFile(p.Filename, p.Params.Filepath, p.Params.OutputType)
		if err != nil {
			p.Info.Error = err.Error()
		}
	}

	// wait for all pending upload jobs to finish
	if p.endedSegments != nil {
		p.segmentsWg.Wait()
	}

	if p.playlistWriter != nil {
		p.playlistWriter.EOS()
		// upload the finalized playlist
		destinationPlaylistPath := p.GetTargetPathForFilename(p.PlaylistFilename)
		p.SegmentsInfo.PlaylistLocation, _, _ = p.storeFile(p.PlaylistFilename, destinationPlaylistPath, p.Params.OutputType)
	}

	return p.Info
}

func (p *Pipeline) storeFile(localFilePath, requestedPath string, mime params.OutputType) (destinationUrl string, size int64, err error) {
	fileInfo, err := os.Stat(localFilePath)
	if err == nil {
		size = fileInfo.Size()
	} else {
		p.Logger.Errorw("could not read file size", err)
	}

	var location string
	deleteFile := true

	switch u := p.FileUpload.(type) {
	case *livekit.S3Upload:
		location = "S3"
		p.Logger.Debugw("uploading to s3")
		destinationUrl, err = sink.UploadS3(u, localFilePath, requestedPath, mime)
	case *livekit.GCPUpload:
		location = "GCP"
		p.Logger.Debugw("uploading to gcp")
		destinationUrl, err = sink.UploadGCP(u, localFilePath, requestedPath, mime)
	case *livekit.AzureBlobUpload:
		location = "Azure"
		p.Logger.Debugw("uploading to azure")
		destinationUrl, err = sink.UploadAzure(u, localFilePath, requestedPath, mime)
	default:
		destinationUrl = requestedPath
		deleteFile = false
	}

	if err != nil {
		p.Logger.Errorw("could not upload file", err, "location", location)
		err = errors.ErrUploadFailed(location, err)
	}
	if deleteFile {
		if err = os.Remove(localFilePath); err != nil {
			p.Logger.Errorw("could not delete file", err)
		}
	}

	return destinationUrl, size, err
}

func (p *Pipeline) onSegmentEnded(segmentPath string, endTime int64) error {

	if p.EgressType == params.EgressTypeSegmentedFile {
		// We need to dispatch to a queue to:
		// 1. Avoid concurrent access to the SegmentsInfo structure
		// 2. Ensure that playlists are uploaded in the same order they are enqueued to avoid an older playlist overwriting a newre one

		p.enqueueSegmentUpload(segmentPath, endTime)
	}

	return nil
}

func (p *Pipeline) startSegmentWorker() {
	p.endedSegments = make(chan segmentUpdate, maxPendingUploads)

	go func() {
		for segmentUpdate := range p.endedSegments {
			func() {
				defer p.segmentsWg.Done()

				p.SegmentsInfo.SegmentCount++

				destinationSegmentPath := p.GetTargetPathForFilename(segmentUpdate.localPath)
				// Ignore error. storeFile will log it.
				_, size, _ := p.storeFile(segmentUpdate.localPath, destinationSegmentPath, p.Params.GetSegmentOutputType())
				p.SegmentsInfo.Size += size

				if p.playlistWriter != nil {
					err := p.playlistWriter.EndSegment(segmentUpdate.localPath, segmentUpdate.endTime)
					if err != nil {
						p.Logger.Errorw("Failed to end segment", err, "path", segmentUpdate.localPath)
						return
					}
					destinationPlaylistPath := p.GetTargetPathForFilename(p.PlaylistFilename)
					p.SegmentsInfo.PlaylistLocation, _, _ = p.storeFile(p.PlaylistFilename, destinationPlaylistPath, p.Params.OutputType)
				}
			}()
		}
	}()
}

func (p *Pipeline) enqueueSegmentUpload(segmentPath string, endTime int64) error {
	p.segmentsWg.Add(1)
	select {
	case p.endedSegments <- segmentUpdate{localPath: segmentPath, endTime: endTime}:
		return nil
	default:
		err := errors.New("segment upload job queue is full")

		p.Logger.Errorw("Failed to upload segment", err)
		p.segmentsWg.Done()
		return errors.ErrUploadFailed(segmentPath, err)
	}
}

func (p *Pipeline) UpdateStream(req *livekit.UpdateStreamRequest) error {
	if p.EgressType != params.EgressTypeStream {
		return errors.ErrInvalidRPC
	}

	for _, url := range req.AddOutputUrls {
		if err := p.VerifyUrl(url); err != nil {
			return err
		}
	}

	now := time.Now().UnixNano()
	for _, url := range req.AddOutputUrls {
		if err := p.out.AddSink(url); err != nil {
			return err
		}

		streamInfo := &livekit.StreamInfo{Url: url}

		p.mu.Lock()
		p.startedAt[url] = now
		p.StreamInfo[url] = streamInfo
		p.mu.Unlock()

		stream := p.Info.GetStream()
		stream.Info = append(stream.Info, streamInfo)
	}

	for _, url := range req.RemoveOutputUrls {
		if err := p.out.RemoveSink(url); err != nil {
			return err
		}

		p.mu.Lock()
		startedAt := p.startedAt[url]
		p.StreamInfo[url].Duration = now - startedAt

		delete(p.startedAt, url)
		delete(p.StreamInfo, url)
		p.mu.Unlock()
	}

	return nil
}

func (p *Pipeline) SendEOS() {
	select {
	case <-p.closed:
		return
	default:
		close(p.closed)
		p.Info.Status = livekit.EgressStatus_EGRESS_ENDING
		if p.onStatusUpdate != nil {
			p.onStatusUpdate(p.Info)
		}

		p.Logger.Debugw("sending EOS to pipeline")
		p.eosTimer = time.AfterFunc(eosTimeout, func() {
			p.Logger.Errorw("pipeline frozen", nil)
			p.Info.Error = "pipeline frozen"
			p.stop()
		})

		switch s := p.in.Source.(type) {
		case *source.SDKSource:
			s.SendEOS()
		case *source.WebSource:
			p.pipeline.SendEvent(gst.NewEOSEvent())
		}
	}
}

func (p *Pipeline) updateStartTime(startedAt int64) {
	switch p.EgressType {
	case params.EgressTypeStream, params.EgressTypeWebsocket:
		p.mu.Lock()
		for _, streamInfo := range p.StreamInfo {
			p.startedAt[streamInfo.Url] = startedAt
		}
		p.mu.Unlock()

	case params.EgressTypeFile, params.EgressTypeSegmentedFile:
		p.startedAt[fileKey] = startedAt
	}

	p.Info.Status = livekit.EgressStatus_EGRESS_ACTIVE
	if p.onStatusUpdate != nil {
		p.onStatusUpdate(p.Info)
	}
}

func (p *Pipeline) updateDuration(endedAt int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.EgressType {
	case params.EgressTypeStream, params.EgressTypeWebsocket:
		for _, info := range p.StreamInfo {
			duration := p.getDuration(info.Url, endedAt)
			if duration > 0 {
				info.Duration = duration
			}
		}

	case params.EgressTypeFile:
		duration := p.getDuration(fileKey, endedAt)
		if duration > 0 {
			p.FileInfo.Duration = duration
		}

	case params.EgressTypeSegmentedFile:
		duration := p.getDuration(fileKey, endedAt)
		if duration > 0 {
			p.SegmentsInfo.Duration = duration
		}

	}
}

func (p *Pipeline) getDuration(k string, endedAt int64) int64 {
	startedAt := p.startedAt[k]
	duration := endedAt - startedAt

	if duration <= 0 {
		p.Logger.Debugw("invalid duration",
			"duration", duration, "startedAt", startedAt, "endedAt", endedAt,
		)
	}

	return duration
}

func (p *Pipeline) messageWatch(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		// EOS received - close and return
		if p.eosTimer != nil {
			p.eosTimer.Stop()
		}

		p.Logger.Debugw("EOS received, stopping pipeline")
		p.stop()
		return false

	case gst.MessageError:
		// handle error if possible, otherwise close and return
		err, handled := p.handleError(msg.ParseError())
		if !handled {
			p.Info.Error = err.Error()
			p.loop.Quit()
			return false
		}

	case gst.MessageStateChanged:
		if p.playing {
			return true
		}

		_, newState := msg.ParseStateChanged()
		if newState != gst.StatePlaying {
			return true
		}

		switch msg.Source() {
		case source.AudioAppSource, source.VideoAppSource:
			switch s := p.in.Source.(type) {
			case *source.SDKSource:
				s.Playing(msg.Source())
			}

		case pipelineSource:
			p.playing = true
			switch s := p.in.Source.(type) {
			case *source.SDKSource:
				p.updateStartTime(s.GetStartTime())
			case *source.WebSource:
				p.updateStartTime(time.Now().UnixNano())
			}
		}

	case gst.MessageElement:
		s := msg.GetStructure()
		if s != nil {
			switch s.Name() {
			case FragmentOpenedMessage:
				path, t, err := getSegmentParamsFromGstStructure(s)
				if err != nil {
					p.Logger.Errorw("Failed retrieving parameters from fragment event structure", err)
					return true
				}

				p.Logger.Debugw("Fragment Opened event", "location", path, "running time", t)

				if p.playlistWriter != nil {
					err := p.playlistWriter.StartSegment(path, t)
					if err != nil {
						p.Logger.Errorw("Failed registering new segment with playlist writer", err, "location", path, "running time", t)
						return true
					}
				}

			case FragmentClosedMessage:
				path, t, err := getSegmentParamsFromGstStructure(s)
				if err != nil {
					p.Logger.Errorw("Failed registering new segment with playlist writer", err, "location", path, "running time", t)
					return true
				}

				p.Logger.Debugw("Fragment Closed event", "location", path, "running time", t)

				err = p.onSegmentEnded(path, t)
				if err != nil {
					p.Logger.Errorw("Failed ending segment with playlist writer", err, "running time", t)
					return true
				}
			}
		}

	default:
		p.Logger.Debugw(msg.String())
	}

	return true
}

func getSegmentParamsFromGstStructure(s *gst.Structure) (path string, time int64, err error) {
	loc, err := s.GetValue(FragmentLocation)
	if err != nil {
		return "", 0, err
	}
	path, ok := loc.(string)
	if !ok {
		return "", 0, errors.New("invalid type for location")
	}

	t, err := s.GetValue(FragmentRunningTime)
	if err != nil {
		return "", 0, err
	}
	ti, ok := t.(uint64)
	if !ok {
		return "", 0, errors.New("invalid type for time")
	}

	return path, int64(ti), nil
}

func (p *Pipeline) stop() {
	p.mu.Lock()

	if p.loop == nil {
		p.mu.Unlock()
		return
	}

	_ = p.pipeline.BlockSetState(gst.StateNull)
	endedAt := time.Now().UnixNano()
	p.Logger.Debugw("pipeline stopped")

	p.loop.Quit()
	p.loop = nil
	p.mu.Unlock()

	switch p.in.Source.(type) {
	case *source.WebSource:
		p.updateDuration(endedAt)
	}
}

// handleError returns true if the error has been handled, false if the pipeline should quit
func (p *Pipeline) handleError(gErr *gst.GError) (error, bool) {
	err := errors.New(gErr.Error())

	element, reason, ok := parseDebugInfo(gErr.DebugString())
	if !ok {
		p.Logger.Errorw("failed to parse pipeline error", err, "debug", gErr.DebugString())
		return err, false
	}

	switch reason {
	case errors.GErrNoURI, errors.GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.out.RemoveSinkByName(element); err != nil {
			p.Logger.Errorw("failed to remove sink", err)
			return err, false
		}
		p.removed[element] = true
		return err, true

	case errors.GErrFailedToStart:
		// returned after an added rtmp sink failed to start
		// should be preceded by a GErrNoURI on the same sink
		handled := p.removed[element]
		if !handled {
			p.Logger.Errorw("element failed to start", err)
		}
		return err, handled

	case errors.GErrStreamingStopped:
		// returned by queue after rtmp sink could not connect
		// should be preceded by a GErrCouldNotConnect on associated sink
		handled := false
		if strings.HasPrefix(element, "queue_") {
			handled = p.removed[fmt.Sprint("sink_", element[6:])]
		}
		if !handled {
			p.Logger.Errorw("streaming sink stopped", err)
		}
		return err, handled

	default:
		// input failure or file write failure. Fatal
		p.Logger.Errorw("pipeline error", err, "debug", gErr.DebugString())
		return err, false
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
	if strings.HasPrefix(reason, errors.GErrCouldNotConnect) {
		reason = errors.GErrCouldNotConnect
	}
	ok = true
	return
}

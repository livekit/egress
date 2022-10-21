package pipeline

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/input"
	"github.com/livekit/egress/pkg/pipeline/input/sdk"
	"github.com/livekit/egress/pkg/pipeline/input/web"
	"github.com/livekit/egress/pkg/pipeline/output"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/tracer"
)

const (
	pipelineSource    = "pipeline"
	eosTimeout        = time.Second * 30
	maxPendingUploads = 100

	fragmentOpenedMessage = "splitmuxsink-fragment-opened"
	fragmentClosedMessage = "splitmuxsink-fragment-closed"
	fragmentLocation      = "location"
	fragmentRunningTime   = "running-time"

	elementGstRtmp2Sink = "GstRtmp2Sink"
	elementGstAppSrc    = "GstAppSrc"
)

type Pipeline struct {
	*params.Params

	// gstreamer
	pipeline *gst.Pipeline
	in       input.Input
	out      *output.OutputBin
	loop     *glib.MainLoop

	// internal
	mu         sync.Mutex
	playing    bool
	limitTimer *time.Timer
	closed     chan struct{}
	closeOnce  sync.Once
	eosTimer   *time.Timer

	// segments
	playlistWriter *sink.PlaylistWriter
	segmentsWg     sync.WaitGroup
	endedSegments  chan segmentUpdate

	// callbacks
	onStatusUpdate func(context.Context, *livekit.EgressInfo)
}

type segmentUpdate struct {
	endTime   int64
	localPath string
}

func New(ctx context.Context, conf *config.Config, p *params.Params) (*Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	// initialize gst
	go func() {
		_, span := tracer.Start(ctx, "gst.Init")
		defer span.End()
		gst.Init(nil)
		close(p.GstReady)
	}()

	// create input bin
	in, err := input.New(ctx, conf, p)
	if err != nil {
		return nil, err
	}

	// create output bin
	out, err := output.New(ctx, p)
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
		if err = in.Bin().Link(out.Element()); err != nil {
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
		closed:         make(chan struct{}),
	}, nil
}

func (p *Pipeline) GetInfo() *livekit.EgressInfo {
	return p.Info
}

func (p *Pipeline) OnStatusUpdate(f func(context.Context, *livekit.EgressInfo)) {
	p.onStatusUpdate = f
}

func (p *Pipeline) Run(ctx context.Context) *livekit.EgressInfo {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	p.Info.StartedAt = time.Now().UnixNano()
	defer func() {
		p.Info.EndedAt = time.Now().UnixNano()

		// update status
		if p.Info.Error != "" {
			p.Info.Status = livekit.EgressStatus_EGRESS_FAILED
		}

		switch p.Info.Status {
		case livekit.EgressStatus_EGRESS_STARTING:
		case livekit.EgressStatus_EGRESS_ACTIVE:
		case livekit.EgressStatus_EGRESS_ENDING:
			p.Info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		}

		p.cleanup()
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
		p.SendEOS(ctx)
	}()

	// session limit timer
	p.startSessionLimitTimer(ctx)

	// add watch
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(p.messageWatch)

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		span.RecordError(err)
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
	switch s := p.in.(type) {
	case *sdk.SDKInput:
		p.updateDuration(s.GetEndTime())
	}

	// skip upload if there was an error
	if p.Info.Error != "" {
		return p.Info
	}

	// upload file
	switch p.EgressType {
	case params.EgressTypeFile:
		var err error
		p.FileInfo.Location, p.FileInfo.Size, err = p.storeFile(ctx, p.LocalFilepath, p.StorageFilepath, p.OutputType)
		if err != nil {
			p.Info.Error = err.Error()
		}

		manifestLocalPath := fmt.Sprintf("%s.json", p.LocalFilepath)
		manifestStoragePath := fmt.Sprintf("%s.json", p.StorageFilepath)
		if err := p.storeManifest(ctx, manifestLocalPath, manifestStoragePath); err != nil {
			p.Logger.Errorw("could not store manifest", err)
		}

	case params.EgressTypeSegmentedFile:
		// wait for all pending upload jobs to finish
		if p.endedSegments != nil {
			p.segmentsWg.Wait()
		}

		if p.playlistWriter != nil {
			if err := p.playlistWriter.EOS(); err != nil {
				p.Logger.Errorw("failed to send EOS to playlist writer", err)
			}

			// upload the finalized playlist
			playlistStoragePath := p.GetStorageFilepath(p.PlaylistFilename)
			p.SegmentsInfo.PlaylistLocation, _, _ = p.storeFile(ctx, p.PlaylistFilename, playlistStoragePath, p.OutputType)

			manifestLocalPath := fmt.Sprintf("%s.json", p.PlaylistFilename)
			manifestStoragePath := fmt.Sprintf("%s.json", playlistStoragePath)
			if err := p.storeManifest(ctx, manifestLocalPath, manifestStoragePath); err != nil {
				p.Logger.Errorw("could not store manifest", err)
			}
		}
	}

	return p.Info
}

func (p *Pipeline) messageWatch(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		// EOS received - close and return
		if p.eosTimer != nil {
			p.eosTimer.Stop()
		}

		p.Logger.Debugw("EOS received, stopping pipeline")
		p.closeOnce.Do(func() {
			p.close(context.Background())
		})

		p.stop()
		return false

	case gst.MessageError:
		// handle error if possible, otherwise close and return
		err, handled := p.handleError(msg.ParseError())
		if !handled {
			p.Info.Error = err.Error()
			p.stop()
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
		case sdk.AudioAppSource, sdk.VideoAppSource:
			switch s := p.in.(type) {
			case *sdk.SDKInput:
				s.Playing(msg.Source())
			}

		case pipelineSource:
			p.playing = true
			switch s := p.in.(type) {
			case *sdk.SDKInput:
				p.updateStartTime(s.GetStartTime())
			case *web.WebInput:
				p.updateStartTime(time.Now().UnixNano())
			}
		}

	case gst.MessageElement:
		s := msg.GetStructure()
		if s != nil {
			switch s.Name() {
			case fragmentOpenedMessage:
				filepath, t, err := getSegmentParamsFromGstStructure(s)
				if err != nil {
					p.Logger.Errorw("failed to retrieve segment parameters from event", err)
					return true
				}

				p.Logger.Debugw("fragment opened", "location", filepath, "running time", t)

				if p.playlistWriter != nil {
					if err = p.playlistWriter.StartSegment(filepath, t); err != nil {
						p.Logger.Errorw("failed to register new segment with playlist writer", err, "location", filepath, "running time", t)
						return true
					}
				}

			case fragmentClosedMessage:
				filepath, t, err := getSegmentParamsFromGstStructure(s)
				if err != nil {
					p.Logger.Errorw("failed to retrieve segment parameters from event", err, "location", filepath, "running time", t)
					return true
				}

				p.Logger.Debugw("fragment closed", "location", filepath, "running time", t)

				// We need to dispatch to a queue to:
				// 1. Avoid concurrent access to the SegmentsInfo structure
				// 2. Ensure that playlists are uploaded in the same order they are enqueued to avoid an older playlist overwriting a newer one
				if err = p.enqueueSegmentUpload(filepath, t); err != nil {
					p.Logger.Errorw("failed to end segment with playlist writer", err, "running time", t)
					return true
				}
			}
		}

	default:
		p.Logger.Debugw(msg.String())
	}

	return true
}

func (p *Pipeline) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) error {
	ctx, span := tracer.Start(ctx, "Pipeline.UpdateStream")
	defer span.End()

	if p.EgressType != params.EgressTypeStream {
		return errors.ErrInvalidRPC
	}

	for _, url := range req.AddOutputUrls {
		if err := p.VerifyUrl(url); err != nil {
			return err
		}
	}

	errs := make([]string, 0)

	now := time.Now().UnixNano()
	for _, url := range req.AddOutputUrls {
		if err := p.out.AddSink(url); err != nil {
			errs = append(errs, err.Error())
			continue
		}

		p.mu.Lock()
		streamInfo := &livekit.StreamInfo{
			Url:       url,
			StartedAt: now,
			Status:    livekit.StreamInfo_ACTIVE,
		}
		p.StreamInfo[url] = streamInfo
		p.Info.GetStream().Info = append(p.Info.GetStream().Info, streamInfo)
		p.mu.Unlock()
	}

	for _, url := range req.RemoveOutputUrls {
		if err := p.removeSink(url, livekit.StreamInfo_FINISHED); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

func (p *Pipeline) removeSink(url string, status livekit.StreamInfo_Status) error {
	now := time.Now().UnixNano()

	p.mu.Lock()
	streamInfo := p.StreamInfo[url]
	streamInfo.Status = status
	streamInfo.EndedAt = now
	if streamInfo.StartedAt == 0 {
		streamInfo.StartedAt = now
	} else {
		streamInfo.Duration = now - streamInfo.StartedAt
	}
	delete(p.StreamInfo, url)
	done := len(p.StreamInfo) == 0
	p.mu.Unlock()

	p.Logger.Debugw("removing stream sink", "url", url, "status", status, "duration", streamInfo.Duration)

	switch status {
	case livekit.StreamInfo_FINISHED:
		if done {
			p.SendEOS(context.Background())
			return nil
		}
	case livekit.StreamInfo_FAILED:
		if done {
			return errors.New("could not connect")
		} else if p.onStatusUpdate != nil {
			p.onStatusUpdate(context.Background(), p.Info)
		}
	}

	return p.out.RemoveSink(url)
}

func (p *Pipeline) SendEOS(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	p.closeOnce.Do(func() {
		p.close(ctx)

		go func() {
			p.Logger.Debugw("sending EOS to pipeline")
			p.eosTimer = time.AfterFunc(eosTimeout, func() {
				p.Logger.Errorw("pipeline frozen", nil)
				p.Info.Error = "pipeline frozen"
				p.stop()
			})

			switch s := p.in.(type) {
			case *sdk.SDKInput:
				s.SendEOS()
			}

			p.pipeline.SendEvent(gst.NewEOSEvent())
		}()
	})
}

func (p *Pipeline) close(ctx context.Context) {
	close(p.closed)
	if p.limitTimer != nil {
		p.limitTimer.Stop()
	}

	if p.Info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
		p.Info.Status = livekit.EgressStatus_EGRESS_ENDING
		if p.onStatusUpdate != nil {
			p.onStatusUpdate(ctx, p.Info)
		}
	}
}

func (p *Pipeline) startSessionLimitTimer(ctx context.Context) {
	if timeout := p.GetSessionTimeout(); timeout > 0 {
		p.limitTimer = time.AfterFunc(timeout, func() {
			p.SendEOS(ctx)
			p.Info.Status = livekit.EgressStatus_EGRESS_LIMIT_REACHED
		})
	}
}

func (p *Pipeline) updateStartTime(startedAt int64) {
	switch p.EgressType {
	case params.EgressTypeStream, params.EgressTypeWebsocket:
		p.mu.Lock()
		for _, streamInfo := range p.StreamInfo {
			streamInfo.Status = livekit.StreamInfo_ACTIVE
			streamInfo.StartedAt = startedAt
		}
		p.mu.Unlock()

	case params.EgressTypeFile:
		p.FileInfo.StartedAt = startedAt

	case params.EgressTypeSegmentedFile:
		p.SegmentsInfo.StartedAt = startedAt
	}

	p.Info.Status = livekit.EgressStatus_EGRESS_ACTIVE
	if p.onStatusUpdate != nil {
		p.onStatusUpdate(context.Background(), p.Info)
	}
}

func (p *Pipeline) startSegmentWorker() {
	p.endedSegments = make(chan segmentUpdate, maxPendingUploads)

	go func() {
		for update := range p.endedSegments {
			func() {
				defer p.segmentsWg.Done()

				p.SegmentsInfo.SegmentCount++

				segmentStoragePath := p.GetStorageFilepath(update.localPath)
				// Ignore error. storeFile will log it.
				_, size, _ := p.storeFile(context.Background(), update.localPath, segmentStoragePath, p.GetSegmentOutputType())
				p.SegmentsInfo.Size += size

				if p.playlistWriter != nil {
					err := p.playlistWriter.EndSegment(update.localPath, update.endTime)
					if err != nil {
						p.Logger.Errorw("failed to end segment", err, "path", update.localPath)
						return
					}
					playlistStoragePath := p.GetStorageFilepath(p.PlaylistFilename)
					p.SegmentsInfo.PlaylistLocation, _, _ = p.storeFile(context.Background(), p.PlaylistFilename, playlistStoragePath, p.OutputType)
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
		p.Logger.Errorw("failed to upload segment", err)
		p.segmentsWg.Done()
		return errors.ErrUploadFailed(segmentPath, err)
	}
}

func (p *Pipeline) updateDuration(endedAt int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.EgressType {
	case params.EgressTypeStream, params.EgressTypeWebsocket:
		for _, info := range p.StreamInfo {
			info.Status = livekit.StreamInfo_FINISHED
			if info.StartedAt == 0 {
				info.StartedAt = endedAt
			}
			info.EndedAt = endedAt
			info.Duration = endedAt - info.StartedAt
		}

	case params.EgressTypeFile:
		if p.FileInfo.StartedAt == 0 {
			p.FileInfo.StartedAt = endedAt
		}
		p.FileInfo.EndedAt = endedAt
		p.FileInfo.Duration = endedAt - p.FileInfo.StartedAt

	case params.EgressTypeSegmentedFile:
		if p.SegmentsInfo.StartedAt == 0 {
			p.SegmentsInfo.StartedAt = endedAt
		}
		p.SegmentsInfo.EndedAt = endedAt
		p.SegmentsInfo.Duration = endedAt - p.SegmentsInfo.StartedAt
	}
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

	switch p.in.(type) {
	case *web.WebInput:
		p.updateDuration(endedAt)
	}
}

func (p *Pipeline) storeFile(ctx context.Context, localFilepath, storageFilepath string, mime params.OutputType) (destinationUrl string, size int64, err error) {
	ctx, span := tracer.Start(ctx, "Pipeline.storeFile")
	defer span.End()

	fileInfo, err := os.Stat(localFilepath)
	if err == nil {
		size = fileInfo.Size()
	} else {
		p.Logger.Errorw("could not read file size", err)
	}

	var location string
	switch u := p.UploadConfig.(type) {
	case *livekit.S3Upload:
		location = "S3"
		p.Logger.Debugw("uploading to s3")
		destinationUrl, err = sink.UploadS3(u, localFilepath, storageFilepath, mime)

	case *livekit.GCPUpload:
		location = "GCP"
		p.Logger.Debugw("uploading to gcp")
		destinationUrl, err = sink.UploadGCP(u, localFilepath, storageFilepath, mime)

	case *livekit.AzureBlobUpload:
		location = "Azure"
		p.Logger.Debugw("uploading to azure")
		destinationUrl, err = sink.UploadAzure(u, localFilepath, storageFilepath, mime)

	default:
		destinationUrl = storageFilepath
	}

	if err != nil {
		p.Logger.Errorw("could not upload file", err, "location", location)
		err = errors.ErrUploadFailed(location, err)
		span.RecordError(err)
	}

	return destinationUrl, size, err
}

func (p *Pipeline) storeManifest(ctx context.Context, localFilepath, storageFilepath string) error {
	manifest, err := os.Create(localFilepath)
	if err != nil {
		return err
	}

	b, err := p.GetManifest()
	if err != nil {
		return err
	}

	_, err = manifest.Write(b)
	if err != nil {
		return err
	}

	_, _, err = p.storeFile(ctx, localFilepath, storageFilepath, "application/json")
	return err
}

func (p *Pipeline) cleanup() {
	// clean up temp dir
	if p.UploadConfig != nil {
		switch p.EgressType {
		case params.EgressTypeFile:
			dir, _ := path.Split(p.LocalFilepath)
			if dir != "" {
				p.Logger.Debugw("removing temporary directory", "path", dir)
				if err := os.RemoveAll(dir); err != nil {
					p.Logger.Errorw("could not delete temp dir", err)
				}
			}

		case params.EgressTypeSegmentedFile:
			dir, _ := path.Split(p.PlaylistFilename)
			if dir != "" {
				p.Logger.Debugw("removing temporary directory", "path", dir)
				if err := os.RemoveAll(dir); err != nil {
					p.Logger.Errorw("could not delete temp dir", err)
				}
			}
		}
	}
}

func getSegmentParamsFromGstStructure(s *gst.Structure) (filepath string, time int64, err error) {
	loc, err := s.GetValue(fragmentLocation)
	if err != nil {
		return "", 0, err
	}
	filepath, ok := loc.(string)
	if !ok {
		return "", 0, errors.New("invalid type for location")
	}

	t, err := s.GetValue(fragmentRunningTime)
	if err != nil {
		return "", 0, err
	}
	ti, ok := t.(uint64)
	if !ok {
		return "", 0, errors.New("invalid type for time")
	}

	return filepath, int64(ti), nil
}

// handleError returns true if the error has been handled, false if the pipeline should quit
func (p *Pipeline) handleError(gErr *gst.GError) (error, bool) {
	element, name, message := parseDebugInfo(gErr)
	err := errors.New(gErr.Error())

	switch {
	case element == elementGstRtmp2Sink:
		// bad URI or could not connect. Remove rtmp output
		url, e := p.out.GetUrlFromName(name)
		if e != nil {
			p.Logger.Warnw("rtmp output not found", e, "url", url)
			return e, false
		}
		if e = p.removeSink(url, livekit.StreamInfo_FAILED); e != nil {
			return err, false
		}
		return err, true

	case element == elementGstAppSrc:
		if message == "streaming stopped, reason not-negotiated (-4)" {
			// send eos to app src
			p.Logger.Debugw("streaming stopped", "name", name)
			p.in.(*sdk.SDKInput).SendAppSrcEOS(name)
			return err, true
		}
	}

	// input failure or file write failure. Fatal
	p.Logger.Errorw("pipeline error", err,
		"element", element,
		"message", message,
	)

	return err, false
}

// Debug info comes in the following format:
// file.c(line): method_name (): /GstPipeline:pipeline/GstBin:bin_name/GstElement:element_name:\nError message
var regExp = regexp.MustCompile("(?s)(.*?)GstPipeline:pipeline\\/GstBin:(.*?)\\/(.*?):([^:]*)(:\n)?(.*)")

func parseDebugInfo(gErr *gst.GError) (element, name, message string) {
	match := regExp.FindStringSubmatch(gErr.DebugString())

	element = match[3]
	name = match[4]
	message = match[6]
	return
}

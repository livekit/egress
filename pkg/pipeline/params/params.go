package params

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path"
	"strings"
	"time"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
)

type Params struct {
	conf     *config.Config
	Logger   logger.Logger
	Info     *livekit.EgressInfo
	GstReady chan struct{}

	SourceParams
	AudioParams
	VideoParams

	EgressType
	OutputType

	MutedChan chan bool
	StreamParams
	FileParams
	SegmentedFileParams

	FileUpload interface{}
}

type SourceParams struct {
	// source
	RoomName     string
	Token        string
	LKUrl        string
	TemplateBase string
	IsWebSource  bool

	// web source
	Display        string
	Layout         string
	CustomBase     string
	CustomInputURL string

	// sdk source
	TrackID      string
	AudioTrackID string
	VideoTrackID string
}

type AudioParams struct {
	AudioEnabled   bool
	AudioCodec     MimeType
	AudioBitrate   int32
	AudioFrequency int32
}

type VideoParams struct {
	VideoEnabled bool
	VideoCodec   MimeType
	VideoProfile Profile
	Width        int32
	Height       int32
	Depth        int32
	Framerate    int32
	VideoBitrate int32
}

type StreamParams struct {
	WebsocketUrl string
	StreamUrls   []string
	StreamInfo   map[string]*livekit.StreamInfo
}

type FileParams struct {
	FileInfo *livekit.FileInfo
	Filename string
	Filepath string
}

type SegmentedFileParams struct {
	SegmentsInfo     *livekit.SegmentsInfo
	LocalFilePrefix  string
	TargetDirectory  string
	PlaylistFilename string
	SegmentDuration  int
}

func ValidateRequest(ctx context.Context, conf *config.Config, request *livekit.StartEgressRequest) (*livekit.EgressInfo, error) {
	ctx, span := tracer.Start(ctx, "Params.ValidateRequest")
	defer span.End()

	p, err := getPipelineParams(conf, request)
	return p.Info, err
}

func GetPipelineParams(ctx context.Context, conf *config.Config, request *livekit.StartEgressRequest) (*Params, error) {
	ctx, span := tracer.Start(ctx, "Params.GetPipelineParams")
	defer span.End()

	return getPipelineParams(conf, request)
}

// getPipelineParams must always return params with valid info, even on error
func getPipelineParams(conf *config.Config, request *livekit.StartEgressRequest) (p *Params, err error) {
	// start with defaults
	p = &Params{
		Logger: logger.Logger(logger.GetLogger().WithValues("egressID", request.EgressId)),
		Info: &livekit.EgressInfo{
			EgressId: request.EgressId,
			RoomId:   request.RoomId,
			Status:   livekit.EgressStatus_EGRESS_STARTING,
		},
		GstReady: make(chan struct{}),
		AudioParams: AudioParams{
			AudioBitrate:   128,
			AudioFrequency: 44100,
		},
		VideoParams: VideoParams{
			VideoProfile: ProfileMain,
			Width:        1920,
			Height:       1080,
			Depth:        24,
			Framerate:    30,
			VideoBitrate: 4500,
		},
		conf: conf,
	}

	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		p.Info.Request = &livekit.EgressInfo_RoomComposite{RoomComposite: req.RoomComposite}

		// input params
		p.IsWebSource = true
		p.RoomName = req.RoomComposite.RoomName
		if p.RoomName == "" {
			err = errors.ErrInvalidInput("RoomName")
			return
		}

		p.Layout = req.RoomComposite.Layout
		p.Display = fmt.Sprintf(":%d", 10+rand.Intn(2147483637))
		if req.RoomComposite.CustomBaseUrl != "" {
			p.TemplateBase = req.RoomComposite.CustomBaseUrl
		} else {
			p.TemplateBase = conf.TemplateBase
		}
		p.AudioEnabled = !req.RoomComposite.VideoOnly
		p.VideoEnabled = !req.RoomComposite.AudioOnly

		// encoding options
		switch opts := req.RoomComposite.Options.(type) {
		case *livekit.RoomCompositeEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.RoomCompositeEgressRequest_Advanced:
			p.applyAdvanced(opts.Advanced)
		}

		// output params
		switch o := req.RoomComposite.Output.(type) {
		case *livekit.RoomCompositeEgressRequest_File:
			p.updateOutputType(o.File.FileType)
			if err = p.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return
			}

		case *livekit.RoomCompositeEgressRequest_Stream:
			if err = p.updateStreamParams(OutputTypeRTMP, o.Stream.Urls); err != nil {
				return
			}

		case *livekit.RoomCompositeEgressRequest_Segments:
			p.updateOutputType(o.Segments.Protocol)
			if err = p.updateSegmentsParams(o.Segments.FilenamePrefix, o.Segments.PlaylistName, o.Segments.SegmentDuration, o.Segments.Output); err != nil {
				return
			}

		default:
			err = errors.ErrInvalidInput("output")
			return
		}

	case *livekit.StartEgressRequest_TrackComposite:
		p.Info.Request = &livekit.EgressInfo_TrackComposite{TrackComposite: req.TrackComposite}

		// encoding options
		switch opts := req.TrackComposite.Options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.TrackCompositeEgressRequest_Advanced:
			p.applyAdvanced(opts.Advanced)
		}

		// input params
		p.RoomName = req.TrackComposite.RoomName
		if p.RoomName == "" {
			err = errors.ErrInvalidInput("RoomName")
			return
		}

		p.AudioTrackID = req.TrackComposite.AudioTrackId
		p.VideoTrackID = req.TrackComposite.VideoTrackId
		p.AudioEnabled = p.AudioTrackID != ""
		p.VideoEnabled = p.VideoTrackID != ""
		if !p.AudioEnabled && !p.VideoEnabled {
			err = errors.ErrInvalidInput("TrackIDs")
			return
		}

		// output params
		switch o := req.TrackComposite.Output.(type) {
		case *livekit.TrackCompositeEgressRequest_File:
			if o.File.FileType != livekit.EncodedFileType_DEFAULT_FILETYPE {
				p.updateOutputType(o.File.FileType)
			}
			if err = p.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return
			}

		case *livekit.TrackCompositeEgressRequest_Stream:
			if err = p.updateStreamParams(OutputTypeRTMP, o.Stream.Urls); err != nil {
				return
			}

		case *livekit.TrackCompositeEgressRequest_Segments:
			p.updateOutputType(o.Segments.Protocol)
			if err = p.updateSegmentsParams(o.Segments.FilenamePrefix, o.Segments.PlaylistName, o.Segments.SegmentDuration, o.Segments.Output); err != nil {
				return
			}

		default:
			err = errors.ErrInvalidInput("output")
			return
		}

	case *livekit.StartEgressRequest_Track:
		p.Info.Request = &livekit.EgressInfo_Track{Track: req.Track}

		// input params
		p.RoomName = req.Track.RoomName
		if p.RoomName == "" {
			err = errors.ErrInvalidInput("RoomName")
			return
		}

		p.TrackID = req.Track.TrackId
		if p.TrackID == "" {
			err = errors.ErrInvalidInput("TrackID")
			return
		}

		// output params
		switch o := req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_File:
			if err = p.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return
			}
		case *livekit.TrackEgressRequest_WebsocketUrl:
			if err = p.updateStreamParams(OutputTypeRaw, []string{o.WebsocketUrl}); err != nil {
				return
			}

		default:
			err = errors.ErrInvalidInput("output")
			return
		}

	default:
		err = errors.ErrInvalidInput("request")
		return
	}

	if err = p.updateConnectionInfo(request); err != nil {
		return
	}

	if p.OutputType != "" {
		if err = p.updateCodecsFromOutputType(); err != nil {
			return
		}
	}

	return
}

func (p *Params) applyPreset(preset livekit.EncodingOptionsPreset) {
	switch preset {
	case livekit.EncodingOptionsPreset_H264_720P_30:
		p.Width = 1280
		p.Height = 720
		p.VideoBitrate = 3000

	case livekit.EncodingOptionsPreset_H264_720P_60:
		p.Width = 1280
		p.Height = 720
		p.Framerate = 60

	case livekit.EncodingOptionsPreset_H264_1080P_30:
		// default

	case livekit.EncodingOptionsPreset_H264_1080P_60:
		p.Framerate = 60
		p.VideoBitrate = 6000
	}
}

func (p *Params) applyAdvanced(advanced *livekit.EncodingOptions) {
	// audio
	switch advanced.AudioCodec {
	case livekit.AudioCodec_OPUS:
		p.AudioCodec = MimeTypeOpus
	case livekit.AudioCodec_AAC:
		p.AudioCodec = MimeTypeAAC
	}

	if advanced.AudioBitrate != 0 {
		p.AudioBitrate = advanced.AudioBitrate
	}
	if advanced.AudioFrequency != 0 {
		p.AudioFrequency = advanced.AudioFrequency
	}

	// video
	switch advanced.VideoCodec {
	case livekit.VideoCodec_H264_BASELINE:
		p.VideoCodec = MimeTypeH264
		p.VideoProfile = ProfileBaseline

	case livekit.VideoCodec_H264_MAIN:
		p.VideoCodec = MimeTypeH264

	case livekit.VideoCodec_H264_HIGH:
		p.VideoCodec = MimeTypeH264
		p.VideoProfile = ProfileHigh
	}

	if advanced.Width != 0 {
		p.Width = advanced.Width
	}
	if advanced.Height != 0 {
		p.Height = advanced.Height
	}
	if advanced.Depth != 0 {
		p.Depth = advanced.Depth
	}
	if advanced.Framerate != 0 {
		p.Framerate = advanced.Framerate
	}
	if advanced.VideoBitrate != 0 {
		p.VideoBitrate = advanced.VideoBitrate
	}
}

func (p *Params) updateOutputType(fileType interface{}) {

	switch f := fileType.(type) {
	case livekit.EncodedFileType:
		switch f {
		case livekit.EncodedFileType_DEFAULT_FILETYPE:
			if !p.VideoEnabled && p.AudioCodec != MimeTypeAAC {
				p.OutputType = OutputTypeOGG
			} else {
				p.OutputType = OutputTypeMP4
			}
		case livekit.EncodedFileType_MP4:
			p.OutputType = OutputTypeMP4
		case livekit.EncodedFileType_OGG:
			p.OutputType = OutputTypeOGG
		}
	case livekit.SegmentedFileProtocol:
		switch f {
		case livekit.SegmentedFileProtocol_DEFAULT_SEGMENTED_FILE_PROTOCOL, livekit.SegmentedFileProtocol_HLS_PROTOCOL:
			p.OutputType = OutputTypeHLS
		}
	}
}

func (p *Params) updateFileParams(filepath string, output interface{}) error {
	p.EgressType = EgressTypeFile
	p.Filepath = filepath
	p.FileInfo = &livekit.FileInfo{}
	p.Info.Result = &livekit.EgressInfo_File{File: p.FileInfo}

	// output location
	switch o := output.(type) {
	case *livekit.EncodedFileOutput_S3:
		p.FileUpload = o.S3
	case *livekit.EncodedFileOutput_Azure:
		p.FileUpload = o.Azure
	case *livekit.EncodedFileOutput_Gcp:
		p.FileUpload = o.Gcp
	default:
		p.FileUpload = p.conf.FileUpload
	}

	// filename
	if p.OutputType != "" {
		err := p.updateFilename(p.RoomName)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *Params) updateStreamParams(outputType OutputType, urls []string) error {
	p.OutputType = outputType

	switch p.OutputType {
	case OutputTypeRTMP:
		p.EgressType = EgressTypeStream
		p.AudioCodec = MimeTypeAAC
		p.VideoCodec = MimeTypeH264
		p.StreamUrls = urls

	case OutputTypeRaw:
		p.EgressType = EgressTypeWebsocket
		p.AudioCodec = MimeTypeRaw
		p.WebsocketUrl = urls[0]
		p.MutedChan = make(chan bool, 1)
	}

	p.StreamInfo = make(map[string]*livekit.StreamInfo)
	var streamInfoList []*livekit.StreamInfo
	for _, url := range urls {
		if err := p.VerifyUrl(url); err != nil {
			return err
		}

		info := &livekit.StreamInfo{Url: url}
		p.StreamInfo[url] = info
		streamInfoList = append(streamInfoList, info)
	}

	p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
	return nil
}

func (p *Params) updateSegmentsParams(fileprefix string, playlistFilename string, segmentDuration uint32, output interface{}) error {
	p.EgressType = EgressTypeSegmentedFile
	p.LocalFilePrefix = fileprefix
	p.PlaylistFilename = playlistFilename
	p.SegmentDuration = int(segmentDuration)
	if p.SegmentDuration == 0 {
		p.SegmentDuration = 6
	}
	p.SegmentsInfo = &livekit.SegmentsInfo{}
	p.Info.Result = &livekit.EgressInfo_Segments{Segments: p.SegmentsInfo}

	// output location
	switch o := output.(type) {
	case *livekit.EncodedFileOutput_S3:
		p.FileUpload = o.S3
	case *livekit.EncodedFileOutput_Azure:
		p.FileUpload = o.Azure
	case *livekit.EncodedFileOutput_Gcp:
		p.FileUpload = o.Gcp
	default:
		p.FileUpload = p.conf.FileUpload
	}

	// filename
	err := p.updatePrefixAndPlaylist(p.RoomName)
	if err != nil {
		return err
	}

	return nil
}

func (p *Params) updateConnectionInfo(request *livekit.StartEgressRequest) error {
	// token
	if request.Token != "" {
		p.Token = request.Token
	} else if p.conf.ApiKey != "" && p.conf.ApiSecret != "" {
		token, err := egress.BuildEgressToken(p.Info.EgressId, p.conf.ApiKey, p.conf.ApiSecret, p.RoomName)
		if err != nil {
			return err
		}
		p.Token = token
	} else {
		return errors.ErrInvalidInput("token or api key/secret")
	}

	// url
	if request.WsUrl != "" {
		p.LKUrl = request.WsUrl
	} else if p.conf.WsUrl != "" {
		p.LKUrl = p.conf.WsUrl
	} else {
		return errors.ErrInvalidInput("ws_url")
	}

	return nil
}

// used for web input source
func (p *Params) updateCodecsFromOutputType() error {
	// check audio codec
	if p.AudioEnabled {
		if p.AudioCodec == "" {
			p.AudioCodec = DefaultAudioCodecs[p.OutputType]
		} else if !codecCompatibility[p.OutputType][p.AudioCodec] {
			return errors.ErrIncompatible(p.OutputType, p.AudioCodec)
		}
	}

	// check video codec
	if p.VideoEnabled {
		if p.VideoCodec == "" {
			p.VideoCodec = DefaultVideoCodecs[p.OutputType]
		} else if !codecCompatibility[p.OutputType][p.VideoCodec] {
			return errors.ErrIncompatible(p.OutputType, p.VideoCodec)
		}
	}

	return nil
}

// used for sdk input source
func (p *Params) UpdateOutputTypeFromCodecs(fileIdentifier string) error {
	if p.OutputType == "" {
		if !p.VideoEnabled {
			// audio input is always opus
			p.OutputType = OutputTypeOGG
		} else {
			p.OutputType = OutputTypeMP4
		}
	}

	// check audio codec
	if p.AudioEnabled && !codecCompatibility[p.OutputType][p.AudioCodec] {
		return errors.ErrIncompatible(p.OutputType, p.AudioCodec)
	}

	// check video codec
	if p.VideoEnabled && !codecCompatibility[p.OutputType][p.VideoCodec] {
		return errors.ErrIncompatible(p.OutputType, p.VideoCodec)
	}

	return p.updateFilename(fileIdentifier)
}

func (p *Params) updateFilename(identifier string) error {
	ext := FileExtensions[p.OutputType]

	if p.Filepath == "" || strings.HasSuffix(p.Filepath, "/") {
		p.Filepath = fmt.Sprintf("%s%s-%v%v", p.Filepath, identifier, time.Now().String(), ext)
	} else if !strings.HasSuffix(p.Filepath, string(ext)) {
		p.Filepath = p.Filepath + string(ext)
	}

	// get filename from path
	dir, filename := path.Split(p.Filepath)
	if p.FileUpload == nil {
		if dir != "" {
			// create local directory
			if err := os.MkdirAll(dir, os.ModeDir); err != nil {
				return err
			}
		}
		p.Filename = p.Filepath
	} else {
		// Prepend the configuration base directory and the egress Id
		outDir := path.Join(p.conf.LocalOutputDirectory, p.Info.EgressId)

		// create temporary directory
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		if err := os.MkdirAll(outDir, os.ModePerm&0o700); err != nil {
			return err
		}
		p.Filename = path.Join(outDir, filename)
	}

	p.FileInfo.Filename = p.Filename
	p.Logger.Debugw("writing to file", "filename", p.Filename)
	return nil
}

func (p *Params) updatePrefixAndPlaylist(identifier string) error {
	ext := FileExtensions[p.OutputType]

	if p.LocalFilePrefix == "" || strings.HasSuffix(p.LocalFilePrefix, "/") {
		p.LocalFilePrefix = fmt.Sprintf("%s%s-%v", p.LocalFilePrefix, identifier, time.Now().String())
	}

	// Playlist path is relative to file prefix. Only keep actual filename if a full path is given
	_, p.PlaylistFilename = path.Split(p.PlaylistFilename)
	if p.PlaylistFilename == "" {
		p.PlaylistFilename = fmt.Sprintf("playlist%s", ext)
	}

	var filePrefix string
	p.TargetDirectory, filePrefix = path.Split(p.LocalFilePrefix)
	if p.FileUpload == nil {
		if p.TargetDirectory != "" {
			if err := os.MkdirAll(p.TargetDirectory, os.ModeDir); err != nil {
				return err
			}
		}
		p.PlaylistFilename = path.Join(p.TargetDirectory, p.PlaylistFilename)
	} else {
		// Prepend the configuration base directory and the egress Id
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		outDir := path.Join(p.conf.LocalOutputDirectory, p.Info.EgressId)
		if err := os.MkdirAll(outDir, os.ModePerm&0o700); err != nil {
			return err
		}

		p.PlaylistFilename = path.Join(outDir, p.PlaylistFilename)
		p.LocalFilePrefix = path.Join(outDir, filePrefix)
	}
	p.Logger.Debugw("writing to path", "prefix", p.LocalFilePrefix)

	p.SegmentsInfo.PlaylistName = p.GetTargetPathForFilename(p.PlaylistFilename)
	return nil
}

func (p *Params) VerifyUrl(url string) error {
	var protocol, prefix string

	switch p.OutputType {
	case OutputTypeRTMP:
		protocol = "rtmp"
		prefix = "rtmp"
	case OutputTypeRaw:
		protocol = "websocket"
		prefix = "ws"
	}

	if !strings.HasPrefix(url, prefix+"://") && !strings.HasPrefix(url, prefix+"s://") {
		return errors.ErrInvalidUrl(url, protocol)
	}

	return nil
}

func (p *Params) GetSegmentOutputType() OutputType {
	switch p.OutputType {
	case OutputTypeHLS:
		// HLS is always mpeg ts for now. We may implement fmp4 in the future
		return OutputTypeTS
	default:
		return p.OutputType
	}
}

func (p *SegmentedFileParams) GetTargetPathForFilename(filename string) string {
	// Remove any path prepended to the filename
	_, filename = path.Split(filename)

	return path.Join(p.TargetDirectory, filename)
}

package params

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
)

type Params struct {
	Logger logger.Logger
	Info   *livekit.EgressInfo

	SourceParams
	AudioParams
	VideoParams

	SkipPipeline bool
	IsStream     bool
	OutputType
	StreamParams
	FileParams
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
	StreamUrls []string
	StreamInfo map[string]*livekit.StreamInfo
}

type FileParams struct {
	FileInfo   *livekit.FileInfo
	Filename   string
	Filepath   string
	FileUpload interface{}
}

func GetPipelineParams(conf *config.Config, request *livekit.StartEgressRequest) (*Params, error) {
	// start with defaults
	p := &Params{
		Logger: logger.Logger(logger.GetLogger().WithValues("egressID", request.EgressId)),
		Info: &livekit.EgressInfo{
			EgressId: request.EgressId,
			RoomId:   request.RoomId,
			Status:   livekit.EgressStatus_EGRESS_STARTING,
		},
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
	}

	var err error
	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		p.Info.Request = &livekit.EgressInfo_RoomComposite{RoomComposite: req.RoomComposite}

		// input params
		p.IsWebSource = true
		p.RoomName = req.RoomComposite.RoomName
		if p.RoomName == "" {
			return nil, errors.ErrInvalidInput("RoomName")
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
			err = p.updateFileParams(conf, o.File.Filepath, o.File.Output)
		case *livekit.RoomCompositeEgressRequest_Stream:
			err = p.updateStreamParams(o.Stream.Protocol, o.Stream.Urls)
		default:
			err = errors.ErrInvalidInput("output")
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
			return nil, errors.ErrInvalidInput("RoomName")
		}

		p.AudioTrackID = req.TrackComposite.AudioTrackId
		p.VideoTrackID = req.TrackComposite.VideoTrackId
		p.AudioEnabled = p.AudioTrackID != ""
		p.VideoEnabled = p.VideoTrackID != ""
		if !p.AudioEnabled && !p.VideoEnabled {
			return nil, errors.ErrInvalidInput("TrackIDs")
		}

		// output params
		switch o := req.TrackComposite.Output.(type) {
		case *livekit.TrackCompositeEgressRequest_File:
			if o.File.FileType != livekit.EncodedFileType_DEFAULT_FILETYPE {
				p.updateOutputType(o.File.FileType)
			}
			err = p.updateFileParams(conf, o.File.Filepath, o.File.Output)
		case *livekit.TrackCompositeEgressRequest_Stream:
			err = p.updateStreamParams(o.Stream.Protocol, o.Stream.Urls)
		default:
			err = errors.ErrInvalidInput("output")
		}

	case *livekit.StartEgressRequest_Track:
		p.Info.Request = &livekit.EgressInfo_Track{Track: req.Track}

		// input params
		p.RoomName = req.Track.RoomName
		if p.RoomName == "" {
			return nil, errors.ErrInvalidInput("RoomName")
		}

		p.TrackID = req.Track.TrackId
		if p.TrackID == "" {
			return nil, errors.ErrInvalidInput("TrackID")
		}

		// output params
		switch o := req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_File:
			err = p.updateFileParams(conf, o.File.Filepath, o.File.Output)
		case *livekit.TrackEgressRequest_WebsocketUrl:
			err = errors.ErrNotSupported("websocket_url")
		default:
			err = errors.ErrInvalidInput("output")
		}

	default:
		err = errors.ErrInvalidInput("request")
	}

	if err != nil {
		return nil, err
	}

	if err = p.updateConnectionInfo(conf, request); err != nil {
		return nil, err
	}

	if p.OutputType != "" {
		if err = p.updateCodecsFromOutputType(); err != nil {
			return nil, err
		}
	}

	return p, nil
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

func (p *Params) updateOutputType(fileType livekit.EncodedFileType) {
	switch fileType {
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
}

func (p *Params) updateFileParams(conf *config.Config, filepath string, output interface{}) error {
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
		p.FileUpload = conf.FileUpload
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

func (p *Params) updateStreamParams(protocol livekit.StreamProtocol, urls []string) error {
	p.IsStream = true
	p.OutputType = OutputTypeRTMP
	p.StreamUrls = urls

	p.StreamInfo = make(map[string]*livekit.StreamInfo)
	var streamInfoList []*livekit.StreamInfo
	for _, url := range urls {
		if !strings.HasPrefix(url, "rtmp://") && !strings.HasPrefix(url, "rtmps://") {
			return errors.ErrInvalidUrl(url, protocol)
		}

		info := &livekit.StreamInfo{Url: url}
		p.StreamInfo[url] = info
		streamInfoList = append(streamInfoList, info)
	}

	p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
	return nil
}

func (p *Params) updateConnectionInfo(conf *config.Config, request *livekit.StartEgressRequest) error {
	// token
	if request.Token != "" {
		p.Token = request.Token
	} else if conf.ApiKey != "" && conf.ApiSecret != "" {
		token, err := egress.BuildEgressToken(p.Info.EgressId, conf.ApiKey, conf.ApiSecret, p.RoomName)
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
	} else if conf.WsUrl != "" {
		p.LKUrl = conf.WsUrl
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
			p.OutputType = OutputTypeOGG
		} else if p.VideoCodec == MimeTypeVP8 && !p.AudioEnabled {
			p.OutputType = OutputTypeIVF
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

	filename := p.Filepath
	if filename == "" || strings.HasSuffix(filename, "/") {
		filename = fmt.Sprintf("%s%s-%v%v", filename, identifier, time.Now().String(), ext)
	} else if !strings.HasSuffix(filename, string(ext)) {
		filename = filename + string(ext)
	}

	// get filename from path
	idx := strings.LastIndex(filename, "/")
	if idx > 0 {
		if p.FileUpload == nil {
			if err := os.MkdirAll(filename[:idx], os.ModeDir); err != nil {
				return err
			}
		} else {
			filename = filename[idx+1:]
		}
	}

	p.Logger.Debugw("writing to file", "filename", filename)
	p.Filename = filename
	p.FileInfo.Filename = filename
	return nil
}

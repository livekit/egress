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
	SourceParams
	AudioParams
	VideoParams

	// format
	IsStream       bool
	StreamProtocol livekit.StreamProtocol
	StreamUrls     []string
	FileParams

	// info
	Info       *livekit.EgressInfo
	FileInfo   *livekit.FileInfo
	StreamInfo map[string]*livekit.StreamInfo

	// logger
	Logger logger.Logger
}

type SourceParams struct {
	// source
	RoomName     string
	Token        string
	LKUrl        string
	TemplateBase string

	// web source
	IsWebInput     bool
	Display        string
	Layout         string
	CustomBase     string
	CustomInputURL string

	// sdk source
	TrackID      string
	AudioTrackID string
	VideoTrackID string
	SkipPipeline bool
}

type AudioParams struct {
	AudioEnabled   bool
	AudioCodec     livekit.AudioCodec
	AudioBitrate   int32
	AudioFrequency int32
}

type VideoParams struct {
	VideoEnabled bool
	VideoCodec   livekit.VideoCodec
	Width        int32
	Height       int32
	Depth        int32
	Framerate    int32
	VideoBitrate int32
}

type FileParams struct {
	Filename   string
	Filepath   string
	FileType   livekit.EncodedFileType
	FileUpload interface{}
}

func GetPipelineParams(conf *config.Config, request *livekit.StartEgressRequest) (*Params, error) {
	params := getEncodingParams(request)
	params.Info = &livekit.EgressInfo{
		EgressId: request.EgressId,
		RoomId:   request.RoomId,
		Status:   livekit.EgressStatus_EGRESS_STARTING,
	}
	params.Logger = logger.Logger(logger.GetLogger().WithValues("egressID", request.EgressId))

	var format string
	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		params.Info.Request = &livekit.EgressInfo_RoomComposite{RoomComposite: req.RoomComposite}

		params.IsWebInput = true
		params.Display = fmt.Sprintf(":%d", 10+rand.Intn(2147483637))
		params.AudioEnabled = !req.RoomComposite.VideoOnly
		params.VideoEnabled = !req.RoomComposite.AudioOnly
		params.RoomName = req.RoomComposite.RoomName
		params.Layout = req.RoomComposite.Layout
		if req.RoomComposite.CustomBaseUrl != "" {
			params.TemplateBase = req.RoomComposite.CustomBaseUrl
		} else {
			params.TemplateBase = conf.TemplateBase
		}

		switch o := req.RoomComposite.Output.(type) {
		case *livekit.RoomCompositeEgressRequest_File:
			if o.File.FileType == livekit.EncodedFileType_DEFAULT_FILETYPE {
				if !params.VideoEnabled && params.AudioCodec != livekit.AudioCodec_AAC {
					o.File.FileType = livekit.EncodedFileType_OGG
				} else {
					o.File.FileType = livekit.EncodedFileType_MP4
				}
			}

			format = o.File.FileType.String()
			if err := params.updateFileInfo(conf, o.File.FileType, o.File.Filepath, o.File.Output); err != nil {
				return nil, err
			}

		case *livekit.RoomCompositeEgressRequest_Stream:
			if o.Stream.Protocol == livekit.StreamProtocol_DEFAULT_PROTOCOL {
				o.Stream.Protocol = livekit.StreamProtocol_RTMP
			}

			format = o.Stream.Protocol.String()
			if err := params.updateStreamInfo(o.Stream.Protocol, o.Stream.Urls); err != nil {
				return nil, err
			}

		default:
			return nil, errors.ErrInvalidInput("output")
		}

	case *livekit.StartEgressRequest_TrackComposite:
		params.Info.Request = &livekit.EgressInfo_TrackComposite{TrackComposite: req.TrackComposite}

		params.AudioTrackID = req.TrackComposite.AudioTrackId
		params.AudioEnabled = params.AudioTrackID != ""
		params.VideoTrackID = req.TrackComposite.VideoTrackId
		params.VideoEnabled = params.VideoTrackID != ""
		params.RoomName = req.TrackComposite.RoomName

		switch o := req.TrackComposite.Output.(type) {
		case *livekit.TrackCompositeEgressRequest_File:
			if o.File.FileType == livekit.EncodedFileType_DEFAULT_FILETYPE {
				if !params.VideoEnabled && params.AudioCodec != livekit.AudioCodec_AAC {
					o.File.FileType = livekit.EncodedFileType_OGG
				} else {
					o.File.FileType = livekit.EncodedFileType_MP4
				}
			}

			format = o.File.FileType.String()
			if err := params.updateFileInfo(conf, o.File.FileType, o.File.Filepath, o.File.Output); err != nil {
				return nil, err
			}

		case *livekit.TrackCompositeEgressRequest_Stream:
			if o.Stream.Protocol == livekit.StreamProtocol_DEFAULT_PROTOCOL {
				o.Stream.Protocol = livekit.StreamProtocol_RTMP
			}

			format = o.Stream.Protocol.String()
			if err := params.updateStreamInfo(o.Stream.Protocol, o.Stream.Urls); err != nil {
				return nil, err
			}

		default:
			return nil, errors.ErrInvalidInput("output")
		}

	case *livekit.StartEgressRequest_Track:
		params.Info.Request = &livekit.EgressInfo_Track{Track: req.Track}

		params.RoomName = req.Track.RoomName
		params.TrackID = req.Track.TrackId

		err := params.updateConnectionInfo(conf, request)
		if err != nil {
			return nil, err
		}

		switch o := req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_File:
			if err := params.updateFileInfo(conf, -1, o.File.Filepath, o.File.Output); err != nil {
				return nil, err
			}
			return params, nil

		case *livekit.TrackEgressRequest_WebsocketUrl:
			return nil, errors.ErrNotSupported("websocket_url")

		default:
			return nil, errors.ErrInvalidInput("output")
		}

	default:
		return nil, errors.ErrInvalidInput("request")
	}

	if err := params.updateConnectionInfo(conf, request); err != nil {
		return nil, err
	}

	// check audio codec
	if params.AudioEnabled {
		if params.AudioCodec == livekit.AudioCodec_DEFAULT_AC {
			params.AudioCodec = DefaultAudioCodecs[format]
		} else if !compatibleAudioCodecs[format][params.AudioCodec] {
			return nil, errors.ErrIncompatible(format, params.AudioCodec)
		}
	}

	// check video codec
	if params.VideoEnabled {
		if params.VideoCodec == livekit.VideoCodec_DEFAULT_VC {
			params.VideoCodec = DefaultVideoCodecs[format]
		} else if !compatibleVideoCodecs[format][params.VideoCodec] {
			return nil, errors.ErrIncompatible(format, params.VideoCodec)
		}
	}

	return params, nil
}

func getEncodingParams(request *livekit.StartEgressRequest) *Params {
	var preset livekit.EncodingOptionsPreset = -1
	var advanced *livekit.EncodingOptions

	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		switch opts := req.RoomComposite.Options.(type) {
		case *livekit.RoomCompositeEgressRequest_Preset:
			preset = opts.Preset
		case *livekit.RoomCompositeEgressRequest_Advanced:
			advanced = opts.Advanced
		}
	case *livekit.StartEgressRequest_TrackComposite:
		switch options := req.TrackComposite.Options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			preset = options.Preset
		case *livekit.TrackCompositeEgressRequest_Advanced:
			advanced = options.Advanced
		}
	case *livekit.StartEgressRequest_Track:
		return &Params{}
	}

	params := fullHD30
	if preset != -1 {
		switch preset {
		case livekit.EncodingOptionsPreset_H264_720P_30:
			params = hd30
		case livekit.EncodingOptionsPreset_H264_720P_60:
			params = hd60
		case livekit.EncodingOptionsPreset_H264_1080P_30:
			// default
		case livekit.EncodingOptionsPreset_H264_1080P_60:
			params = fullHD60
		}
	} else if advanced != nil {
		// audio
		params.AudioCodec = advanced.AudioCodec
		if advanced.AudioBitrate != 0 {
			params.AudioBitrate = advanced.AudioBitrate
		}
		if advanced.AudioFrequency != 0 {
			params.AudioFrequency = advanced.AudioFrequency
		}

		// video
		params.VideoCodec = advanced.VideoCodec
		if advanced.Width != 0 {
			params.Width = advanced.Width
		}
		if advanced.Height != 0 {
			params.Height = advanced.Height
		}
		if advanced.Depth != 0 {
			params.Depth = advanced.Depth
		}
		if advanced.Framerate != 0 {
			params.Framerate = advanced.Framerate
		}
		if advanced.VideoBitrate != 0 {
			params.VideoBitrate = advanced.VideoBitrate
		}
	}

	return &params
}

func (p *Params) updateStreamInfo(protocol livekit.StreamProtocol, urls []string) error {
	p.IsStream = true
	p.StreamProtocol = protocol
	p.StreamUrls = urls

	p.StreamInfo = make(map[string]*livekit.StreamInfo)
	var streamInfoList []*livekit.StreamInfo
	for _, url := range urls {
		switch protocol {
		case livekit.StreamProtocol_RTMP:
			if !strings.HasPrefix(url, "rtmp://") && !strings.HasPrefix(url, "rtmps://") {
				return errors.ErrInvalidUrl(url, protocol)
			}
		}

		info := &livekit.StreamInfo{
			Url: url,
		}
		p.StreamInfo[url] = info
		streamInfoList = append(streamInfoList, info)
	}
	p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
	return nil
}

func (p *Params) updateFileInfo(conf *config.Config, fileType livekit.EncodedFileType, filepath string, output interface{}) error {
	local := false
	switch o := output.(type) {
	case *livekit.EncodedFileOutput_S3:
		p.FileUpload = o.S3
	case *livekit.EncodedFileOutput_Azure:
		p.FileUpload = o.Azure
	case *livekit.EncodedFileOutput_Gcp:
		p.FileUpload = o.Gcp
	default:
		if conf.FileUpload != nil {
			p.FileUpload = conf.FileUpload
		} else {
			local = true
		}
	}

	p.Filepath = filepath
	p.FileInfo = &livekit.FileInfo{}

	if fileType != -1 {
		filename, err := getFilename(filepath, fileType, p.RoomName, local)
		if err != nil {
			return err
		}

		p.FileType = fileType
		p.Filename = filename
		p.FileInfo.Filename = filename
	}

	p.Info.Result = &livekit.EgressInfo_File{File: p.FileInfo}

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

	if request.WsUrl != "" {
		p.LKUrl = request.WsUrl
	} else if conf.WsUrl != "" {
		p.LKUrl = conf.WsUrl
	} else {
		return errors.ErrInvalidInput("ws_url")
	}

	return nil
}

func getFilename(filepath string, fileType livekit.EncodedFileType, roomName string, local bool) (string, error) {
	ext := "." + strings.ToLower(fileType.String())
	if filepath == "" {
		return fmt.Sprintf("%s-%v%s", roomName, time.Now().String(), ext), nil
	}

	// check for extension
	if !strings.HasSuffix(filepath, ext) {
		filepath = filepath + ext
	}

	// get filename from path
	idx := strings.LastIndex(filepath, "/")
	if idx == -1 {
		return filepath, nil
	}

	if local {
		if err := os.MkdirAll(filepath[:idx], os.ModeDir); err != nil {
			return "", err
		}
		return filepath, nil
	}

	return filepath[idx+1:], nil
}

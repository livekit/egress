package config

import (
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/errors"
)

type Params struct {
	// source
	RoomName string

	// web source
	IsWebInput     bool
	Layout         string
	CustomBase     string
	CustomInputURL string

	// sdk source
	TrackID      string
	AudioTrackID string
	VideoTrackID string

	// audio
	AudioEnabled   bool
	AudioCodec     livekit.AudioCodec
	AudioBitrate   int32
	AudioFrequency int32

	// video
	VideoEnabled bool
	VideoCodec   livekit.VideoCodec
	Width        int32
	Height       int32
	Depth        int32
	Framerate    int32
	VideoBitrate int32

	// format
	Transcode bool
	WSUrl     string

	IsStream       bool
	StreamProtocol livekit.StreamProtocol
	StreamUrls     []string

	FileType livekit.EncodedFileType
	FileUrl  string

	// info
	Info *livekit.EgressInfo
}

var (
	hd30 = Params{
		AudioEnabled:   true,
		AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoEnabled:   true,
		VideoCodec:     livekit.VideoCodec_DEFAULT_VC,
		Width:          1280,
		Height:         720,
		Depth:          24,
		Framerate:      30,
		VideoBitrate:   3000,
	}
	hd60 = Params{
		AudioEnabled:   true,
		AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoEnabled:   true,
		VideoCodec:     livekit.VideoCodec_DEFAULT_VC,
		Width:          1280,
		Height:         720,
		Depth:          24,
		Framerate:      60,
		VideoBitrate:   4500,
	}
	fullHD30 = Params{
		AudioEnabled:   true,
		AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoEnabled:   true,
		VideoCodec:     livekit.VideoCodec_DEFAULT_VC,
		Width:          1920,
		Height:         1080,
		Depth:          24,
		Framerate:      30,
		VideoBitrate:   4500,
	}
	fullHD60 = Params{
		AudioEnabled:   true,
		AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoEnabled:   true,
		VideoCodec:     livekit.VideoCodec_DEFAULT_VC,
		Width:          1920,
		Height:         1080,
		Depth:          24,
		Framerate:      60,
		VideoBitrate:   6000,
	}
)

func getEncodingParams(request *livekit.StartEgressRequest) *Params {
	var preset livekit.EncodingOptionsPreset = -1
	var advanced *livekit.EncodingOptions

	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_WebComposite:
		switch opts := req.WebComposite.Options.(type) {
		case *livekit.WebCompositeEgressRequest_Preset:
			preset = opts.Preset
		case *livekit.WebCompositeEgressRequest_Advanced:
			advanced = opts.Advanced
		}
	case *livekit.StartEgressRequest_TrackComposite:
		switch options := req.TrackComposite.Options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			preset = options.Preset
		case *livekit.TrackCompositeEgressRequest_Advanced:
			advanced = options.Advanced
		}
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

func GetPipelineParams(request *livekit.StartEgressRequest) (*Params, error) {
	params := getEncodingParams(request)
	params.Info = &livekit.EgressInfo{
		EgressId: request.EgressId,
	}

	var format string
	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_WebComposite:
		params.Info.EgressType = livekit.EgressType_WEB_COMPOSITE_EGRESS
		params.Info.Request = &livekit.EgressInfo_WebComposite{WebComposite: req.WebComposite}

		params.Transcode = true
		params.AudioEnabled = !req.WebComposite.AudioOnly
		params.VideoEnabled = !req.WebComposite.VideoOnly
		params.IsWebInput = true
		params.Layout = req.WebComposite.Layout
		params.RoomName = req.WebComposite.RoomName
		params.CustomBase = req.WebComposite.CustomBaseUrl

		switch o := req.WebComposite.Output.(type) {
		case *livekit.WebCompositeEgressRequest_File:
			format = o.File.FileType.String()
			params.FileType = o.File.FileType
			params.FileUrl = o.File.HttpUrl
		case *livekit.WebCompositeEgressRequest_Stream:
			format = o.Stream.Protocol.String()
			params.IsStream = true
			params.StreamProtocol = o.Stream.Protocol
			params.StreamUrls = o.Stream.Urls
		default:
			return nil, errors.ErrInvalidRequest
		}
	case *livekit.StartEgressRequest_TrackComposite:
		params.Info.EgressType = livekit.EgressType_TRACK_COMPOSITE_EGRESS
		params.Info.Request = &livekit.EgressInfo_TrackComposite{TrackComposite: req.TrackComposite}

		params.Transcode = true
		params.AudioEnabled = req.TrackComposite.AudioTrackId != ""
		params.VideoEnabled = req.TrackComposite.VideoTrackId != ""
		params.RoomName = req.TrackComposite.RoomName

		switch o := req.TrackComposite.Output.(type) {
		case *livekit.TrackCompositeEgressRequest_File:
			format = o.File.FileType.String()
			params.FileType = o.File.FileType
			params.FileUrl = o.File.HttpUrl
		case *livekit.TrackCompositeEgressRequest_Stream:
			format = o.Stream.Protocol.String()
			params.IsStream = true
			params.StreamProtocol = o.Stream.Protocol
			params.StreamUrls = o.Stream.Urls
		}

		return nil, errors.ErrNotSupported("track composite requests")
	case *livekit.StartEgressRequest_Track:
		params.Info.EgressType = livekit.EgressType_TRACK_EGRESS
		params.Info.Request = &livekit.EgressInfo_Track{Track: req.Track}

		params.RoomName = req.Track.RoomName
		params.TrackID = req.Track.TrackId

		switch o := req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_HttpUrl:
			params.FileUrl = o.HttpUrl
		case *livekit.TrackEgressRequest_WebsocketUrl:
			params.WSUrl = o.WebsocketUrl
		default:
			return nil, errors.ErrInvalidRequest
		}

		return nil, errors.ErrNotSupported("track requests")
		// return params, nil
	default:
		return nil, errors.ErrInvalidRequest
	}

	// check audio codec
	if params.AudioEnabled {
		if params.AudioCodec == livekit.AudioCodec_DEFAULT_AC {
			params.AudioCodec = defaultAudioCodecs[format]
		} else if !compatibleAudioCodecs[format][params.AudioCodec] {
			return nil, errors.ErrIncompatible(format, params.AudioCodec)
		}
	}

	// check video codec
	if params.VideoEnabled {
		if params.VideoCodec == livekit.VideoCodec_DEFAULT_VC {
			params.VideoCodec = defaultVideoCodecs[format]
		} else if !compatibleVideoCodecs[format][params.VideoCodec] {
			return nil, errors.ErrIncompatible(format, params.VideoCodec)
		}
	}

	return params, nil
}

var (
	mp4  = livekit.EncodedFileType_MP4.String()
	webm = livekit.EncodedFileType_WEBM.String()
	ogg  = livekit.EncodedFileType_OGG.String()
	rtmp = livekit.StreamProtocol_RTMP.String()
	srt  = livekit.StreamProtocol_SRT.String()

	defaultAudioCodecs = map[string]livekit.AudioCodec{
		mp4:  livekit.AudioCodec_AAC,
		webm: livekit.AudioCodec_OPUS,
		ogg:  livekit.AudioCodec_OPUS,
		rtmp: livekit.AudioCodec_AAC,
		srt:  livekit.AudioCodec(-1), // unknown
	}

	defaultVideoCodecs = map[string]livekit.VideoCodec{
		mp4:  livekit.VideoCodec_H264_MAIN,
		webm: livekit.VideoCodec_VP8,
		ogg:  livekit.VideoCodec_VP8,
		rtmp: livekit.VideoCodec_H264_MAIN,
		srt:  livekit.VideoCodec(-1), // unknown
	}

	compatibleAudioCodecs = map[string]map[livekit.AudioCodec]bool{
		mp4: {
			livekit.AudioCodec_AAC:  true,
			livekit.AudioCodec_OPUS: true,
		},
		webm: {
			livekit.AudioCodec_OPUS: true,
		},
		ogg: {
			livekit.AudioCodec_OPUS: true,
		},
		rtmp: {
			livekit.AudioCodec_AAC: true,
		},
		srt: {
			// unknown
		},
	}

	compatibleVideoCodecs = map[string]map[livekit.VideoCodec]bool{
		mp4: {
			livekit.VideoCodec_H264_BASELINE: true,
			livekit.VideoCodec_H264_MAIN:     true,
			livekit.VideoCodec_H264_HIGH:     true,
			livekit.VideoCodec_HEVC_MAIN:     true,
			livekit.VideoCodec_HEVC_HIGH:     true,
		},
		webm: {
			livekit.VideoCodec_VP8: true,
			livekit.VideoCodec_VP9: true,
		},
		ogg: {
			livekit.VideoCodec_VP8: true,
		},
		rtmp: {
			livekit.VideoCodec_H264_BASELINE: true,
			livekit.VideoCodec_H264_MAIN:     true,
			livekit.VideoCodec_H264_HIGH:     true,
		},
		srt: {
			// unknown
		},
	}
)

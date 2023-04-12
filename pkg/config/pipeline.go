package config

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst/app"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/egress/pkg/util"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

const (
	webLatency = uint64(15e8)
	sdkLatency = uint64(43e8)
)

type PipelineConfig struct {
	BaseConfig `yaml:",inline"`

	HandlerID string `yaml:"handler_id"`
	TmpDir    string `yaml:"tmp_dir"`

	SourceConfig `yaml:"-"`
	AudioConfig  `yaml:"-"`
	VideoConfig  `yaml:"-"`

	Outputs     map[types.EgressType]*OutputConfig `yaml:"-"`
	OutputCount int

	GstReady chan struct{}       `yaml:"-"`
	Info     *livekit.EgressInfo `yaml:"-"`
}

type SourceConfig struct {
	SourceType types.SourceType
	Latency    uint64
	WebSourceParams
	SDKSourceParams
}

type WebSourceParams struct {
	Display string
	Layout  string
	Token   string
	BaseUrl string
	WebUrl  string
}

type SDKSourceParams struct {
	TrackID             string
	TrackSource         string
	TrackKind           string
	AudioTrackID        string
	VideoTrackID        string
	ParticipantIdentity string
	AudioSrc            *app.Source
	VideoSrc            *app.Source
	AudioInCodec        types.MimeType
	VideoInCodec        types.MimeType
	AudioCodecParams    webrtc.RTPCodecParameters
	VideoCodecParams    webrtc.RTPCodecParameters
}

type AudioConfig struct {
	AudioEnabled     bool
	AudioTranscoding bool
	AudioOutCodec    types.MimeType
	AudioBitrate     int32
	AudioFrequency   int32
}

type VideoConfig struct {
	VideoEnabled     bool
	VideoTranscoding bool
	VideoOutCodec    types.MimeType
	VideoProfile     types.Profile
	Width            int32
	Height           int32
	Depth            int32
	Framerate        int32
	VideoBitrate     int32
	KeyFrameInterval float64
}

func NewPipelineConfig(confString string, req *rpc.StartEgressRequest) (*PipelineConfig, error) {
	p := &PipelineConfig{
		BaseConfig: BaseConfig{},
		Outputs:    make(map[types.EgressType]*OutputConfig),
		GstReady:   make(chan struct{}),
	}

	if err := yaml.Unmarshal([]byte(confString), p); err != nil {
		return nil, errors.ErrCouldNotParseConfig(err)
	}

	if err := p.initLogger(
		"nodeID", p.NodeID,
		"handlerID", p.HandlerID,
		"clusterID", p.ClusterID,
		"egressID", req.EgressId,
	); err != nil {
		return nil, err
	}

	return p, p.Update(req)
}

func GetValidatedPipelineConfig(conf *ServiceConfig, req *rpc.StartEgressRequest) (*PipelineConfig, error) {
	_, span := tracer.Start(context.Background(), "config.GetValidatedPipelineConfig")
	defer span.End()

	p := &PipelineConfig{
		BaseConfig: conf.BaseConfig,
		Outputs:    make(map[types.EgressType]*OutputConfig),
	}

	return p, p.Update(req)
}

func (p *PipelineConfig) Update(request *rpc.StartEgressRequest) error {
	if request.EgressId == "" {
		return errors.ErrInvalidInput("No Egress Id")
	}

	// start with defaults
	p.Info = &livekit.EgressInfo{
		EgressId: request.EgressId,
		RoomId:   request.RoomId,
		Status:   livekit.EgressStatus_EGRESS_STARTING,
	}
	p.AudioConfig = AudioConfig{
		AudioBitrate:   128,
		AudioFrequency: 44100,
	}
	p.VideoConfig = VideoConfig{
		VideoProfile: types.ProfileMain,
		Width:        1920,
		Height:       1080,
		Depth:        24,
		Framerate:    30,
		VideoBitrate: 4500,
	}

	connectionInfoRequired := true
	switch req := request.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		clone := proto.Clone(req.RoomComposite).(*livekit.RoomCompositeEgressRequest)
		p.Info.Request = &livekit.EgressInfo_RoomComposite{
			RoomComposite: clone,
		}
		redactEncodedOutputs(clone)

		p.SourceType = types.SourceTypeWeb
		p.Latency = webLatency

		p.Info.RoomName = req.RoomComposite.RoomName
		p.Layout = req.RoomComposite.Layout
		if req.RoomComposite.CustomBaseUrl != "" {
			p.BaseUrl = req.RoomComposite.CustomBaseUrl
		} else {
			p.BaseUrl = p.TemplateBase
		}
		baseUrl, err := url.Parse(p.BaseUrl)
		if err != nil || (baseUrl.Scheme != "http" && baseUrl.Scheme != "https") {
			return errors.ErrInvalidInput("template base url")
		}

		if !req.RoomComposite.VideoOnly {
			p.AudioEnabled = true
			p.AudioInCodec = types.MimeTypeRawAudio
			p.AudioTranscoding = true
		}
		if !req.RoomComposite.AudioOnly {
			p.VideoEnabled = true
			p.VideoInCodec = types.MimeTypeRawVideo
			p.VideoTranscoding = true
		}
		if !p.AudioEnabled && !p.VideoEnabled {
			return errors.ErrInvalidInput("audio_only and video_only")
		}

		// encoding options
		switch opts := req.RoomComposite.Options.(type) {
		case *livekit.RoomCompositeEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.RoomCompositeEgressRequest_Advanced:
			if err = p.applyAdvanced(opts.Advanced); err != nil {
				return err
			}
		}

		// output params
		if err := p.updateEncodedOutputs(req.RoomComposite); err != nil {
			return err
		}

	case *rpc.StartEgressRequest_Web:
		clone := proto.Clone(req.Web).(*livekit.WebEgressRequest)
		p.Info.Request = &livekit.EgressInfo_Web{
			Web: clone,
		}
		redactEncodedOutputs(clone)

		connectionInfoRequired = false
		p.SourceType = types.SourceTypeWeb
		p.Latency = webLatency

		p.WebUrl = req.Web.Url
		webUrl, err := url.Parse(p.WebUrl)
		if err != nil || (webUrl.Scheme != "http" && webUrl.Scheme != "https") {
			return errors.ErrInvalidInput("web url")
		}

		if !req.Web.VideoOnly {
			p.AudioEnabled = true
			p.AudioInCodec = types.MimeTypeRawAudio
			p.AudioTranscoding = true
		}
		if !req.Web.AudioOnly {
			p.VideoEnabled = true
			p.VideoInCodec = types.MimeTypeRawVideo
			p.VideoTranscoding = true
		}
		if !p.AudioEnabled && !p.VideoEnabled {
			return errors.ErrInvalidInput("audio_only and video_only")
		}

		// encoding options
		switch opts := req.Web.Options.(type) {
		case *livekit.WebEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.WebEgressRequest_Advanced:
			if err = p.applyAdvanced(opts.Advanced); err != nil {
				return err
			}
		}

		// output params
		if err := p.updateEncodedOutputs(req.Web); err != nil {
			return err
		}

	case *rpc.StartEgressRequest_TrackComposite:
		clone := proto.Clone(req.TrackComposite).(*livekit.TrackCompositeEgressRequest)
		p.Info.Request = &livekit.EgressInfo_TrackComposite{
			TrackComposite: clone,
		}
		redactEncodedOutputs(clone)

		p.SourceType = types.SourceTypeSDK
		p.Latency = sdkLatency

		p.Info.RoomName = req.TrackComposite.RoomName
		if audioTrackID := req.TrackComposite.AudioTrackId; audioTrackID != "" {
			p.AudioEnabled = true
			p.AudioTrackID = audioTrackID
			p.AudioTranscoding = true
		}
		if videoTrackID := req.TrackComposite.VideoTrackId; videoTrackID != "" {
			p.VideoEnabled = true
			p.VideoTrackID = videoTrackID
			p.VideoTranscoding = true
		}
		if !p.AudioEnabled && !p.VideoEnabled {
			return errors.ErrInvalidInput("audio_track_id or video_track_id")
		}

		// encoding options
		switch opts := req.TrackComposite.Options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.TrackCompositeEgressRequest_Advanced:
			if err := p.applyAdvanced(opts.Advanced); err != nil {
				return err
			}
		}

		// output params
		if err := p.updateEncodedOutputs(req.TrackComposite); err != nil {
			return err
		}

	case *rpc.StartEgressRequest_Track:
		clone := proto.Clone(req.Track).(*livekit.TrackEgressRequest)
		p.Info.Request = &livekit.EgressInfo_Track{
			Track: clone,
		}
		if f := clone.GetFile(); f != nil {
			redactUpload(f)
		}

		p.SourceType = types.SourceTypeSDK
		p.Latency = sdkLatency

		p.Info.RoomName = req.Track.RoomName
		p.TrackID = req.Track.TrackId
		if p.TrackID == "" {
			return errors.ErrInvalidInput("track_id")
		}

		if err := p.updateDirectOutput(req.Track); err != nil {
			return nil
		}

	default:
		return errors.ErrInvalidInput("request")
	}

	// connection info
	if connectionInfoRequired {
		if p.Info.RoomName == "" {
			return errors.ErrInvalidInput("room_name")
		}

		// token
		if request.Token != "" {
			p.Token = request.Token
		} else if p.ApiKey != "" && p.ApiSecret != "" {
			token, err := egress.BuildEgressToken(p.Info.EgressId, p.ApiKey, p.ApiSecret, p.Info.RoomName)
			if err != nil {
				return err
			}
			p.Token = token
		} else {
			return errors.ErrInvalidInput("token or api key/secret")
		}

		// url
		if request.WsUrl != "" {
			p.WsUrl = request.WsUrl
		} else if p.WsUrl == "" {
			return errors.ErrInvalidInput("ws_url")
		}
	}

	err = p.validateAndUpdateCodecs()
	if err != nil {
		return err
	}

	err = validateAndUpdateFileOutputType()
	if err != nil {
		return err
	}

}

func (p *PipelineConfig) validateAndUpdateCodecs() error {
	compatibleAudioCodecs := types.AllAudioCodecs
	compatibleVideoCodecs := types.AllVideoCodecs

	if p.AudioEnabled {
		if p.AudioOutCodec == "" {
			// No provided codec. First pass: find the list of codecs supported by all outputs, if any
			for _, o := range p.Outputs {
				compatibleAudioCodecs = types.FilterSupportedCodecsInList(compatibleAudioCodecs, types.CodecCompatibility[o.OutputType])
			}

			if len(compatibleAudioCodecs) == 0 {
				return errors.ErrNoCompatibleCodec
			}

			// 2nd pass: try to find a prefered codec
			for _, o := range p.Outputs {
				c := types.DefaultAudioCodecs[o.OutputType]
				if compatibleAudioCodecs[c] {
					p.AudioOutCodec = c
					break
				}
			}

			// No preferred codec found. Pick one from the list
			for k, _ := range compatibleAudioCodecs {
				p.AudioOutCodec = k
				break
			}
		} else {
			for _, o := range p.Outputs {
				if !types.CodecCompatibility[o.OutputType][p.AudioOutCodec] {
					return errors.ErrIncompatible(o.OutputType, p.AudioOutCodec)
				}
			}
		}

	}

	if p.VideoEnabled {
		if p.VideoOutCodec == "" {
			// No provided codec. First pass: find the list of codecs supported by all outputs, if any
			for _, o := range p.Outputs {
				compatibleVideoCodecs = types.FilterSupportedCodecsInList(compatibleVideoCodecs, types.CodecCompatibility[o.OutputType])
			}

			if len(compatibleVideoCodecs) == 0 {
				return errors.ErrNoCompatibleCodec
			}

			// 2nd pass: try to find a prefered codec
			for _, o := range p.Outputs {
				c := types.DefaultVideoCodecs[o.OutputType]
				if compatibleVideoCodecs[c] {
					p.VideoOutCodec = c
					break
				}
			}

			// No preferred codec found. Pick one from the list
			for k, _ := range compatibleVideoCodecs {
				p.VideoOutCodec = k
				break
			}
		} else {
			for _, o := range p.Outputs {
				if !types.CodecCompatibility[o.OutputType][p.VideoOutCodec] {
					return errors.ErrIncompatible(o.OutputType, p.VideoOutCodec)
				}
			}
		}
	}

	return nil
}

// used for sdk input source
func (p *PipelineConfig) UpdateInfoFromSDK(identifier string, replacements map[string]string) error {
	for egressType, o := range p.Outputs {
		switch egressType {
		case types.EgressTypeFile:
			//			if o.OutputType == types.OutputTypeUnknownFile {
			//				if !p.VideoEnabled {
			// audio input is always opus
			//					o.OutputType = types.OutputTypeOGG
			//				} else {
			//					o.OutputType = types.OutputTypeMP4
			//				}
			//			}
			return o.updateFilepath(p, identifier, replacements)

		case types.EgressTypeSegments:
			o.LocalDir = stringReplace(o.LocalDir, replacements)
			o.StorageDir = stringReplace(o.StorageDir, replacements)
			o.PlaylistFilename = stringReplace(o.PlaylistFilename, replacements)
			o.SegmentPrefix = stringReplace(o.SegmentPrefix, replacements)
			o.SegmentsInfo.PlaylistName = stringReplace(o.SegmentsInfo.PlaylistName, replacements)
		}
	}

	return nil
}

func (p *PipelineConfig) ValidateUrl(rawUrl string, outputType types.OutputType) (string, error) {
	parsed, err := url.Parse(rawUrl)
	if err != nil {
		return "", errors.ErrInvalidUrl(rawUrl, err.Error())
	}

	switch outputType {
	case types.OutputTypeRTMP:
		redacted, ok := util.RedactStreamKey(rawUrl)
		if !ok {
			return "", errors.ErrInvalidUrl(rawUrl, "rtmp urls must be of format rtmp(s)://{host}(/{path})/{app}/{stream_key}( live=1)")
		}
		return redacted, nil

	case types.OutputTypeRaw:
		if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
			return "", errors.ErrInvalidUrl(rawUrl, "invalid scheme")
		}
		return rawUrl, nil

	default:
		return "", errors.ErrInvalidInput("stream output type")
	}
}

type Manifest struct {
	EgressID          string `json:"egress_id,omitempty"`
	RoomID            string `json:"room_id,omitempty"`
	RoomName          string `json:"room_name,omitempty"`
	Url               string `json:"url,omitempty"`
	StartedAt         int64  `json:"started_at,omitempty"`
	EndedAt           int64  `json:"ended_at,omitempty"`
	PublisherIdentity string `json:"publisher_identity,omitempty"`
	TrackID           string `json:"track_id,omitempty"`
	TrackKind         string `json:"track_kind,omitempty"`
	TrackSource       string `json:"track_source,omitempty"`
	AudioTrackID      string `json:"audio_track_id,omitempty"`
	VideoTrackID      string `json:"video_track_id,omitempty"`
	SegmentCount      int64  `json:"segment_count,omitempty"`
}

func (p *PipelineConfig) GetManifest(egressType types.EgressType) ([]byte, error) {
	manifest := Manifest{
		EgressID:          p.Info.EgressId,
		RoomID:            p.Info.RoomId,
		RoomName:          p.Info.RoomName,
		Url:               p.WebUrl,
		StartedAt:         p.Info.StartedAt,
		EndedAt:           p.Info.EndedAt,
		PublisherIdentity: p.ParticipantIdentity,
		TrackID:           p.TrackID,
		TrackKind:         p.TrackKind,
		TrackSource:       p.TrackSource,
		AudioTrackID:      p.AudioTrackID,
		VideoTrackID:      p.VideoTrackID,
	}

	if egressType == types.EgressTypeSegments {
		o := p.Outputs[egressType]
		manifest.SegmentCount = o.SegmentsInfo.SegmentCount
	}

	return json.Marshal(manifest)
}

func stringReplace(s string, replacements map[string]string) string {
	for template, value := range replacements {
		s = strings.Replace(s, template, value, -1)
	}
	return s
}

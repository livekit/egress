// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v3"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const Latency = uint64(3e9)

type PipelineConfig struct {
	BaseConfig `yaml:",inline"`

	HandlerID string `yaml:"handler_id"`
	TmpDir    string `yaml:"tmp_dir"`

	types.RequestType `yaml:"-"`
	SourceConfig      `yaml:"-"`
	AudioConfig       `yaml:"-"`
	VideoConfig       `yaml:"-"`

	Outputs              map[types.EgressType][]OutputConfig `yaml:"-"`
	OutputCount          int                                 `yaml:"-"`
	FinalizationRequired bool                                `yaml:"-"`

	Info *livekit.EgressInfo `yaml:"-"`
}

type SourceConfig struct {
	SourceType types.SourceType
	WebSourceParams
	SDKSourceParams
}

type WebSourceParams struct {
	AwaitStartSignal bool
	Display          string
	Layout           string
	Token            string
	BaseUrl          string
	WebUrl           string
}

type SDKSourceParams struct {
	TrackID      string
	AudioTrackID string
	VideoTrackID string
	Identity     string
	TrackSource  string
	TrackKind    string
	AudioInCodec types.MimeType
	VideoInCodec types.MimeType
	AudioTrack   *TrackSource
	VideoTrack   *TrackSource
}

type TrackSource struct {
	TrackID     string
	Kind        lksdk.TrackKind
	AppSrc      *app.Source
	MimeType    types.MimeType
	PayloadType webrtc.PayloadType
	ClockRate   uint32
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
	VideoDecoding    bool
	VideoEncoding    bool
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
		BaseConfig: BaseConfig{
			Logging: &logger.Config{
				Level: "info",
			},
		},
		Outputs: make(map[types.EgressType][]OutputConfig),
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
		Outputs:    make(map[types.EgressType][]OutputConfig),
	}

	return p, p.Update(req)
}

func (p *PipelineConfig) Update(request *rpc.StartEgressRequest) error {
	if request.EgressId == "" {
		return errors.ErrInvalidInput("egressID")
	}

	// start with defaults
	p.Info = &livekit.EgressInfo{
		EgressId:  request.EgressId,
		RoomId:    request.RoomId,
		Status:    livekit.EgressStatus_EGRESS_STARTING,
		UpdatedAt: time.Now().UnixNano(),
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
		p.RequestType = types.RequestTypeRoomComposite
		clone := proto.Clone(req.RoomComposite).(*livekit.RoomCompositeEgressRequest)
		p.Info.Request = &livekit.EgressInfo_RoomComposite{
			RoomComposite: clone,
		}
		redactEncodedOutputs(clone)

		p.SourceType = types.SourceTypeWeb
		p.AwaitStartSignal = true

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
			p.VideoDecoding = true
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
		if err = p.updateEncodedOutputs(req.RoomComposite); err != nil {
			return err
		}

	case *rpc.StartEgressRequest_Web:
		p.RequestType = types.RequestTypeWeb
		clone := proto.Clone(req.Web).(*livekit.WebEgressRequest)
		p.Info.Request = &livekit.EgressInfo_Web{
			Web: clone,
		}
		redactEncodedOutputs(clone)

		connectionInfoRequired = false
		p.SourceType = types.SourceTypeWeb
		p.AwaitStartSignal = req.Web.AwaitStartSignal

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
			p.VideoDecoding = true
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
		if err = p.updateEncodedOutputs(req.Web); err != nil {
			return err
		}

	case *rpc.StartEgressRequest_Participant:
		p.RequestType = types.RequestTypeParticipant
		clone := proto.Clone(req.Participant).(*livekit.ParticipantEgressRequest)
		p.Info.Request = &livekit.EgressInfo_Participant{
			Participant: clone,
		}
		redactEncodedOutputs(clone)

		p.SourceType = types.SourceTypeSDK

		p.Info.RoomName = req.Participant.RoomName
		p.AudioEnabled = true
		p.AudioTranscoding = true
		p.VideoEnabled = true
		p.VideoDecoding = true
		p.Identity = req.Participant.Identity
		if p.Identity == "" {
			return errors.ErrInvalidInput("identity")
		}

		// encoding options
		switch opts := req.Participant.Options.(type) {
		case *livekit.ParticipantEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.ParticipantEgressRequest_Advanced:
			if err := p.applyAdvanced(opts.Advanced); err != nil {
				return err
			}
		}

		// output params
		if err := p.updateEncodedOutputs(req.Participant); err != nil {
			return err
		}

	case *rpc.StartEgressRequest_TrackComposite:
		p.RequestType = types.RequestTypeTrackComposite
		clone := proto.Clone(req.TrackComposite).(*livekit.TrackCompositeEgressRequest)
		p.Info.Request = &livekit.EgressInfo_TrackComposite{
			TrackComposite: clone,
		}
		redactEncodedOutputs(clone)

		p.SourceType = types.SourceTypeSDK

		p.Info.RoomName = req.TrackComposite.RoomName
		if audioTrackID := req.TrackComposite.AudioTrackId; audioTrackID != "" {
			p.AudioEnabled = true
			p.AudioTrackID = audioTrackID
			p.AudioTranscoding = true
		}
		if videoTrackID := req.TrackComposite.VideoTrackId; videoTrackID != "" {
			p.VideoEnabled = true
			p.VideoTrackID = videoTrackID
			p.VideoDecoding = true
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
		p.RequestType = types.RequestTypeTrack
		clone := proto.Clone(req.Track).(*livekit.TrackEgressRequest)
		p.Info.Request = &livekit.EgressInfo_Track{
			Track: clone,
		}
		if f := clone.GetFile(); f != nil {
			redactUpload(f)
		}

		p.SourceType = types.SourceTypeSDK

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

	if p.RequestType != types.RequestTypeTrack {
		err := p.validateAndUpdateOutputParams()
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *PipelineConfig) validateAndUpdateOutputParams() error {
	compatibleAudioCodecs, compatibleVideoCodecs, err := p.validateAndUpdateOutputCodecs()
	if err != nil {
		return err
	}

	// Find a compatible file format if not set
	err = p.updateOutputType(compatibleAudioCodecs, compatibleVideoCodecs)
	if err != nil {
		return err
	}

	// Select a codec compatible with all outputs
	if p.AudioEnabled {
		for _, o := range p.GetEncodedOutputs() {

			if compatibleAudioCodecs[types.DefaultAudioCodecs[o.GetOutputType()]] {
				p.AudioOutCodec = types.DefaultAudioCodecs[o.GetOutputType()]
				break
			}
		}
		if p.AudioOutCodec == "" {
			// No default codec found. Pick a random compatible one
			for k := range compatibleAudioCodecs {
				p.AudioOutCodec = k
			}
		}
	}

	if p.VideoEnabled {
		for _, o := range p.GetEncodedOutputs() {
			if compatibleVideoCodecs[types.DefaultVideoCodecs[o.GetOutputType()]] {
				p.VideoOutCodec = types.DefaultVideoCodecs[o.GetOutputType()]
				break
			}
		}
		if p.VideoOutCodec == "" {
			// No default codec found. Pick a random compatible one
			for k := range compatibleVideoCodecs {
				p.VideoOutCodec = k
			}
		}
	}

	return nil
}

func (p *PipelineConfig) validateAndUpdateOutputCodecs() (compatibleAudioCodecs map[types.MimeType]bool, compatibleVideoCodecs map[types.MimeType]bool, err error) {
	compatibleAudioCodecs = make(map[types.MimeType]bool)
	compatibleVideoCodecs = make(map[types.MimeType]bool)

	// Find video and audio codecs compatible with all outputs
	if p.AudioEnabled {
		if p.AudioOutCodec == "" {
			compatibleAudioCodecs = types.AllOutputAudioCodecs
		} else {
			compatibleAudioCodecs[p.AudioOutCodec] = true
		}

		for _, o := range p.GetEncodedOutputs() {
			compatibleAudioCodecs = types.GetMapIntersection(compatibleAudioCodecs, types.CodecCompatibility[o.GetOutputType()])
			if len(compatibleAudioCodecs) == 0 {
				if p.AudioOutCodec == "" {
					return nil, nil, errors.ErrNoCompatibleCodec
				} else {
					// Return a more specific error if a codec was provided
					return nil, nil, errors.ErrIncompatible(o.GetOutputType(), p.AudioOutCodec)
				}
			}
		}
	}

	if p.VideoEnabled {
		if p.VideoOutCodec == "" {
			compatibleVideoCodecs = types.AllOutputVideoCodecs
		} else {
			compatibleVideoCodecs[p.VideoOutCodec] = true
		}

		for _, o := range p.GetEncodedOutputs() {
			compatibleVideoCodecs = types.GetMapIntersection(compatibleVideoCodecs, types.CodecCompatibility[o.GetOutputType()])
			if len(compatibleVideoCodecs) == 0 {
				if p.AudioOutCodec == "" {
					return nil, nil, errors.ErrNoCompatibleCodec
				} else {
					// Return a more specific error if a codec was provided
					return nil, nil, errors.ErrIncompatible(o.GetOutputType(), p.VideoOutCodec)
				}
			}
		}
	}
	return compatibleAudioCodecs, compatibleVideoCodecs, nil
}

func (p *PipelineConfig) updateOutputType(compatibleAudioCodecs map[types.MimeType]bool, compatibleVideoCodecs map[types.MimeType]bool) error {
	o := p.GetFileConfig()
	if o == nil || o.GetOutputType() != types.OutputTypeUnknownFile {
		return nil
	}

	if !p.VideoEnabled {
		ot := types.GetOutputTypeCompatibleWithCodecs(types.AudioOnlyFileOutputTypes, compatibleAudioCodecs, nil)
		if ot == types.OutputTypeUnknownFile {
			return errors.ErrNoCompatibleFileOutputType
		}
		o.OutputType = ot
	} else if !p.AudioEnabled {
		ot := types.GetOutputTypeCompatibleWithCodecs(types.VideoOnlyFileOutputTypes, nil, compatibleVideoCodecs)
		if ot == types.OutputTypeUnknownFile {
			return errors.ErrNoCompatibleFileOutputType
		}
		o.OutputType = ot
	} else {
		ot := types.GetOutputTypeCompatibleWithCodecs(types.AudioVideoFileOutputTypes, compatibleAudioCodecs, compatibleVideoCodecs)
		if ot == types.OutputTypeUnknownFile {
			return errors.ErrNoCompatibleFileOutputType
		}
		o.OutputType = ot
	}

	identifier, replacements := p.getFilenameInfo()
	err := o.updateFilepath(p, identifier, replacements)
	if err != nil {
		return err
	}

	return nil
}

// used for sdk input source
func (p *PipelineConfig) UpdateInfoFromSDK(identifier string, replacements map[string]string, w, h uint32) error {
	for egressType, c := range p.Outputs {
		if len(c) == 0 {
			continue
		}
		switch egressType {
		case types.EgressTypeFile:
			return c[0].(*FileConfig).updateFilepath(p, identifier, replacements)

		case types.EgressTypeSegments:
			o := c[0].(*SegmentConfig)
			o.LocalDir = stringReplace(o.LocalDir, replacements)
			o.StorageDir = stringReplace(o.StorageDir, replacements)
			o.PlaylistFilename = stringReplace(o.PlaylistFilename, replacements)
			o.LivePlaylistFilename = stringReplace(o.LivePlaylistFilename, replacements)
			o.SegmentPrefix = stringReplace(o.SegmentPrefix, replacements)
			o.SegmentsInfo.PlaylistName = stringReplace(o.SegmentsInfo.PlaylistName, replacements)
			o.SegmentsInfo.LivePlaylistName = stringReplace(o.SegmentsInfo.LivePlaylistName, replacements)

		case types.EgressTypeImages:
			for _, ci := range c {
				o := ci.(*ImageConfig)
				o.LocalDir = stringReplace(o.LocalDir, replacements)
				o.StorageDir = stringReplace(o.StorageDir, replacements)
				o.ImagePrefix = stringReplace(o.ImagePrefix, replacements)
				if o.Width == 0 {
					if w != 0 {
						o.Width = int32(w)
					} else {
						o.Width = p.VideoConfig.Width
					}
				}
				if o.Height == 0 {
					if h != 0 {
						o.Height = int32(h)
					} else {
						o.Height = p.VideoConfig.Height
					}
				}
			}
		}
	}

	return nil
}

func (p *PipelineConfig) ValidateUrl(rawUrl string, outputType types.OutputType) (string, string, error) {
	parsed, err := url.Parse(rawUrl)
	if err != nil {
		return "", "", errors.ErrInvalidUrl(rawUrl, err.Error())
	}

	switch outputType {
	case types.OutputTypeRTMP:
		if parsed.Scheme == "mux" {
			rawUrl = fmt.Sprintf("rtmps://global-live.mux.com:443/app/%s", parsed.Host)
		}

		redacted, ok := utils.RedactStreamKey(rawUrl)
		if !ok {
			return "", "", errors.ErrInvalidUrl(rawUrl, "rtmp urls must be of format rtmp(s)://{host}(/{path})/{app}/{stream_key}( live=1)")
		}
		return rawUrl, redacted, nil

	case types.OutputTypeRaw:
		if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
			return "", "", errors.ErrInvalidUrl(rawUrl, "invalid scheme")
		}
		return rawUrl, rawUrl, nil

	default:
		return "", "", errors.ErrInvalidInput("stream output type")
	}
}

func (p *PipelineConfig) GetEncodedOutputs() []OutputConfig {
	ret := make([]OutputConfig, 0)

	for _, k := range []types.EgressType{types.EgressTypeFile, types.EgressTypeSegments, types.EgressTypeStream, types.EgressTypeWebsocket} {
		ret = append(ret, p.Outputs[k]...)
	}

	return ret
}

func stringReplace(s string, replacements map[string]string) string {
	for template, value := range replacements {
		s = strings.Replace(s, template, value, -1)
	}
	return s
}

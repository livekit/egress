package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/tracer"
)

type PipelineConfig struct {
	BaseConfig `yaml:",inline"`

	HandlerID string    `yaml:"handler_id"`
	TmpDir    string    `yaml:"tmp_dir"`
	MutedChan chan bool `yaml:"-"`

	SourceParams        `yaml:"-"`
	AudioParams         `yaml:"-"`
	VideoParams         `yaml:"-"`
	StreamParams        `yaml:"-"`
	FileParams          `yaml:"-"`
	SegmentedFileParams `yaml:"-"`
	UploadParams        `yaml:"-"`
	types.EgressType    `yaml:"-"`
	types.OutputType    `yaml:"-"`

	GstReady chan struct{}       `yaml:"-"`
	Info     *livekit.EgressInfo `yaml:"-"`
}

type SourceParams struct {
	// source
	Token string

	// web source
	Display    string
	Layout     string
	CustomBase string
	WebUrl     string

	// sdk source
	TrackID             string
	TrackSource         string
	TrackKind           string
	AudioTrackID        string
	VideoTrackID        string
	ParticipantIdentity string
}

type AudioParams struct {
	AudioEnabled   bool
	AudioCodec     types.MimeType
	AudioBitrate   int32
	AudioFrequency int32
}

type VideoParams struct {
	VideoEnabled bool
	VideoCodec   types.MimeType
	VideoProfile types.Profile
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
	FileInfo        *livekit.FileInfo
	LocalFilepath   string
	StorageFilepath string
}

type SegmentedFileParams struct {
	SegmentsInfo      *livekit.SegmentsInfo
	LocalFilePrefix   string
	StoragePathPrefix string
	PlaylistFilename  string
	SegmentDuration   int
}

type UploadParams struct {
	UploadConfig    interface{}
	DisableManifest bool
}

func NewPipelineConfig(confString string, req *livekit.StartEgressRequest) (*PipelineConfig, error) {
	p := &PipelineConfig{
		BaseConfig: BaseConfig{},
		GstReady:   make(chan struct{}),
	}

	if err := yaml.Unmarshal([]byte(confString), p); err != nil {
		return nil, errors.ErrCouldNotParseConfig(err)
	}

	if err := p.initLogger(
		"nodeID", p.NodeID,
		"handlerID", p.HandlerID,
		"egressID", req.EgressId,
	); err != nil {
		return nil, err
	}

	return p, p.Update(req)
}

func GetValidatedPipelineConfig(conf *ServiceConfig, req *livekit.StartEgressRequest) (*PipelineConfig, error) {
	_, span := tracer.Start(context.Background(), "config.GetValidatedPipelineConfig")
	defer span.End()

	p := &PipelineConfig{
		BaseConfig: conf.BaseConfig,
	}

	return p, p.Update(req)
}

func (p *PipelineConfig) Update(request *livekit.StartEgressRequest) error {
	// start with defaults
	p.Info = &livekit.EgressInfo{
		EgressId: request.EgressId,
		RoomId:   request.RoomId,
		Status:   livekit.EgressStatus_EGRESS_STARTING,
	}
	p.AudioParams = AudioParams{
		AudioBitrate:   128,
		AudioFrequency: 44100,
	}
	p.VideoParams = VideoParams{
		VideoProfile: types.ProfileMain,
		Width:        1920,
		Height:       1080,
		Depth:        24,
		Framerate:    30,
		VideoBitrate: 4500,
	}

	p.updateUploadConfig(request)
	connectionInfoRequired := true

	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		p.Info.Request = &livekit.EgressInfo_RoomComposite{RoomComposite: req.RoomComposite}
		p.Info.RoomName = req.RoomComposite.RoomName
		p.Layout = req.RoomComposite.Layout
		if req.RoomComposite.CustomBaseUrl != "" {
			p.TemplateBase = req.RoomComposite.CustomBaseUrl
		}
		p.AudioEnabled = !req.RoomComposite.VideoOnly
		p.VideoEnabled = !req.RoomComposite.AudioOnly
		if !p.AudioEnabled && !p.VideoEnabled {
			return errors.ErrInvalidInput("AudioOnly and VideoOnly")
		}

		// encoding options
		switch opts := req.RoomComposite.Options.(type) {
		case *livekit.RoomCompositeEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.RoomCompositeEgressRequest_Advanced:
			p.applyAdvanced(opts.Advanced)
		}

		// output params
		if err := p.updateEncodedOutput(req.RoomComposite); err != nil {
			return err
		}

	case *livekit.StartEgressRequest_Web:
		p.Info.Request = &livekit.EgressInfo_Web{Web: req.Web}
		connectionInfoRequired = false
		p.WebUrl = req.Web.Url
		if p.WebUrl == "" {
			return errors.ErrInvalidInput("url")
		}
		p.AudioEnabled = !req.Web.VideoOnly
		p.VideoEnabled = !req.Web.AudioOnly
		if !p.AudioEnabled && !p.VideoEnabled {
			return errors.ErrInvalidInput("AudioOnly and VideoOnly")
		}

		// encoding options
		switch opts := req.Web.Options.(type) {
		case *livekit.WebEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.WebEgressRequest_Advanced:
			p.applyAdvanced(opts.Advanced)
		}

		// output params
		if err := p.updateEncodedOutput(req.Web); err != nil {
			return err
		}

	case *livekit.StartEgressRequest_TrackComposite:
		p.Info.Request = &livekit.EgressInfo_TrackComposite{TrackComposite: req.TrackComposite}
		p.Info.RoomName = req.TrackComposite.RoomName
		p.AudioTrackID = req.TrackComposite.AudioTrackId
		p.VideoTrackID = req.TrackComposite.VideoTrackId
		p.AudioEnabled = p.AudioTrackID != ""
		p.VideoEnabled = p.VideoTrackID != ""
		if !p.AudioEnabled && !p.VideoEnabled {
			return errors.ErrInvalidInput("TrackIDs")
		}

		// encoding options
		switch opts := req.TrackComposite.Options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.TrackCompositeEgressRequest_Advanced:
			p.applyAdvanced(opts.Advanced)
		}

		// output params
		if err := p.updateEncodedOutput(req.TrackComposite); err != nil {
			return err
		}

	case *livekit.StartEgressRequest_Track:
		p.Info.Request = &livekit.EgressInfo_Track{Track: req.Track}
		p.Info.RoomName = req.Track.RoomName
		p.TrackID = req.Track.TrackId
		if p.TrackID == "" {
			return errors.ErrInvalidInput("TrackID")
		}

		// output params
		switch o := req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_File:
			p.DisableManifest = o.File.DisableManifest
			if err := p.updateFileParams(o.File.Filepath); err != nil {
				return err
			}
		case *livekit.TrackEgressRequest_WebsocketUrl:
			if err := p.updateStreamParams(types.OutputTypeRaw, []string{o.WebsocketUrl}); err != nil {
				return err
			}

		default:
			return errors.ErrInvalidInput("output")
		}

	default:
		return errors.ErrInvalidInput("request")
	}

	// connection info
	if connectionInfoRequired {
		if p.Info.RoomName == "" {
			return errors.ErrInvalidInput("RoomName")
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

	// codec compatibilities
	if p.OutputType != "" {
		// check audio codec
		if p.AudioEnabled {
			if p.AudioCodec == "" {
				p.AudioCodec = types.DefaultAudioCodecs[p.OutputType]
			} else if !types.CodecCompatibility[p.OutputType][p.AudioCodec] {
				return errors.ErrIncompatible(p.OutputType, p.AudioCodec)
			}
		}

		// check video codec
		if p.VideoEnabled {
			if p.VideoCodec == "" {
				p.VideoCodec = types.DefaultVideoCodecs[p.OutputType]
			} else if !types.CodecCompatibility[p.OutputType][p.VideoCodec] {
				return errors.ErrIncompatible(p.OutputType, p.VideoCodec)
			}
		}
	}

	return nil
}

func (p *PipelineConfig) updateEncodedOutput(req interface {
	GetFile() *livekit.EncodedFileOutput
	GetStream() *livekit.StreamOutput
	GetSegments() *livekit.SegmentedFileOutput
}) error {
	if file := req.GetFile(); file != nil {
		p.DisableManifest = file.DisableManifest
		if file.FileType != livekit.EncodedFileType_DEFAULT_FILETYPE {
			p.updateOutputType(file.FileType)
		}
		return p.updateFileParams(file.Filepath)
	}

	if stream := req.GetStream(); stream != nil {
		return p.updateStreamParams(types.OutputTypeRTMP, stream.Urls)
	}

	if segments := req.GetSegments(); segments != nil {
		p.DisableManifest = segments.DisableManifest
		p.updateOutputType(segments.Protocol)
		return p.updateSegmentsParams(segments.FilenamePrefix, segments.PlaylistName, segments.SegmentDuration)
	}

	return errors.ErrInvalidInput("output")
}

func (p *PipelineConfig) updateOutputType(fileType interface{}) {
	switch f := fileType.(type) {
	case livekit.EncodedFileType:
		switch f {
		case livekit.EncodedFileType_DEFAULT_FILETYPE:
			if !p.VideoEnabled && p.AudioCodec != types.MimeTypeAAC {
				p.OutputType = types.OutputTypeOGG
			} else {
				p.OutputType = types.OutputTypeMP4
			}
		case livekit.EncodedFileType_MP4:
			p.OutputType = types.OutputTypeMP4
		case livekit.EncodedFileType_OGG:
			p.OutputType = types.OutputTypeOGG
		}

	case livekit.SegmentedFileProtocol:
		switch f {
		case livekit.SegmentedFileProtocol_DEFAULT_SEGMENTED_FILE_PROTOCOL,
			livekit.SegmentedFileProtocol_HLS_PROTOCOL:
			p.OutputType = types.OutputTypeHLS
		}
	}
}

func (p *PipelineConfig) updateFileParams(storageFilepath string) error {
	p.EgressType = types.EgressTypeFile
	p.StorageFilepath = storageFilepath
	p.FileInfo = &livekit.FileInfo{}
	p.Info.Result = &livekit.EgressInfo_File{File: p.FileInfo}

	// filename
	identifier, replacements := p.getFilenameInfo()
	if p.OutputType != "" {
		err := p.updateFilepath(identifier, replacements)
		if err != nil {
			return err
		}
	} else {
		p.StorageFilepath = stringReplace(p.StorageFilepath, replacements)
	}

	return nil
}

func (p *PipelineConfig) updateFilepath(identifier string, replacements map[string]string) error {
	p.StorageFilepath = stringReplace(p.StorageFilepath, replacements)

	// get file extension
	ext := types.FileExtensionForOutputType[p.OutputType]

	if p.StorageFilepath == "" || strings.HasSuffix(p.StorageFilepath, "/") {
		// generate filepath
		p.StorageFilepath = fmt.Sprintf("%s%s-%s%s", p.StorageFilepath, identifier, time.Now().Format("2006-01-02T150405"), ext)
	} else if !strings.HasSuffix(p.StorageFilepath, string(ext)) {
		// check for existing (incorrect) extension
		extIdx := strings.LastIndex(p.StorageFilepath, ".")
		if extIdx > 0 {
			existingExt := types.FileExtension(p.StorageFilepath[extIdx:])
			if _, ok := types.FileExtensions[existingExt]; ok {
				p.StorageFilepath = p.StorageFilepath[:extIdx]
			}
		}
		// add file extension
		p.StorageFilepath = p.StorageFilepath + string(ext)
	}

	// update filename
	p.FileInfo.Filename = p.StorageFilepath

	// get local filepath
	dir, filename := path.Split(p.StorageFilepath)
	if p.UploadConfig == nil {
		if dir != "" {
			// create local directory
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}
		// write directly to requested location
		p.LocalFilepath = p.StorageFilepath
	} else {
		// prepend the configuration base directory and the egress Id
		tempDir := path.Join(p.LocalOutputDirectory, p.Info.EgressId)

		// create temporary directory
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return err
		}

		// write to tmp dir
		p.LocalFilepath = path.Join(tempDir, filename)
	}

	return nil
}

func (p *PipelineConfig) updateStreamParams(outputType types.OutputType, urls []string) error {
	p.OutputType = outputType

	switch p.OutputType {
	case types.OutputTypeRTMP:
		p.EgressType = types.EgressTypeStream
		p.AudioCodec = types.MimeTypeAAC
		p.VideoCodec = types.MimeTypeH264
		p.StreamUrls = urls

	case types.OutputTypeRaw:
		p.EgressType = types.EgressTypeWebsocket
		p.AudioCodec = types.MimeTypeRaw
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

func (p *PipelineConfig) updateSegmentsParams(filePrefix string, playlistFilename string, segmentDuration uint32) error {
	p.EgressType = types.EgressTypeSegmentedFile
	p.LocalFilePrefix = filePrefix
	p.PlaylistFilename = playlistFilename
	p.SegmentDuration = int(segmentDuration)
	if p.SegmentDuration == 0 {
		p.SegmentDuration = 6
	}
	p.SegmentsInfo = &livekit.SegmentsInfo{}
	p.Info.Result = &livekit.EgressInfo_Segments{Segments: p.SegmentsInfo}

	// filename
	identifier, replacements := p.getFilenameInfo()
	err := p.updatePrefixAndPlaylist(identifier, replacements)
	if err != nil {
		return err
	}

	return nil
}

func (p *PipelineConfig) getFilenameInfo() (string, map[string]string) {
	if p.Info.RoomName != "" {
		return p.Info.RoomName, map[string]string{
			"{room_name}": p.Info.RoomName,
			"{room_id}":   p.Info.RoomId,
			"{time}":      time.Now().Format("2006-01-02T150405"),
		}
	}

	return "web", map[string]string{
		"{time}": time.Now().Format("2006-01-02T150405"),
	}
}

func (p *PipelineConfig) updatePrefixAndPlaylist(identifier string, replacements map[string]string) error {
	p.LocalFilePrefix = stringReplace(p.LocalFilePrefix, replacements)
	p.PlaylistFilename = stringReplace(p.PlaylistFilename, replacements)

	ext := types.FileExtensionForOutputType[p.OutputType]

	if p.LocalFilePrefix == "" || strings.HasSuffix(p.LocalFilePrefix, "/") {
		p.LocalFilePrefix = fmt.Sprintf("%s%s-%s", p.LocalFilePrefix, identifier, time.Now().Format("2006-01-02T150405"))
	}

	// Playlist path is relative to file prefix. Only keep actual filename if a full path is given
	_, p.PlaylistFilename = path.Split(p.PlaylistFilename)
	if p.PlaylistFilename == "" {
		p.PlaylistFilename = fmt.Sprintf("playlist-%s%s", identifier, ext)
	}

	var filePrefix string
	p.StoragePathPrefix, filePrefix = path.Split(p.LocalFilePrefix)
	if p.UploadConfig == nil {
		if p.StoragePathPrefix != "" {
			if err := os.MkdirAll(p.StoragePathPrefix, 0755); err != nil {
				return err
			}
		}
		p.PlaylistFilename = path.Join(p.StoragePathPrefix, p.PlaylistFilename)
	} else {
		// Prepend the configuration base directory and the egress Id
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		tmpDir := path.Join(p.LocalOutputDirectory, p.Info.EgressId)
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			return err
		}

		p.PlaylistFilename = path.Join(tmpDir, p.PlaylistFilename)
		p.LocalFilePrefix = path.Join(tmpDir, filePrefix)
	}

	p.SegmentsInfo.PlaylistName = p.GetStorageFilepath(p.PlaylistFilename)
	return nil
}

// used for sdk input source
func (p *PipelineConfig) UpdateFileInfoFromSDK(fileIdentifier string, replacements map[string]string) error {
	if p.OutputType == "" {
		if !p.VideoEnabled {
			// audio input is always opus
			p.OutputType = types.OutputTypeOGG
		} else {
			p.OutputType = types.OutputTypeMP4
		}
	}

	return p.updateFilepath(fileIdentifier, replacements)
}

func (p *PipelineConfig) UpdatePlaylistNamesFromSDK(replacements map[string]string) {
	p.LocalFilePrefix = stringReplace(p.LocalFilePrefix, replacements)
	p.PlaylistFilename = stringReplace(p.PlaylistFilename, replacements)
	p.StoragePathPrefix = stringReplace(p.StoragePathPrefix, replacements)
	p.SegmentsInfo.PlaylistName = stringReplace(p.SegmentsInfo.PlaylistName, replacements)
}

func (p *PipelineConfig) VerifyUrl(url string) error {
	var protocol, prefix string

	switch p.OutputType {
	case types.OutputTypeRTMP:
		protocol = "rtmp"
		prefix = "rtmp"
	case types.OutputTypeRaw:
		protocol = "websocket"
		prefix = "ws"
	}

	if !strings.HasPrefix(url, prefix+"://") && !strings.HasPrefix(url, prefix+"s://") {
		return errors.ErrInvalidUrl(url, protocol)
	}

	return nil
}

func (p *PipelineConfig) GetSegmentOutputType() types.OutputType {
	switch p.OutputType {
	case types.OutputTypeHLS:
		// HLS is always mpeg ts for now. We may implement fmp4 in the future
		return types.OutputTypeTS
	default:
		return p.OutputType
	}
}

func (p *SegmentedFileParams) GetStorageFilepath(filename string) string {
	// Remove any path prepended to the filename
	_, filename = path.Split(filename)

	return path.Join(p.StoragePathPrefix, filename)
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

func (p *PipelineConfig) GetManifest() ([]byte, error) {
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
	if p.SegmentsInfo != nil {
		manifest.SegmentCount = p.SegmentsInfo.SegmentCount
	}
	return json.Marshal(manifest)
}

func stringReplace(s string, replacements map[string]string) string {
	for template, value := range replacements {
		s = strings.Replace(s, template, value, -1)
	}
	return s
}

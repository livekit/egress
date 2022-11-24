package config

import (
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
)

type PipelineConfig struct {
	*BaseConfig `yaml:",inline"`

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

func NewPipelineConfig(confString, egressID string) (*PipelineConfig, error) {
	conf := &PipelineConfig{
		BaseConfig: &BaseConfig{},
		GstReady:   make(chan struct{}),
	}

	if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
		return nil, errors.ErrCouldNotParseConfig(err)
	}
	if err := conf.initLogger(
		"nodeID", conf.NodeID,
		"handlerID", conf.HandlerID,
		"egressID", egressID,
	); err != nil {
		return nil, err
	}
	conf.updateUploadConfig()
	return conf, nil
}

func PipelineConfigFromService(sc *ServiceConfig) *PipelineConfig {
	conf := &PipelineConfig{
		BaseConfig: sc.BaseConfig,
	}
	conf.updateUploadConfig()
	return conf
}

func (c *PipelineConfig) updateUploadConfig() {
	if c.S3 != nil {
		c.UploadConfig = c.S3.ToS3Upload()
	} else if c.Azure != nil {
		c.UploadConfig = c.Azure.ToAzureUpload()
	} else if c.GCP != nil {
		c.UploadConfig = c.GCP.ToGCPUpload()
	} else if c.AliOSS != nil {
		c.UploadConfig = c.AliOSS.ToAliOSSUpload()
	}
}

func ValidateRequest(conf *ServiceConfig, req *livekit.StartEgressRequest) (*livekit.EgressInfo, error) {
	c := PipelineConfigFromService(conf)
	err := c.Update(req)
	return c.Info, err
}

func (c *PipelineConfig) Update(request *livekit.StartEgressRequest) error {
	// start with defaults
	c.Info = &livekit.EgressInfo{
		EgressId: request.EgressId,
		RoomId:   request.RoomId,
		Status:   livekit.EgressStatus_EGRESS_STARTING,
	}
	c.AudioParams = AudioParams{
		AudioBitrate:   128,
		AudioFrequency: 44100,
	}
	c.VideoParams = VideoParams{
		VideoProfile: types.ProfileMain,
		Width:        1920,
		Height:       1080,
		Depth:        24,
		Framerate:    30,
		VideoBitrate: 4500,
	}

	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		c.Info.Request = &livekit.EgressInfo_RoomComposite{RoomComposite: req.RoomComposite}
		c.Info.RoomName = req.RoomComposite.RoomName
		if c.Info.RoomName == "" {
			return errors.ErrInvalidInput("RoomName")
		}

		// input params
		c.Layout = req.RoomComposite.Layout
		if req.RoomComposite.CustomBaseUrl != "" {
			c.TemplateBase = req.RoomComposite.CustomBaseUrl
		}
		c.AudioEnabled = !req.RoomComposite.VideoOnly
		c.VideoEnabled = !req.RoomComposite.AudioOnly
		if !c.AudioEnabled && !c.VideoEnabled {
			return errors.ErrInvalidInput("AudioOnly and VideoOnly")
		}

		// encoding options
		switch opts := req.RoomComposite.Options.(type) {
		case *livekit.RoomCompositeEgressRequest_Preset:
			c.applyPreset(opts.Preset)

		case *livekit.RoomCompositeEgressRequest_Advanced:
			c.applyAdvanced(opts.Advanced)
		}

		// output params
		switch o := req.RoomComposite.Output.(type) {
		case *livekit.RoomCompositeEgressRequest_File:
			c.DisableManifest = o.File.DisableManifest
			c.updateOutputType(o.File.FileType)
			if err := c.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return err
			}

		case *livekit.RoomCompositeEgressRequest_Stream:
			if err := c.updateStreamParams(types.OutputTypeRTMP, o.Stream.Urls); err != nil {
				return err
			}

		case *livekit.RoomCompositeEgressRequest_Segments:
			c.DisableManifest = o.Segments.DisableManifest
			c.updateOutputType(o.Segments.Protocol)
			if err := c.updateSegmentsParams(o.Segments.FilenamePrefix, o.Segments.PlaylistName, o.Segments.SegmentDuration, o.Segments.Output); err != nil {
				return err
			}

		default:
			return errors.ErrInvalidInput("output")
		}

	case *livekit.StartEgressRequest_Web:
		c.Info.Request = &livekit.EgressInfo_Web{Web: req.Web}

		// input params
		c.WebUrl = req.Web.Url
		if c.WebUrl == "" {
			return errors.ErrInvalidInput("url")
		}
		c.AudioEnabled = !req.Web.VideoOnly
		c.VideoEnabled = !req.Web.AudioOnly
		if !c.AudioEnabled && !c.VideoEnabled {
			return errors.ErrInvalidInput("AudioOnly and VideoOnly")
		}

		// encoding options
		switch opts := req.Web.Options.(type) {
		case *livekit.WebEgressRequest_Preset:
			c.applyPreset(opts.Preset)

		case *livekit.WebEgressRequest_Advanced:
			c.applyAdvanced(opts.Advanced)
		}

		// output params
		switch o := req.Web.Output.(type) {
		case *livekit.WebEgressRequest_File:
			c.DisableManifest = o.File.DisableManifest
			c.updateOutputType(o.File.FileType)
			if err := c.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return err
			}

		case *livekit.WebEgressRequest_Stream:
			if err := c.updateStreamParams(types.OutputTypeRTMP, o.Stream.Urls); err != nil {
				return err
			}

		case *livekit.WebEgressRequest_Segments:
			c.DisableManifest = o.Segments.DisableManifest
			c.updateOutputType(o.Segments.Protocol)
			if err := c.updateSegmentsParams(o.Segments.FilenamePrefix, o.Segments.PlaylistName, o.Segments.SegmentDuration, o.Segments.Output); err != nil {
				return err
			}

		default:
			return errors.ErrInvalidInput("output")
		}

	case *livekit.StartEgressRequest_TrackComposite:
		c.Info.Request = &livekit.EgressInfo_TrackComposite{TrackComposite: req.TrackComposite}
		c.Info.RoomName = req.TrackComposite.RoomName
		if c.Info.RoomName == "" {
			return errors.ErrInvalidInput("RoomName")
		}

		// encoding options
		switch opts := req.TrackComposite.Options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			c.applyPreset(opts.Preset)

		case *livekit.TrackCompositeEgressRequest_Advanced:
			c.applyAdvanced(opts.Advanced)
		}

		// input params
		c.AudioTrackID = req.TrackComposite.AudioTrackId
		c.VideoTrackID = req.TrackComposite.VideoTrackId
		c.AudioEnabled = c.AudioTrackID != ""
		c.VideoEnabled = c.VideoTrackID != ""
		if !c.AudioEnabled && !c.VideoEnabled {
			return errors.ErrInvalidInput("TrackIDs")
		}

		// output params
		switch o := req.TrackComposite.Output.(type) {
		case *livekit.TrackCompositeEgressRequest_File:
			c.DisableManifest = o.File.DisableManifest
			if o.File.FileType != livekit.EncodedFileType_DEFAULT_FILETYPE {
				c.updateOutputType(o.File.FileType)
			}
			if err := c.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return err
			}

		case *livekit.TrackCompositeEgressRequest_Stream:
			if err := c.updateStreamParams(types.OutputTypeRTMP, o.Stream.Urls); err != nil {
				return err
			}

		case *livekit.TrackCompositeEgressRequest_Segments:
			c.DisableManifest = o.Segments.DisableManifest
			c.updateOutputType(o.Segments.Protocol)
			if err := c.updateSegmentsParams(o.Segments.FilenamePrefix, o.Segments.PlaylistName, o.Segments.SegmentDuration, o.Segments.Output); err != nil {
				return err
			}

		default:
			return errors.ErrInvalidInput("output")
		}

	case *livekit.StartEgressRequest_Track:
		c.Info.Request = &livekit.EgressInfo_Track{Track: req.Track}
		c.Info.RoomName = req.Track.RoomName
		if c.Info.RoomName == "" {
			return errors.ErrInvalidInput("RoomName")
		}

		c.TrackID = req.Track.TrackId
		if c.TrackID == "" {
			return errors.ErrInvalidInput("TrackID")
		}

		// output params
		switch o := req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_File:
			c.DisableManifest = o.File.DisableManifest
			if err := c.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return err
			}
		case *livekit.TrackEgressRequest_WebsocketUrl:
			if err := c.updateStreamParams(types.OutputTypeRaw, []string{o.WebsocketUrl}); err != nil {
				return err
			}

		default:
			return errors.ErrInvalidInput("output")
		}

	default:
		return errors.ErrInvalidInput("request")
	}

	if c.Info.RoomName != "" {
		if err := c.updateConnectionInfo(request); err != nil {
			return err
		}
	}

	if c.OutputType != "" {
		if err := c.updateCodecs(); err != nil {
			return err
		}
	}

	return nil
}

// TODO: check performance of 1920x1920 display for portrait modes
func (c *PipelineConfig) applyPreset(preset livekit.EncodingOptionsPreset) {
	switch preset {
	case livekit.EncodingOptionsPreset_H264_720P_30:
		c.Width = 1280
		c.Height = 720
		c.VideoBitrate = 3000

	case livekit.EncodingOptionsPreset_H264_720P_60:
		c.Width = 1280
		c.Height = 720
		c.Framerate = 60

	case livekit.EncodingOptionsPreset_H264_1080P_30:
		// default

	case livekit.EncodingOptionsPreset_H264_1080P_60:
		c.Framerate = 60
		c.VideoBitrate = 6000

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_720P_30:
		c.Width = 603
		c.Height = 1072
		c.VideoBitrate = 3000

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_720P_60:
		c.Width = 603
		c.Height = 1072
		c.Framerate = 60

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_1080P_30:
		c.Width = 603
		c.Height = 1072

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_1080P_60:
		c.Width = 603
		c.Height = 1072
		c.Framerate = 60
		c.VideoBitrate = 6000
	}
}

func (c *PipelineConfig) applyAdvanced(advanced *livekit.EncodingOptions) {
	// audio
	switch advanced.AudioCodec {
	case livekit.AudioCodec_OPUS:
		c.AudioCodec = types.MimeTypeOpus
	case livekit.AudioCodec_AAC:
		c.AudioCodec = types.MimeTypeAAC
	}

	if advanced.AudioBitrate != 0 {
		c.AudioBitrate = advanced.AudioBitrate
	}
	if advanced.AudioFrequency != 0 {
		c.AudioFrequency = advanced.AudioFrequency
	}

	// video
	switch advanced.VideoCodec {
	case livekit.VideoCodec_H264_BASELINE:
		c.VideoCodec = types.MimeTypeH264
		c.VideoProfile = types.ProfileBaseline

	case livekit.VideoCodec_H264_MAIN:
		c.VideoCodec = types.MimeTypeH264

	case livekit.VideoCodec_H264_HIGH:
		c.VideoCodec = types.MimeTypeH264
		c.VideoProfile = types.ProfileHigh
	}

	if advanced.Width != 0 {

		c.Width = advanced.Width
	}
	if advanced.Height != 0 {
		c.Height = advanced.Height
	}
	if advanced.Depth != 0 {
		c.Depth = advanced.Depth
	}
	if advanced.Framerate != 0 {
		c.Framerate = advanced.Framerate
	}
	if advanced.VideoBitrate != 0 {
		c.VideoBitrate = advanced.VideoBitrate
	}
}

func (c *PipelineConfig) updateOutputType(fileType interface{}) {
	switch f := fileType.(type) {
	case livekit.EncodedFileType:
		switch f {
		case livekit.EncodedFileType_DEFAULT_FILETYPE:
			if !c.VideoEnabled && c.AudioCodec != types.MimeTypeAAC {
				c.OutputType = types.OutputTypeOGG
			} else {
				c.OutputType = types.OutputTypeMP4
			}
		case livekit.EncodedFileType_MP4:
			c.OutputType = types.OutputTypeMP4
		case livekit.EncodedFileType_OGG:
			c.OutputType = types.OutputTypeOGG
		}

	case livekit.SegmentedFileProtocol:
		switch f {
		case livekit.SegmentedFileProtocol_DEFAULT_SEGMENTED_FILE_PROTOCOL,
			livekit.SegmentedFileProtocol_HLS_PROTOCOL:
			c.OutputType = types.OutputTypeHLS
		}
	}
}

func (c *PipelineConfig) updateFileParams(storageFilepath string, output interface{}) error {
	c.EgressType = types.EgressTypeFile
	c.StorageFilepath = storageFilepath
	c.FileInfo = &livekit.FileInfo{}
	c.Info.Result = &livekit.EgressInfo_File{File: c.FileInfo}

	// output location
	switch o := output.(type) {
	case *livekit.EncodedFileOutput_S3:
		c.UploadConfig = o.S3
	case *livekit.EncodedFileOutput_Azure:
		c.UploadConfig = o.Azure
	case *livekit.EncodedFileOutput_Gcp:
		c.UploadConfig = o.Gcp
	case *livekit.EncodedFileOutput_AliOSS:
		c.UploadConfig = o.AliOSS
	case *livekit.DirectFileOutput_S3:
		c.UploadConfig = o.S3
	case *livekit.DirectFileOutput_Azure:
		c.UploadConfig = o.Azure
	case *livekit.DirectFileOutput_Gcp:
		c.UploadConfig = o.Gcp
	case *livekit.DirectFileOutput_AliOSS:
		c.UploadConfig = o.AliOSS
	}

	// filename
	identifier, replacements := c.getFilenameInfo()
	if c.OutputType != "" {
		err := c.updateFilepath(identifier, replacements)
		if err != nil {
			return err
		}
	} else {
		c.StorageFilepath = stringReplace(c.StorageFilepath, replacements)
	}

	return nil
}

func (c *PipelineConfig) updateStreamParams(outputType types.OutputType, urls []string) error {
	c.OutputType = outputType

	switch c.OutputType {
	case types.OutputTypeRTMP:
		c.EgressType = types.EgressTypeStream
		c.AudioCodec = types.MimeTypeAAC
		c.VideoCodec = types.MimeTypeH264
		c.StreamUrls = urls

	case types.OutputTypeRaw:
		c.EgressType = types.EgressTypeWebsocket
		c.AudioCodec = types.MimeTypeRaw
		c.WebsocketUrl = urls[0]
		c.MutedChan = make(chan bool, 1)
	}

	c.StreamInfo = make(map[string]*livekit.StreamInfo)
	var streamInfoList []*livekit.StreamInfo
	for _, url := range urls {
		if err := c.VerifyUrl(url); err != nil {
			return err
		}

		info := &livekit.StreamInfo{Url: url}
		c.StreamInfo[url] = info
		streamInfoList = append(streamInfoList, info)
	}

	c.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
	return nil
}

func (c *PipelineConfig) updateSegmentsParams(filePrefix string, playlistFilename string, segmentDuration uint32, output interface{}) error {
	c.EgressType = types.EgressTypeSegmentedFile
	c.LocalFilePrefix = filePrefix
	c.PlaylistFilename = playlistFilename
	c.SegmentDuration = int(segmentDuration)
	if c.SegmentDuration == 0 {
		c.SegmentDuration = 6
	}
	c.SegmentsInfo = &livekit.SegmentsInfo{}
	c.Info.Result = &livekit.EgressInfo_Segments{Segments: c.SegmentsInfo}

	// output location
	switch o := output.(type) {
	case *livekit.SegmentedFileOutput_S3:
		c.UploadConfig = o.S3
	case *livekit.SegmentedFileOutput_Azure:
		c.UploadConfig = o.Azure
	case *livekit.SegmentedFileOutput_Gcp:
		c.UploadConfig = o.Gcp
	case *livekit.SegmentedFileOutput_AliOSS:
		c.UploadConfig = o.AliOSS
	}

	// filename
	identifier, replacements := c.getFilenameInfo()
	err := c.UpdatePrefixAndPlaylist(identifier, replacements)
	if err != nil {
		return err
	}

	return nil
}

func (c *PipelineConfig) getFilenameInfo() (string, map[string]string) {
	if c.Info.RoomName != "" {
		return c.Info.RoomName, map[string]string{
			"{room_name}": c.Info.RoomName,
			"{room_id}":   c.Info.RoomId,
			"{time}":      time.Now().Format("2006-01-02T150405"),
		}
	}

	return "web", map[string]string{
		"{time}": time.Now().Format("2006-01-02T150405"),
	}
}

func (c *PipelineConfig) updateConnectionInfo(request *livekit.StartEgressRequest) error {
	// token
	if request.Token != "" {
		c.Token = request.Token
	} else if c.ApiKey != "" && c.ApiSecret != "" {
		token, err := egress.BuildEgressToken(c.Info.EgressId, c.ApiKey, c.ApiSecret, c.Info.RoomName)
		if err != nil {
			return err
		}
		c.Token = token
	} else {
		return errors.ErrInvalidInput("token or api key/secret")
	}

	// url
	if request.WsUrl != "" {
		c.WsUrl = request.WsUrl
	} else if c.WsUrl == "" {
		return errors.ErrInvalidInput("ws_url")
	}

	return nil
}

// used for web input source
func (c *PipelineConfig) updateCodecs() error {
	// check audio codec
	if c.AudioEnabled {
		if c.AudioCodec == "" {
			c.AudioCodec = types.DefaultAudioCodecs[c.OutputType]
		} else if !types.CodecCompatibility[c.OutputType][c.AudioCodec] {
			return errors.ErrIncompatible(c.OutputType, c.AudioCodec)
		}
	}

	// check video codec
	if c.VideoEnabled {
		if c.VideoCodec == "" {
			c.VideoCodec = types.DefaultVideoCodecs[c.OutputType]
		} else if !types.CodecCompatibility[c.OutputType][c.VideoCodec] {
			return errors.ErrIncompatible(c.OutputType, c.VideoCodec)
		}
	}

	return nil
}

// used for sdk input source
func (c *PipelineConfig) UpdateFileInfoFromSDK(fileIdentifier string, replacements map[string]string) error {
	if c.OutputType == "" {
		if !c.VideoEnabled {
			// audio input is always opus
			c.OutputType = types.OutputTypeOGG
		} else {
			c.OutputType = types.OutputTypeMP4
		}
	}

	// check audio codec
	if c.AudioEnabled && !types.CodecCompatibility[c.OutputType][c.AudioCodec] {
		return errors.ErrIncompatible(c.OutputType, c.AudioCodec)
	}

	// check video codec
	if c.VideoEnabled && !types.CodecCompatibility[c.OutputType][c.VideoCodec] {
		return errors.ErrIncompatible(c.OutputType, c.VideoCodec)
	}

	return c.updateFilepath(fileIdentifier, replacements)
}

func (c *PipelineConfig) updateFilepath(identifier string, replacements map[string]string) error {
	c.StorageFilepath = stringReplace(c.StorageFilepath, replacements)

	// get file extension
	ext := types.FileExtensionForOutputType[c.OutputType]

	if c.StorageFilepath == "" || strings.HasSuffix(c.StorageFilepath, "/") {
		// generate filepath
		c.StorageFilepath = fmt.Sprintf("%s%s-%s%s", c.StorageFilepath, identifier, time.Now().Format("2006-01-02T150405"), ext)
	} else if !strings.HasSuffix(c.StorageFilepath, string(ext)) {
		// check for existing (incorrect) extension
		extIdx := strings.LastIndex(c.StorageFilepath, ".")
		if extIdx > 0 {
			existingExt := types.FileExtension(c.StorageFilepath[extIdx:])
			if _, ok := types.FileExtensions[existingExt]; ok {
				c.StorageFilepath = c.StorageFilepath[:extIdx]
			}
		}
		// add file extension
		c.StorageFilepath = c.StorageFilepath + string(ext)
	}

	// update filename
	c.FileInfo.Filename = c.StorageFilepath

	// get local filepath
	dir, filename := path.Split(c.StorageFilepath)
	if c.UploadConfig == nil {
		if dir != "" {
			// create local directory
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}
		// write directly to requested location
		c.LocalFilepath = c.StorageFilepath
	} else {
		// prepend the configuration base directory and the egress Id
		tempDir := path.Join(c.LocalOutputDirectory, c.Info.EgressId)

		// create temporary directory
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return err
		}

		// write to tmp dir
		c.LocalFilepath = path.Join(tempDir, filename)
	}

	return nil
}

func (c *PipelineConfig) UpdatePrefixAndPlaylist(identifier string, replacements map[string]string) error {
	c.LocalFilePrefix = stringReplace(c.LocalFilePrefix, replacements)
	c.PlaylistFilename = stringReplace(c.PlaylistFilename, replacements)

	ext := types.FileExtensionForOutputType[c.OutputType]

	if c.LocalFilePrefix == "" || strings.HasSuffix(c.LocalFilePrefix, "/") {
		c.LocalFilePrefix = fmt.Sprintf("%s%s-%s", c.LocalFilePrefix, identifier, time.Now().Format("2006-01-02T150405"))
	}

	// Playlist path is relative to file prefix. Only keep actual filename if a full path is given
	_, c.PlaylistFilename = path.Split(c.PlaylistFilename)
	if c.PlaylistFilename == "" {
		c.PlaylistFilename = fmt.Sprintf("playlist-%s%s", identifier, ext)
	}

	var filePrefix string
	c.StoragePathPrefix, filePrefix = path.Split(c.LocalFilePrefix)
	if c.UploadConfig == nil {
		if c.StoragePathPrefix != "" {
			if err := os.MkdirAll(c.StoragePathPrefix, 0755); err != nil {
				return err
			}
		}
		c.PlaylistFilename = path.Join(c.StoragePathPrefix, c.PlaylistFilename)
	} else {
		// Prepend the configuration base directory and the egress Id
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		tmpDir := path.Join(c.LocalOutputDirectory, c.Info.EgressId)
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			return err
		}

		c.PlaylistFilename = path.Join(tmpDir, c.PlaylistFilename)
		c.LocalFilePrefix = path.Join(tmpDir, filePrefix)
	}

	c.SegmentsInfo.PlaylistName = c.GetStorageFilepath(c.PlaylistFilename)
	return nil
}

func (c *PipelineConfig) UpdatePlaylistNamesFromSDK(replacements map[string]string) {
	c.LocalFilePrefix = stringReplace(c.LocalFilePrefix, replacements)
	c.PlaylistFilename = stringReplace(c.PlaylistFilename, replacements)
	c.StoragePathPrefix = stringReplace(c.StoragePathPrefix, replacements)
	c.SegmentsInfo.PlaylistName = stringReplace(c.SegmentsInfo.PlaylistName, replacements)
}

func (c *PipelineConfig) VerifyUrl(url string) error {
	var protocol, prefix string

	switch c.OutputType {
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

func (c *PipelineConfig) GetSegmentOutputType() types.OutputType {
	switch c.OutputType {
	case types.OutputTypeHLS:
		// HLS is always mpeg ts for now. We may implement fmp4 in the future
		return types.OutputTypeTS
	default:
		return c.OutputType
	}
}

func (p *SegmentedFileParams) GetStorageFilepath(filename string) string {
	// Remove any path prepended to the filename
	_, filename = path.Split(filename)

	return path.Join(p.StoragePathPrefix, filename)
}

func (c *PipelineConfig) GetSessionTimeout() time.Duration {
	switch c.EgressType {
	case types.EgressTypeFile:
		return c.FileOutputMaxDuration
	case types.EgressTypeStream, types.EgressTypeWebsocket:
		return c.StreamOutputMaxDuration
	case types.EgressTypeSegmentedFile:
		return c.SegmentOutputMaxDuration
	}

	return 0
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

func (c *PipelineConfig) GetManifest() ([]byte, error) {
	manifest := Manifest{
		EgressID:          c.Info.EgressId,
		RoomID:            c.Info.RoomId,
		RoomName:          c.Info.RoomName,
		Url:               c.WebUrl,
		StartedAt:         c.Info.StartedAt,
		EndedAt:           c.Info.EndedAt,
		PublisherIdentity: c.ParticipantIdentity,
		TrackID:           c.TrackID,
		TrackKind:         c.TrackKind,
		TrackSource:       c.TrackSource,
		AudioTrackID:      c.AudioTrackID,
		VideoTrackID:      c.VideoTrackID,
	}
	if c.SegmentsInfo != nil {
		manifest.SegmentCount = c.SegmentsInfo.SegmentCount
	}
	return json.Marshal(manifest)
}

func stringReplace(s string, replacements map[string]string) string {
	for template, value := range replacements {
		s = strings.Replace(s, template, value, -1)
	}
	return s
}

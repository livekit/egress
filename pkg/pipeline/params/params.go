package params

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path"
	"strings"
	"time"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type Params struct {
	conf *config.Config

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

	UploadParams
}

type SourceParams struct {
	// source
	Token        string
	LKUrl        string
	TemplateBase string

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
		conf:   conf,
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
	}

	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		p.Info.Request = &livekit.EgressInfo_RoomComposite{RoomComposite: req.RoomComposite}
		p.Info.RoomName = req.RoomComposite.RoomName
		if p.Info.RoomName == "" {
			err = errors.ErrInvalidInput("RoomName")
			return
		}

		// input params
		p.Layout = req.RoomComposite.Layout
		p.Display = fmt.Sprintf(":%d", 10+rand.Intn(2147483637))
		if req.RoomComposite.CustomBaseUrl != "" {
			p.TemplateBase = req.RoomComposite.CustomBaseUrl
		} else {
			p.TemplateBase = conf.TemplateBase
		}
		p.AudioEnabled = !req.RoomComposite.VideoOnly
		p.VideoEnabled = !req.RoomComposite.AudioOnly
		if !p.AudioEnabled && !p.VideoEnabled {
			err = errors.ErrInvalidInput("AudioOnly and VideoOnly")
			return
		}

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
			p.DisableManifest = o.File.DisableManifest
			p.updateOutputType(o.File.FileType)
			if err = p.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return
			}

		case *livekit.RoomCompositeEgressRequest_Stream:
			if err = p.updateStreamParams(OutputTypeRTMP, o.Stream.Urls); err != nil {
				return
			}

		case *livekit.RoomCompositeEgressRequest_Segments:
			p.DisableManifest = o.Segments.DisableManifest
			p.updateOutputType(o.Segments.Protocol)
			if err = p.updateSegmentsParams(o.Segments.FilenamePrefix, o.Segments.PlaylistName, o.Segments.SegmentDuration, o.Segments.Output); err != nil {
				return
			}

		default:
			err = errors.ErrInvalidInput("output")
			return
		}

	case *livekit.StartEgressRequest_Web:
		p.Info.Request = &livekit.EgressInfo_Web{Web: req.Web}

		// input params
		p.WebUrl = req.Web.Url
		if p.WebUrl == "" {
			err = errors.ErrInvalidInput("url")
			return
		}
		p.Display = fmt.Sprintf(":%d", 10+rand.Intn(2147483637))
		p.AudioEnabled = !req.Web.VideoOnly
		p.VideoEnabled = !req.Web.AudioOnly
		if !p.AudioEnabled && !p.VideoEnabled {
			err = errors.ErrInvalidInput("AudioOnly and VideoOnly")
			return
		}

		// encoding options
		switch opts := req.Web.Options.(type) {
		case *livekit.WebEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.WebEgressRequest_Advanced:
			p.applyAdvanced(opts.Advanced)
		}

		// output params
		switch o := req.Web.Output.(type) {
		case *livekit.WebEgressRequest_File:
			p.DisableManifest = o.File.DisableManifest
			p.updateOutputType(o.File.FileType)
			if err = p.updateFileParams(o.File.Filepath, o.File.Output); err != nil {
				return
			}

		case *livekit.WebEgressRequest_Stream:
			if err = p.updateStreamParams(OutputTypeRTMP, o.Stream.Urls); err != nil {
				return
			}

		case *livekit.WebEgressRequest_Segments:
			p.DisableManifest = o.Segments.DisableManifest
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
		p.Info.RoomName = req.TrackComposite.RoomName
		if p.Info.RoomName == "" {
			err = errors.ErrInvalidInput("RoomName")
			return
		}

		// encoding options
		switch opts := req.TrackComposite.Options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			p.applyPreset(opts.Preset)

		case *livekit.TrackCompositeEgressRequest_Advanced:
			p.applyAdvanced(opts.Advanced)
		}

		// input params
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
			p.DisableManifest = o.File.DisableManifest
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
			p.DisableManifest = o.Segments.DisableManifest
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
		p.Info.RoomName = req.Track.RoomName
		if p.Info.RoomName == "" {
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
			p.DisableManifest = o.File.DisableManifest
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
		if err = p.updateCodecs(); err != nil {
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

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_720P_30:
		p.Width = 720
		p.Height = 1280
		p.VideoBitrate = 3000

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_720P_60:
		p.Width = 720
		p.Height = 1280
		p.Framerate = 60

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_1080P_30:
		p.Width = 1080
		p.Height = 1920

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_1080P_60:
		p.Width = 1080
		p.Height = 1920
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
		case livekit.SegmentedFileProtocol_DEFAULT_SEGMENTED_FILE_PROTOCOL,
			livekit.SegmentedFileProtocol_HLS_PROTOCOL:
			p.OutputType = OutputTypeHLS
		}
	}
}

func (p *Params) updateFileParams(storageFilepath string, output interface{}) error {
	p.EgressType = EgressTypeFile
	p.StorageFilepath = storageFilepath
	p.FileInfo = &livekit.FileInfo{}
	p.Info.Result = &livekit.EgressInfo_File{File: p.FileInfo}

	// output location
	switch o := output.(type) {
	case *livekit.EncodedFileOutput_S3:
		p.UploadConfig = o.S3
	case *livekit.EncodedFileOutput_Azure:
		p.UploadConfig = o.Azure
	case *livekit.EncodedFileOutput_Gcp:
		p.UploadConfig = o.Gcp
	case *livekit.EncodedFileOutput_AliOSS:
		p.UploadConfig = o.AliOSS
	case *livekit.DirectFileOutput_S3:
		p.UploadConfig = o.S3
	case *livekit.DirectFileOutput_Azure:
		p.UploadConfig = o.Azure
	case *livekit.DirectFileOutput_Gcp:
		p.UploadConfig = o.Gcp
	case *livekit.DirectFileOutput_AliOSS:
		p.UploadConfig = o.AliOSS
	default:
		p.UploadConfig = p.conf.FileUpload
	}

	// filename
	replacements := map[string]string{
		"{room_name}": p.Info.RoomName,
		"{room_id}":   p.Info.RoomId,
		"{time}":      time.Now().Format("2006-01-02T150405"),
	}
	if p.OutputType != "" {
		err := p.updateFilepath(p.Info.RoomName, replacements)
		if err != nil {
			return err
		}
	} else {
		p.StorageFilepath = stringReplace(p.StorageFilepath, replacements)
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

func (p *Params) updateSegmentsParams(filePrefix string, playlistFilename string, segmentDuration uint32, output interface{}) error {
	p.EgressType = EgressTypeSegmentedFile
	p.LocalFilePrefix = filePrefix
	p.PlaylistFilename = playlistFilename
	p.SegmentDuration = int(segmentDuration)
	if p.SegmentDuration == 0 {
		p.SegmentDuration = 6
	}
	p.SegmentsInfo = &livekit.SegmentsInfo{}
	p.Info.Result = &livekit.EgressInfo_Segments{Segments: p.SegmentsInfo}

	// output location
	switch o := output.(type) {
	case *livekit.SegmentedFileOutput_S3:
		p.UploadConfig = o.S3
	case *livekit.SegmentedFileOutput_Azure:
		p.UploadConfig = o.Azure
	case *livekit.SegmentedFileOutput_Gcp:
		p.UploadConfig = o.Gcp
	case *livekit.SegmentedFileOutput_AliOSS:
		p.UploadConfig = o.AliOSS
	default:
		p.UploadConfig = p.conf.FileUpload
	}

	// filename
	err := p.UpdatePrefixAndPlaylist(p.Info.RoomName, map[string]string{
		"{room_name}": p.Info.RoomName,
		"{room_id}":   p.Info.RoomId,
		"{time}":      time.Now().Format("2006-01-02T150405"),
	})
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
		token, err := egress.BuildEgressToken(p.Info.EgressId, p.conf.ApiKey, p.conf.ApiSecret, p.Info.RoomName)
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
func (p *Params) updateCodecs() error {
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
func (p *Params) UpdateFileInfoFromSDK(fileIdentifier string, replacements map[string]string) error {
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

	return p.updateFilepath(fileIdentifier, replacements)
}

func (p *Params) updateFilepath(identifier string, replacements map[string]string) error {
	p.StorageFilepath = stringReplace(p.StorageFilepath, replacements)

	// get file extension
	ext := FileExtensionForOutputType[p.OutputType]

	if p.StorageFilepath == "" || strings.HasSuffix(p.StorageFilepath, "/") {
		// generate filepath
		p.StorageFilepath = fmt.Sprintf("%s%s-%s%s", p.StorageFilepath, identifier, time.Now().Format("2006-01-02T150405"), ext)
	} else if !strings.HasSuffix(p.StorageFilepath, string(ext)) {
		// check for existing (incorrect) extension
		extIdx := strings.LastIndex(p.StorageFilepath, ".")
		if extIdx > 0 {
			existingExt := FileExtension(p.StorageFilepath[extIdx:])
			if _, ok := FileExtensions[existingExt]; ok {
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
		tempDir := path.Join(p.conf.LocalOutputDirectory, p.Info.EgressId)

		// create temporary directory
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return err
		}

		// write to tmp dir
		p.LocalFilepath = path.Join(tempDir, filename)
	}

	p.Logger.Debugw("writing to file", "filename", p.LocalFilepath)
	return nil
}

func (p *Params) UpdatePrefixAndPlaylist(identifier string, replacements map[string]string) error {
	p.LocalFilePrefix = stringReplace(p.LocalFilePrefix, replacements)
	p.PlaylistFilename = stringReplace(p.PlaylistFilename, replacements)

	ext := FileExtensionForOutputType[p.OutputType]

	if p.LocalFilePrefix == "" || strings.HasSuffix(p.LocalFilePrefix, "/") {
		p.LocalFilePrefix = fmt.Sprintf("%s%s-%s", p.LocalFilePrefix, identifier, time.Now().String())
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
		tmpDir := path.Join(p.conf.LocalOutputDirectory, p.Info.EgressId)
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			return err
		}

		p.PlaylistFilename = path.Join(tmpDir, p.PlaylistFilename)
		p.LocalFilePrefix = path.Join(tmpDir, filePrefix)
	}
	p.Logger.Debugw("writing to path", "prefix", p.LocalFilePrefix)

	p.SegmentsInfo.PlaylistName = p.GetStorageFilepath(p.PlaylistFilename)
	return nil
}

func (p *Params) UpdatePlaylistNamesFromSDK(replacements map[string]string) {
	p.LocalFilePrefix = stringReplace(p.LocalFilePrefix, replacements)
	p.PlaylistFilename = stringReplace(p.PlaylistFilename, replacements)
	p.StoragePathPrefix = stringReplace(p.StoragePathPrefix, replacements)
	p.SegmentsInfo.PlaylistName = stringReplace(p.SegmentsInfo.PlaylistName, replacements)
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

func (p *SegmentedFileParams) GetStorageFilepath(filename string) string {
	// Remove any path prepended to the filename
	_, filename = path.Split(filename)

	return path.Join(p.StoragePathPrefix, filename)
}

func (p *Params) GetSessionTimeout() time.Duration {
	switch p.EgressType {
	case EgressTypeFile:
		return p.conf.FileOutputMaxDuration
	case EgressTypeStream, EgressTypeWebsocket:
		return p.conf.StreamOutputMaxDuration
	case EgressTypeSegmentedFile:
		return p.conf.SegmentOutputMaxDuration
	}

	return 0
}

type Manifest struct {
	EgressID          string `json:"egress_id,omitempty"`
	RoomID            string `json:"room_id,omitempty"`
	RoomName          string `json:"room_name,omitempty"`
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

func (p *Params) GetManifest() ([]byte, error) {
	manifest := Manifest{
		EgressID:          p.Info.EgressId,
		RoomID:            p.Info.RoomId,
		RoomName:          p.Info.RoomName,
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

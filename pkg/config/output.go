package config

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type EncodedOutput interface {
	GetFile() *livekit.EncodedFileOutput
	GetStream() *livekit.StreamOutput
	GetSegments() *livekit.SegmentedFileOutput
	GetFileOutputs() []*livekit.EncodedFileOutput
	GetStreamOutputs() []*livekit.StreamOutput
	GetSegmentOutputs() []*livekit.SegmentedFileOutput
}

type OutputConfig struct {
	types.EgressType
	types.OutputType

	FileParams
	SegmentParams
	StreamParams
	WebsocketParams

	DisableManifest bool
	UploadConfig    interface{}
}

type FileParams struct {
	FileInfo        *livekit.FileInfo
	LocalFilepath   string
	StorageFilepath string
}

type SegmentParams struct {
	SegmentsInfo      *livekit.SegmentsInfo
	LocalFilePrefix   string
	StoragePathPrefix string
	PlaylistFilename  string
	SegmentSuffix     livekit.SegmentedFileSuffix
	SegmentDuration   int
}

type StreamParams struct {
	StreamUrls []string
	StreamInfo map[string]*livekit.StreamInfo
}

type WebsocketParams struct {
	WebsocketUrl string
}

func (p *PipelineConfig) updateEncodedOutputs(req EncodedOutput) error {
	var deprecated bool

	// file output
	file := req.GetFile()
	if file != nil {
		deprecated = true
	} else {
		files := req.GetFileOutputs()
		if len(files) > 1 {
			return errors.ErrInvalidInput("multiple file outputs")
		}
		if len(files) == 1 {
			file = files[0]
			deprecated = false
		}
	}
	if file != nil {
		conf, err := p.getEncodedFileConfig(req, file)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeFile] = conf
		p.Info.FileResults = []*livekit.FileInfo{conf.FileInfo}
		if deprecated {
			p.Info.Result = &livekit.EgressInfo_File{File: conf.FileInfo}
			return nil
		}
	}

	// stream output
	stream := req.GetStream()
	if stream != nil {
		deprecated = true
	} else {
		streams := req.GetStreamOutputs()
		if len(streams) > 1 {
			return errors.ErrInvalidInput("multiple stream outputs")
		}
		if len(streams) == 1 {
			stream = streams[0]
			deprecated = false
		}
	}
	if stream != nil {
		conf, err := p.getStreamConfig(types.OutputTypeRTMP, stream.Urls)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeStream] = conf
		streamInfoList := make([]*livekit.StreamInfo, 0, len(conf.StreamInfo))
		for _, info := range conf.StreamInfo {
			streamInfoList = append(streamInfoList, info)
		}
		p.Info.StreamResults = streamInfoList
		if deprecated {
			p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
			return nil
		}
	}

	// segment output
	segment := req.GetSegments()
	if segment != nil {
		deprecated = true
	} else {
		segments := req.GetSegmentOutputs()
		if len(segments) > 1 {
			return errors.ErrInvalidInput("multiple segment outputs")
		}
		if len(segments) == 1 {
			segment = segments[0]
			deprecated = false
		}
	}
	if segment != nil {
		conf, err := p.getSegmentConfig(segment)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeSegments] = conf
		p.Info.SegmentResults = []*livekit.SegmentsInfo{conf.SegmentsInfo}
		if deprecated {
			p.Info.Result = &livekit.EgressInfo_Segments{Segments: conf.SegmentsInfo}
			return nil
		}
	}

	return nil
}

func (p *PipelineConfig) updateDirectOutput(req *livekit.TrackEgressRequest) error {
	switch o := req.Output.(type) {
	case *livekit.TrackEgressRequest_File:
		conf, err := p.getDirectFileConfig(o.File)
		if err != nil {
			return err
		}

		p.Info.FileResults = []*livekit.FileInfo{conf.FileInfo}
		p.Info.Result = &livekit.EgressInfo_File{File: conf.FileInfo}
		p.Outputs[types.EgressTypeFile] = conf

	case *livekit.TrackEgressRequest_WebsocketUrl:
		conf, err := p.getStreamConfig(types.OutputTypeRaw, []string{o.WebsocketUrl})
		if err != nil {
			return err
		}

		streamInfoList := make([]*livekit.StreamInfo, 0, len(conf.StreamInfo))
		for _, info := range conf.StreamInfo {
			streamInfoList = append(streamInfoList, info)
		}
		p.Info.StreamResults = streamInfoList
		p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
		p.Outputs[types.EgressTypeWebsocket] = conf

	default:
		return errors.ErrInvalidInput("output")
	}

	return nil
}

func (p *PipelineConfig) getEncodedFileConfig(req interface{}, file *livekit.EncodedFileOutput) (*OutputConfig, error) {
	outputType := types.OutputTypeUnknown
	updateOutputType := true

	switch req.(type) {
	case *livekit.TrackCompositeEgressRequest:
		if file.FileType == livekit.EncodedFileType_DEFAULT_FILETYPE {
			updateOutputType = false
		}
	}

	if updateOutputType {
		switch file.FileType {
		case livekit.EncodedFileType_DEFAULT_FILETYPE:
			if !p.VideoEnabled && p.AudioCodec != types.MimeTypeAAC {
				outputType = types.OutputTypeOGG
			} else {
				outputType = types.OutputTypeMP4
			}
		case livekit.EncodedFileType_MP4:
			outputType = types.OutputTypeMP4
		case livekit.EncodedFileType_OGG:
			outputType = types.OutputTypeOGG
		}
	}

	return p.getFileConfig(outputType, file)
}

func (p *PipelineConfig) getDirectFileConfig(file *livekit.DirectFileOutput) (*OutputConfig, error) {
	return p.getFileConfig(types.OutputTypeUnknown, file)
}

type fileConf interface {
	GetFilepath() string
	GetDisableManifest() bool
	uploadConf
}

func (p *PipelineConfig) getFileConfig(outputType types.OutputType, file fileConf) (*OutputConfig, error) {
	conf := &OutputConfig{
		EgressType: types.EgressTypeFile,
		OutputType: outputType,
		FileParams: FileParams{
			FileInfo:        &livekit.FileInfo{},
			StorageFilepath: file.GetFilepath(),
		},
		DisableManifest: file.GetDisableManifest(),
	}

	// filename
	identifier, replacements := p.getFilenameInfo()
	if conf.OutputType != types.OutputTypeUnknown {
		err := conf.updateFilepath(p, identifier, replacements)
		if err != nil {
			return nil, err
		}
	} else {
		conf.StorageFilepath = stringReplace(conf.StorageFilepath, replacements)
	}

	conf.UploadConfig = p.getUploadConfig(file)
	return conf, nil
}

func (p *PipelineConfig) getStreamConfig(outputType types.OutputType, urls []string) (*OutputConfig, error) {
	if len(urls) < 1 {
		return nil, errors.ErrInvalidInput("stream url")
	}

	conf := &OutputConfig{
		OutputType: outputType,
	}

	switch outputType {
	case types.OutputTypeRTMP:
		conf.EgressType = types.EgressTypeStream
		p.AudioCodec = types.MimeTypeAAC
		p.VideoCodec = types.MimeTypeH264
		conf.StreamUrls = urls

	case types.OutputTypeRaw:
		conf.EgressType = types.EgressTypeWebsocket
		p.AudioCodec = types.MimeTypeRaw
		conf.WebsocketUrl = urls[0]
	}

	// Use a 4s default key frame interval for streaming
	if p.KeyFrameInterval == 0 {
		p.KeyFrameInterval = 4
	}

	conf.StreamInfo = make(map[string]*livekit.StreamInfo)
	var streamInfoList []*livekit.StreamInfo
	for _, rawUrl := range urls {
		redacted, err := p.ValidateUrl(rawUrl, outputType)
		if err != nil {
			return nil, err
		}

		info := &livekit.StreamInfo{Url: redacted}
		conf.StreamInfo[rawUrl] = info
		streamInfoList = append(streamInfoList, info)
	}

	return conf, nil
}

// segments should always be added last, so we can check keyframe interval from file/stream
func (p *PipelineConfig) getSegmentConfig(segments *livekit.SegmentedFileOutput) (*OutputConfig, error) {
	conf := &OutputConfig{
		EgressType: types.EgressTypeSegments,
		SegmentParams: SegmentParams{
			SegmentsInfo:     &livekit.SegmentsInfo{},
			LocalFilePrefix:  segments.FilenamePrefix,
			SegmentSuffix:    segments.FilenameSuffix,
			PlaylistFilename: segments.PlaylistName,
			SegmentDuration:  int(segments.SegmentDuration),
		},
		DisableManifest: segments.DisableManifest,
	}

	if conf.SegmentDuration == 0 {
		if p.KeyFrameInterval != 0 {
			conf.SegmentDuration = int(p.KeyFrameInterval)
		} else {
			conf.SegmentDuration = 4
		}
	}

	if p.KeyFrameInterval == 0 {
		// The splitMuxSink should request key frames from the encoder at expected frame boundaries.
		// Set the key frame interval to twice the segment duration as a failsafe
		p.KeyFrameInterval = 2 * float64(conf.SegmentDuration)
	} else if p.KeyFrameInterval < float64(conf.SegmentDuration) {
		conf.SegmentDuration = int(p.KeyFrameInterval)
	}

	// filename
	identifier, replacements := p.getFilenameInfo()
	err := conf.updatePrefixAndPlaylist(p, identifier, replacements)
	if err != nil {
		return nil, err
	}

	switch segments.Protocol {
	case livekit.SegmentedFileProtocol_DEFAULT_SEGMENTED_FILE_PROTOCOL,
		livekit.SegmentedFileProtocol_HLS_PROTOCOL:
		conf.OutputType = types.OutputTypeHLS
	}

	conf.UploadConfig = p.getUploadConfig(segments)
	return conf, nil
}

func (p *PipelineConfig) getFilenameInfo() (string, map[string]string) {
	now := time.Now()
	utc := fmt.Sprintf("%s%03d", now.Format("20060102150405"), now.UnixMilli()%1000)
	if p.Info.RoomName != "" {
		return p.Info.RoomName, map[string]string{
			"{room_name}": p.Info.RoomName,
			"{room_id}":   p.Info.RoomId,
			"{time}":      now.Format("2006-01-02T150405"),
			"{utc}":       utc,
		}
	}

	return "web", map[string]string{
		"{time}": now.Format("2006-01-02T150405"),
		"{utc}":  utc,
	}
}

func (o *OutputConfig) updateFilepath(p *PipelineConfig, identifier string, replacements map[string]string) error {
	o.StorageFilepath = stringReplace(o.StorageFilepath, replacements)

	// get file extension
	ext := types.FileExtensionForOutputType[o.OutputType]

	if o.StorageFilepath == "" || strings.HasSuffix(o.StorageFilepath, "/") {
		// generate filepath
		o.StorageFilepath = fmt.Sprintf("%s%s-%s%s", o.StorageFilepath, identifier, time.Now().Format("2006-01-02T150405"), ext)
	} else if !strings.HasSuffix(o.StorageFilepath, string(ext)) {
		// check for existing (incorrect) extension
		extIdx := strings.LastIndex(o.StorageFilepath, ".")
		if extIdx > 0 {
			existingExt := types.FileExtension(o.StorageFilepath[extIdx:])
			if _, ok := types.FileExtensions[existingExt]; ok {
				o.StorageFilepath = o.StorageFilepath[:extIdx]
			}
		}
		// add file extension
		o.StorageFilepath = o.StorageFilepath + string(ext)
	}

	// update filename
	o.FileInfo.Filename = o.StorageFilepath

	// get local filepath
	dir, filename := path.Split(o.StorageFilepath)
	if o.UploadConfig == nil {
		if dir != "" {
			// create local directory
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}
		// write directly to requested location
		o.LocalFilepath = o.StorageFilepath
	} else {
		// prepend the configuration base directory and the egress Id
		tempDir := path.Join(p.LocalOutputDirectory, p.Info.EgressId)

		// create temporary directory
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return err
		}

		// write to tmp dir
		o.LocalFilepath = path.Join(tempDir, filename)
	}

	return nil
}

func (o *OutputConfig) updatePrefixAndPlaylist(p *PipelineConfig, identifier string, replacements map[string]string) error {
	o.LocalFilePrefix = stringReplace(o.LocalFilePrefix, replacements)
	o.PlaylistFilename = stringReplace(o.PlaylistFilename, replacements)

	ext := types.FileExtensionForOutputType[o.OutputType]

	if o.LocalFilePrefix == "" || strings.HasSuffix(o.LocalFilePrefix, "/") {
		o.LocalFilePrefix = fmt.Sprintf("%s%s-%s", o.LocalFilePrefix, identifier, time.Now().Format("2006-01-02T150405"))
	}

	// Playlist path is relative to file prefix. Only keep actual filename if a full path is given
	_, o.PlaylistFilename = path.Split(o.PlaylistFilename)
	if o.PlaylistFilename == "" {
		o.PlaylistFilename = fmt.Sprintf("playlist-%s%s", identifier, ext)
	} else if !strings.HasSuffix(o.PlaylistFilename, string(ext)) {
		o.PlaylistFilename = fmt.Sprintf("%s%s", o.PlaylistFilename, ext)
	}

	var filePrefix string
	o.StoragePathPrefix, filePrefix = path.Split(o.LocalFilePrefix)
	if o.UploadConfig == nil {
		if o.StoragePathPrefix != "" {
			if err := os.MkdirAll(o.StoragePathPrefix, 0755); err != nil {
				return err
			}
		}
		o.PlaylistFilename = path.Join(o.StoragePathPrefix, o.PlaylistFilename)
	} else {
		// Prepend the configuration base directory and the egress Id
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		tmpDir := path.Join(p.LocalOutputDirectory, p.Info.EgressId)
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			return err
		}

		o.PlaylistFilename = path.Join(tmpDir, o.PlaylistFilename)
		o.LocalFilePrefix = path.Join(tmpDir, filePrefix)
	}

	o.SegmentsInfo.PlaylistName = o.GetStorageFilepath(o.PlaylistFilename)
	return nil
}

func (o *OutputConfig) GetStorageFilepath(filename string) string {
	// Remove any path prepended to the filename
	_, filename = path.Split(filename)
	return path.Join(o.StoragePathPrefix, filename)
}

type uploadConf interface {
	GetS3() *livekit.S3Upload
	GetGcp() *livekit.GCPUpload
	GetAzure() *livekit.AzureBlobUpload
	GetAliOSS() *livekit.AliOSSUpload
}

func (p *PipelineConfig) getUploadConfig(upload uploadConf) interface{} {
	if s3 := upload.GetS3(); s3 != nil {
		return s3
	}
	if gcp := upload.GetGcp(); gcp != nil {
		return gcp
	}
	if azure := upload.GetAzure(); azure != nil {
		return azure
	}
	if ali := upload.GetAliOSS(); ali != nil {
		return ali
	}
	if p.S3 != nil {
		return p.S3.ToS3Upload()
	}
	if p.GCP != nil {
		return p.GCP.ToGCPUpload()
	}
	if p.Azure != nil {
		return p.Azure.ToAzureUpload()
	}
	if p.AliOSS != nil {
		return p.AliOSS.ToAliOSSUpload()
	}
	return nil
}

func redactEncodedOutputs(out EncodedOutput) {
	if file := out.GetFile(); file != nil {
		redactUpload(file)
	} else if stream := out.GetStream(); stream != nil {
		redactStreamKeys(stream)
	} else if segment := out.GetSegments(); segment != nil {
		redactUpload(segment)
	} else {
		if files := out.GetFileOutputs(); len(files) == 1 {
			redactUpload(files[0])
		}
		if streams := out.GetStreamOutputs(); len(streams) == 1 {
			redactStreamKeys(streams[0])
		}
		if segments := out.GetSegmentOutputs(); len(segments) == 1 {
			redactUpload(segments[0])
		}
	}
}

func redactStreamKeys(stream *livekit.StreamOutput) {
	for i, url := range stream.Urls {
		if redacted, ok := redactStreamKey(url); ok {
			stream.Urls[i] = redacted
		}
	}
}

// rtmp urls must be of format rtmp(s)://{host}(/{path})/{app}/{stream_key}( live=1)
var rtmpRegexp = regexp.MustCompile("^(rtmps?:\\/\\/)(.*\\/)(.*\\/)(\\S*)( live=1)?$")

func redactStreamKey(url string) (string, bool) {
	match := rtmpRegexp.FindStringSubmatch(url)
	if len(match) != 6 {
		return url, false
	}

	match[4] = redact(match[4])
	return strings.Join(match[1:], ""), true
}

func redactUpload(upload uploadConf) {
	if s3 := upload.GetS3(); s3 != nil {
		s3.AccessKey = redact(s3.AccessKey)
		s3.Secret = redact(s3.Secret)
		return
	}

	if gcp := upload.GetGcp(); gcp != nil {
		gcp.Credentials = []byte(redact(string(gcp.Credentials)))
		return
	}

	if azure := upload.GetAzure(); azure != nil {
		azure.AccountName = redact(azure.AccountName)
		azure.AccountKey = redact(azure.AccountKey)
		return
	}

	if aliOSS := upload.GetAliOSS(); aliOSS != nil {
		aliOSS.AccessKey = redact(aliOSS.AccessKey)
		aliOSS.Secret = redact(aliOSS.Secret)
		return
	}
}

func redact(s string) string {
	return strings.Repeat("*", len(s))
}

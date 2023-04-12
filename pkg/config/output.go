package config

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/egress/pkg/util"
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

type fileOutput interface {
	GetFilepath() string
	GetDisableManifest() bool
	uploader
}

type uploader interface {
	GetS3() *livekit.S3Upload
	GetGcp() *livekit.GCPUpload
	GetAzure() *livekit.AzureBlobUpload
	GetAliOSS() *livekit.AliOSSUpload
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
	SegmentsInfo     *livekit.SegmentsInfo
	LocalDir         string
	StorageDir       string
	PlaylistFilename string
	SegmentPrefix    string
	SegmentSuffix    livekit.SegmentedFileSuffix
	SegmentDuration  int
}

type StreamParams struct {
	StreamUrls []string
	StreamInfo map[string]*livekit.StreamInfo
}

type WebsocketParams struct {
	WebsocketUrl string
}

func (p *PipelineConfig) updateEncodedOutputs(req EncodedOutput) error {
	files := req.GetFileOutputs()
	streams := req.GetStreamOutputs()
	segments := req.GetSegmentOutputs()

	// file output
	var file *livekit.EncodedFileOutput
	switch len(files) {
	case 0:
		file = req.GetFile()
	case 1:
		file = files[0]
	default:
		return errors.ErrInvalidInput("multiple file outputs")
	}
	if file != nil {
		conf, err := p.getEncodedFileConfig(req, file)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeFile] = conf
		p.OutputCount++

		p.Info.FileResults = []*livekit.FileInfo{conf.FileInfo}
		if len(streams)+len(segments) == 0 {
			p.Info.Result = &livekit.EgressInfo_File{File: conf.FileInfo}
			return nil
		}
	}

	// stream output
	var stream *livekit.StreamOutput
	switch len(streams) {
	case 0:
		stream = req.GetStream()
	case 1:
		stream = streams[0]
	default:
		return errors.ErrInvalidInput("multiple stream outputs")
	}
	if stream != nil {
		conf, err := p.getStreamConfig(types.OutputTypeRTMP, stream.Urls)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeStream] = conf
		p.OutputCount += len(stream.Urls)

		streamInfoList := make([]*livekit.StreamInfo, 0, len(conf.StreamInfo))
		for _, info := range conf.StreamInfo {
			streamInfoList = append(streamInfoList, info)
		}
		p.Info.StreamResults = streamInfoList
		if len(files)+len(segments) == 0 {
			// empty stream output only valid in combination with other outputs
			if len(stream.Urls) == 0 {
				return errors.ErrInvalidInput("stream url")
			}

			p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
			return nil
		}
	}

	// segment output
	var segment *livekit.SegmentedFileOutput
	switch len(segments) {
	case 0:
		segment = req.GetSegments()
	case 1:
		segment = segments[0]
	default:
		return errors.ErrInvalidInput("multiple segmented file outputs")
	}
	if segment != nil {
		conf, err := p.getSegmentConfig(segment)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeSegments] = conf
		p.OutputCount++

		p.Info.SegmentResults = []*livekit.SegmentsInfo{conf.SegmentsInfo}
		if len(streams)+len(segments) == 0 {
			p.Info.Result = &livekit.EgressInfo_Segments{Segments: conf.SegmentsInfo}
			return nil
		}
	}

	if p.OutputCount == 0 {
		return errors.ErrInvalidInput("output")
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
		p.OutputCount = 1

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
		p.OutputCount = 1

	default:
		return errors.ErrInvalidInput("output")
	}

	return nil
}

func (p *PipelineConfig) getEncodedFileConfig(req interface{}, file *livekit.EncodedFileOutput) (*OutputConfig, error) {
	var outputType types.OutputType

	switch file.FileType {
	case livekit.EncodedFileType_DEFAULT_FILETYPE:
		outputType = types.OutputTypeUnknownFile
		//	case livekit.EncodedFileType_DEFAULT_FILETYPE:
		//		if !p.VideoEnabled && p.AudioOutCodec != types.MimeTypeAAC {
		//			outputType = types.OutputTypeOGG
		//		} else {
		//			outputType = types.OutputTypeMP4
		//		}
	case livekit.EncodedFileType_MP4:
		outputType = types.OutputTypeMP4
	case livekit.EncodedFileType_OGG:
		outputType = types.OutputTypeOGG
	}

	return p.getFileConfig(outputType, file)
}

func (p *PipelineConfig) getDirectFileConfig(file *livekit.DirectFileOutput) (*OutputConfig, error) {
	return p.getFileConfig(types.OutputTypeUnknownFile, file)
}

func (p *PipelineConfig) getFileConfig(outputType types.OutputType, file fileOutput) (*OutputConfig, error) {
	conf := &OutputConfig{
		EgressType: types.EgressTypeFile,
		OutputType: outputType,
		FileParams: FileParams{
			FileInfo:        &livekit.FileInfo{},
			StorageFilepath: clean(file.GetFilepath()),
		},
		DisableManifest: file.GetDisableManifest(),
		UploadConfig:    p.getUploadConfig(file),
	}

	// filename
	identifier, replacements := p.getFilenameInfo()
	if conf.OutputType != types.OutputTypeUnknownFile {
		err := conf.updateFilepath(p, identifier, replacements)
		if err != nil {
			return nil, err
		}
	} else {
		conf.StorageFilepath = stringReplace(conf.StorageFilepath, replacements)
	}

	return conf, nil
}

func (p *PipelineConfig) getStreamConfig(outputType types.OutputType, urls []string) (*OutputConfig, error) {
	conf := &OutputConfig{
		OutputType: outputType,
	}

	switch outputType {
	case types.OutputTypeRTMP:
		conf.EgressType = types.EgressTypeStream
		p.AudioOutCodec = types.MimeTypeAAC
		p.VideoOutCodec = types.MimeTypeH264
		conf.StreamUrls = urls

	case types.OutputTypeRaw:
		conf.EgressType = types.EgressTypeWebsocket
		p.AudioOutCodec = types.MimeTypeRawAudio
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
			SegmentPrefix:    clean(segments.FilenamePrefix),
			SegmentSuffix:    segments.FilenameSuffix,
			PlaylistFilename: clean(segments.PlaylistName),
			SegmentDuration:  int(segments.SegmentDuration),
		},
		DisableManifest: segments.DisableManifest,
		UploadConfig:    p.getUploadConfig(segments),
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

	switch segments.Protocol {
	case livekit.SegmentedFileProtocol_DEFAULT_SEGMENTED_FILE_PROTOCOL,
		livekit.SegmentedFileProtocol_HLS_PROTOCOL:
		conf.OutputType = types.OutputTypeHLS
	}

	// filename
	err := conf.updatePrefixAndPlaylist(p)
	if err != nil {
		return nil, err
	}

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
		if extIdx := strings.LastIndex(o.StorageFilepath, "."); extIdx > -1 {
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

func (o *OutputConfig) updatePrefixAndPlaylist(p *PipelineConfig) error {
	identifier, replacements := p.getFilenameInfo()

	o.SegmentPrefix = stringReplace(o.SegmentPrefix, replacements)
	o.PlaylistFilename = stringReplace(o.PlaylistFilename, replacements)

	ext := types.FileExtensionForOutputType[o.OutputType]

	playlistDir, playlistName := path.Split(o.PlaylistFilename)
	fileDir, filePrefix := path.Split(o.SegmentPrefix)

	// remove extension from playlist name
	if extIdx := strings.LastIndex(playlistName, "."); extIdx > -1 {
		existingExt := types.FileExtension(playlistName[extIdx:])
		if _, ok := types.FileExtensions[existingExt]; ok {
			playlistName = playlistName[:extIdx]
		}
		playlistName = playlistName[:extIdx]
	}

	// only keep fileDir if it is a subdirectory of playlistDir
	if fileDir != "" {
		if playlistDir == fileDir {
			fileDir = ""
		} else if playlistDir == "" {
			playlistDir = fileDir
			fileDir = ""
		}
	}
	o.StorageDir = playlistDir

	// ensure playlistName
	if playlistName == "" {
		if filePrefix != "" {
			playlistName = filePrefix
		} else {
			playlistName = fmt.Sprintf("%s-%s", identifier, time.Now().Format("2006-01-02T150405"))
		}
	}

	// ensure filePrefix
	if filePrefix == "" {
		filePrefix = playlistName
	}

	// update config
	o.StorageDir = playlistDir
	o.PlaylistFilename = fmt.Sprintf("%s%s", playlistName, ext)
	o.SegmentPrefix = fmt.Sprintf("%s%s", fileDir, filePrefix)

	if o.UploadConfig == nil {
		o.LocalDir = playlistDir
	} else {
		// Prepend the configuration base directory and the egress Id
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		// Append a "/" to the path for consistency with the "UploadConfig == nil" case
		o.LocalDir = path.Join(p.LocalOutputDirectory, p.Info.EgressId) + "/"
	}

	// create local directories
	if fileDir != "" {
		if err := os.MkdirAll(path.Join(o.LocalDir, fileDir), 0755); err != nil {
			return err
		}
	} else if o.LocalDir != "" {
		if err := os.MkdirAll(o.LocalDir, 0755); err != nil {
			return err
		}
	}

	o.SegmentsInfo.PlaylistName = path.Join(o.StorageDir, o.PlaylistFilename)
	return nil
}

func (p *PipelineConfig) getUploadConfig(upload uploader) interface{} {
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

func redactUpload(upload uploader) {
	if s3 := upload.GetS3(); s3 != nil {
		s3.AccessKey = util.Redact(s3.AccessKey)
		s3.Secret = util.Redact(s3.Secret)
		return
	}

	if gcp := upload.GetGcp(); gcp != nil {
		gcp.Credentials = util.Redact(gcp.Credentials)
		return
	}

	if azure := upload.GetAzure(); azure != nil {
		azure.AccountName = util.Redact(azure.AccountName)
		azure.AccountKey = util.Redact(azure.AccountKey)
		return
	}

	if aliOSS := upload.GetAliOSS(); aliOSS != nil {
		aliOSS.AccessKey = util.Redact(aliOSS.AccessKey)
		aliOSS.Secret = util.Redact(aliOSS.Secret)
		return
	}
}

func redactStreamKeys(stream *livekit.StreamOutput) {
	for i, url := range stream.Urls {
		if redacted, ok := util.RedactStreamKey(url); ok {
			stream.Urls[i] = redacted
		}
	}
}

func clean(filepath string) string {
	hasEndingSlash := strings.HasSuffix(filepath, "/")
	filepath = path.Clean(filepath)
	for strings.HasPrefix(filepath, "../") {
		filepath = filepath[3:]
	}
	if filepath == "" || filepath == "." || filepath == ".." {
		return ""
	}
	if hasEndingSlash {
		return filepath + "/"
	}
	return filepath
}

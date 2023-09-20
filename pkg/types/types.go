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

package types

type RequestType string
type SourceType string
type EgressType string
type MimeType string
type Profile string
type OutputType string
type FileExtension string

const (
	// request types
	RequestTypeRoomComposite  = "room_composite"
	RequestTypeWeb            = "web"
	RequestTypeParticipant    = "participant"
	RequestTypeTrackComposite = "track_composite"
	RequestTypeTrack          = "track"

	// source types
	SourceTypeWeb SourceType = "web"
	SourceTypeSDK SourceType = "sdk"

	// egress types
	EgressTypeStream    EgressType = "stream"
	EgressTypeWebsocket EgressType = "websocket"
	EgressTypeFile      EgressType = "file"
	EgressTypeSegments  EgressType = "segments"
	EgressTypeImages    EgressType = "images"

	// input types
	MimeTypeAAC      MimeType = "audio/aac"
	MimeTypeOpus     MimeType = "audio/opus"
	MimeTypeRawAudio MimeType = "audio/x-raw"
	MimeTypeH264     MimeType = "video/h264"
	MimeTypeVP8      MimeType = "video/vp8"
	MimeTypeVP9      MimeType = "video/vp9"
	MimeTypeJPEG     MimeType = "image/jpeg"
	MimeTypeRawVideo MimeType = "video/x-raw"

	// video profiles
	ProfileBaseline Profile = "baseline"
	ProfileMain     Profile = "main"
	ProfileHigh     Profile = "high"

	// output types
	OutputTypeUnknownFile OutputType = ""
	OutputTypeRaw         OutputType = "audio/x-raw"
	OutputTypeOGG         OutputType = "audio/ogg"
	OutputTypeIVF         OutputType = "video/x-ivf"
	OutputTypeMP4         OutputType = "video/mp4"
	OutputTypeTS          OutputType = "video/mp2t"
	OutputTypeWebM        OutputType = "video/webm"
	OutputTypeJPEG        OutputType = "image/jpeg"
	OutputTypeRTMP        OutputType = "rtmp"
	OutputTypeHLS         OutputType = "application/x-mpegurl"
	OutputTypeJSON        OutputType = "application/json"
	OutputTypeBlob        OutputType = "application/octet-stream"

	// file extensions
	FileExtensionRaw  = ".raw"
	FileExtensionOGG  = ".ogg"
	FileExtensionIVF  = ".ivf"
	FileExtensionMP4  = ".mp4"
	FileExtensionTS   = ".ts"
	FileExtensionWebM = ".webm"
	FileExtensionM3U8 = ".m3u8"
	FileExtensionJPEG = ".jpeg"
)

var (
	DefaultAudioCodecs = map[OutputType]MimeType{
		OutputTypeRaw:  MimeTypeRawAudio,
		OutputTypeOGG:  MimeTypeOpus,
		OutputTypeMP4:  MimeTypeAAC,
		OutputTypeTS:   MimeTypeAAC,
		OutputTypeWebM: MimeTypeOpus,
		OutputTypeRTMP: MimeTypeAAC,
		OutputTypeHLS:  MimeTypeAAC,
	}

	DefaultVideoCodecs = map[OutputType]MimeType{
		OutputTypeIVF:  MimeTypeVP8,
		OutputTypeMP4:  MimeTypeH264,
		OutputTypeTS:   MimeTypeH264,
		OutputTypeWebM: MimeTypeVP8,
		OutputTypeRTMP: MimeTypeH264,
		OutputTypeHLS:  MimeTypeH264,
	}

	FileExtensions = map[FileExtension]struct{}{
		FileExtensionRaw:  {},
		FileExtensionOGG:  {},
		FileExtensionIVF:  {},
		FileExtensionMP4:  {},
		FileExtensionTS:   {},
		FileExtensionWebM: {},
		FileExtensionM3U8: {},
		FileExtensionJPEG: {},
	}

	FileExtensionForOutputType = map[OutputType]FileExtension{
		OutputTypeRaw:  FileExtensionRaw,
		OutputTypeOGG:  FileExtensionOGG,
		OutputTypeIVF:  FileExtensionIVF,
		OutputTypeMP4:  FileExtensionMP4,
		OutputTypeTS:   FileExtensionTS,
		OutputTypeWebM: FileExtensionWebM,
		OutputTypeHLS:  FileExtensionM3U8,
		OutputTypeJPEG: FileExtensionJPEG,
	}

	CodecCompatibility = map[OutputType]map[MimeType]bool{
		OutputTypeRaw: {
			MimeTypeRawAudio: true,
		},
		OutputTypeOGG: {
			MimeTypeOpus: true,
		},
		OutputTypeIVF: {
			MimeTypeVP8: true,
			MimeTypeVP9: true,
		},
		OutputTypeMP4: {
			MimeTypeAAC:  true,
			MimeTypeOpus: true,
			MimeTypeH264: true,
		},
		OutputTypeTS: {
			MimeTypeAAC:  true,
			MimeTypeOpus: true,
			MimeTypeH264: true,
		},
		OutputTypeWebM: {
			MimeTypeOpus: true,
			MimeTypeVP8:  true,
			MimeTypeVP9:  true,
		},
		OutputTypeRTMP: {
			MimeTypeAAC:  true,
			MimeTypeH264: true,
		},
		OutputTypeHLS: {
			MimeTypeAAC:  true,
			MimeTypeH264: true,
		},
		OutputTypeUnknownFile: {
			MimeTypeAAC:  true,
			MimeTypeOpus: true,
			MimeTypeH264: true,
			MimeTypeVP8:  true,
			MimeTypeVP9:  true,
		},
	}

	AllOutputAudioCodecs = map[MimeType]bool{
		MimeTypeAAC:      true,
		MimeTypeOpus:     true,
		MimeTypeRawAudio: true,
	}

	AllOutputVideoCodecs = map[MimeType]bool{
		MimeTypeH264: true,
	}

	AudioOnlyFileOutputTypes = []OutputType{
		OutputTypeOGG,
		OutputTypeMP4,
	}
	VideoOnlyFileOutputTypes = []OutputType{
		OutputTypeMP4,
	}
	AudioVideoFileOutputTypes = []OutputType{
		OutputTypeMP4,
	}

	TrackOutputTypes = map[MimeType]OutputType{
		MimeTypeOpus: OutputTypeOGG,
		MimeTypeH264: OutputTypeMP4,
		MimeTypeVP8:  OutputTypeWebM,
		MimeTypeVP9:  OutputTypeWebM,
	}
)

func GetOutputTypeCompatibleWithCodecs(types []OutputType, audioCodecs map[MimeType]bool, videoCodecs map[MimeType]bool) OutputType {
	for _, t := range types {
		if audioCodecs != nil && !IsOutputTypeCompatibleWithCodecs(t, audioCodecs) {
			continue
		}

		if videoCodecs != nil && !IsOutputTypeCompatibleWithCodecs(t, videoCodecs) {
			continue
		}

		return t
	}

	return OutputTypeUnknownFile
}

func IsOutputTypeCompatibleWithCodecs(ot OutputType, codecs map[MimeType]bool) bool {
	for k := range codecs {
		if CodecCompatibility[ot][k] {
			return true
		}
	}
	return false
}

func GetMapIntersection[K comparable](mapA map[K]bool, mapB map[K]bool) map[K]bool {
	res := make(map[K]bool)

	for k := range mapA {
		if mapB[k] {
			res[k] = true
		}
	}

	return res
}

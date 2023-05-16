package types

type MimeType string
type Profile string
type SourceType string
type EgressType string
type OutputType string
type FileExtension string

const (
	// source types
	SourceTypeWeb SourceType = "web"
	SourceTypeSDK SourceType = "sdk"

	// input types
	MimeTypeAAC      MimeType = "audio/aac"
	MimeTypeOpus     MimeType = "audio/opus"
	MimeTypeRawAudio MimeType = "audio/x-raw"
	MimeTypeH264     MimeType = "video/h264"
	MimeTypeVP8      MimeType = "video/vp8"
	MimeTypeRawVideo MimeType = "video/x-raw"

	// video profiles
	ProfileBaseline Profile = "baseline"
	ProfileMain     Profile = "main"
	ProfileHigh     Profile = "high"

	// egress types
	EgressTypeStream    EgressType = "stream"
	EgressTypeWebsocket EgressType = "websocket"
	EgressTypeFile      EgressType = "file"
	EgressTypeSegments  EgressType = "segments"

	// output types
	OutputTypeUnknownFile OutputType = ""
	OutputTypeRaw         OutputType = "audio/x-raw"
	OutputTypeOGG         OutputType = "audio/ogg"
	OutputTypeIVF         OutputType = "video/x-ivf"
	OutputTypeMP4         OutputType = "video/mp4"
	OutputTypeTS          OutputType = "video/mp2t"
	OutputTypeWebM        OutputType = "video/webm"
	OutputTypeRTMP        OutputType = "rtmp"
	OutputTypeHLS         OutputType = "application/x-mpegurl"
	OutputTypeJSON        OutputType = "application/json"

	// file extensions
	FileExtensionRaw  = ".raw"
	FileExtensionOGG  = ".ogg"
	FileExtensionIVF  = ".ivf"
	FileExtensionMP4  = ".mp4"
	FileExtensionTS   = ".ts"
	FileExtensionWebM = ".webm"
	FileExtensionM3U8 = ".m3u8"
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
	}

	FileExtensionForOutputType = map[OutputType]FileExtension{
		OutputTypeRaw:  FileExtensionRaw,
		OutputTypeOGG:  FileExtensionOGG,
		OutputTypeIVF:  FileExtensionIVF,
		OutputTypeMP4:  FileExtensionMP4,
		OutputTypeTS:   FileExtensionTS,
		OutputTypeWebM: FileExtensionWebM,
		OutputTypeHLS:  FileExtensionM3U8,
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
	for k, _ := range codecs {
		if CodecCompatibility[ot][k] {
			return true
		}
	}
	return false
}

func GetMapIntersection[K comparable](mapA map[K]bool, mapB map[K]bool) map[K]bool {
	res := make(map[K]bool)

	for k, _ := range mapA {
		if mapB[k] {
			res[k] = true
		}
	}

	return res
}

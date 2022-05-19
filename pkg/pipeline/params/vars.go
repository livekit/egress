package params

type MimeType string
type Profile string
type EgressType string
type OutputType string
type FileExtension string

const (
	// input types
	MimeTypeAAC  MimeType = "audio/aac"
	MimeTypeOpus MimeType = "audio/opus"
	MimeTypeRaw  MimeType = "audio/x-raw"
	MimeTypeH264 MimeType = "video/h264"
	MimeTypeVP8  MimeType = "video/vp8"

	// video profiles
	ProfileBaseline Profile = "baseline"
	ProfileMain     Profile = "main"
	ProfileHigh     Profile = "high"

	// egress types
	EgressTypeStream    EgressType = "stream"
	EgressTypeWebsocket EgressType = "websocket"
	EgressTypeFile      EgressType = "file"

	// output types
	OutputTypeRaw  OutputType = "audio/x-raw"
	OutputTypeOGG  OutputType = "audio/ogg"
	OutputTypeIVF  OutputType = "video/x-ivf"
	OutputTypeMP4  OutputType = "video/mp4"
	OutputTypeTS   OutputType = "video/mp2t"
	OutputTypeWebM OutputType = "video/webm"
	OutputTypeRTMP OutputType = "rtmp"

	// file extensions
	FileExtensionRaw  = ".raw"
	FileExtensionOGG  = ".ogg"
	FileExtensionIVF  = ".ivf"
	FileExtensionMP4  = ".mp4"
	FileExtensionTS   = ".ts"
	FileExtensionWebM = ".webm"
)

var (
	DefaultAudioCodecs = map[OutputType]MimeType{
		OutputTypeRaw:  MimeTypeRaw,
		OutputTypeOGG:  MimeTypeOpus,
		OutputTypeMP4:  MimeTypeAAC,
		OutputTypeTS:   MimeTypeAAC,
		OutputTypeWebM: MimeTypeOpus,
		OutputTypeRTMP: MimeTypeAAC,
	}

	DefaultVideoCodecs = map[OutputType]MimeType{
		OutputTypeIVF:  MimeTypeVP8,
		OutputTypeMP4:  MimeTypeH264,
		OutputTypeTS:   MimeTypeH264,
		OutputTypeWebM: MimeTypeVP8,
		OutputTypeRTMP: MimeTypeH264,
	}

	FileExtensions = map[OutputType]FileExtension{
		OutputTypeRaw:  FileExtensionRaw,
		OutputTypeOGG:  FileExtensionOGG,
		OutputTypeIVF:  FileExtensionIVF,
		OutputTypeMP4:  FileExtensionMP4,
		OutputTypeTS:   FileExtensionTS,
		OutputTypeWebM: FileExtensionWebM,
	}

	codecCompatibility = map[OutputType]map[MimeType]bool{
		OutputTypeRaw: {
			MimeTypeRaw: true,
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
	}
)

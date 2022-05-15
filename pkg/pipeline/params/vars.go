package params

type MimeType string
type Profile string
type OutputType string
type FileExtension string

const (
	// input types
	MimeTypeAAC  MimeType = "audio/aac"
	MimeTypeOpus MimeType = "audio/opus"
	MimeTypeH264 MimeType = "video/h264"
	MimeTypeVP8  MimeType = "video/vp8"

	// video profiles
	ProfileBaseline Profile = "baseline"
	ProfileMain     Profile = "main"
	ProfileHigh     Profile = "high"

	// output types
	OutputTypeOGG  OutputType = "audio/ogg"
	OutputTypeMP4  OutputType = "video/mp4"
	OutputTypeTS   OutputType = "video/mp2t"
	OutputTypeIVF  OutputType = "video/x-ivf"
	OutputTypeWebM OutputType = "video/webm"
	OutputTypeRTMP OutputType = "rtmp"
	OutputTypeRaw  OutputType = "raw"

	// file extensions
	FileExtensionOGG  = ".ogg"
	FileExtensionMP4  = ".mp4"
	FileExtensionTS   = ".ts"
	FileExtensionIVF  = ".ivf"
	FileExtensionWebM = ".webm"
	FileExtensionRaw  = ".raw"
)

var (
	DefaultAudioCodecs = map[OutputType]MimeType{
		OutputTypeOGG:  MimeTypeOpus,
		OutputTypeMP4:  MimeTypeOpus,
		OutputTypeTS:   MimeTypeOpus,
		OutputTypeWebM: MimeTypeOpus,
		OutputTypeRTMP: MimeTypeAAC,
	}

	DefaultVideoCodecs = map[OutputType]MimeType{
		OutputTypeMP4:  MimeTypeH264,
		OutputTypeTS:   MimeTypeH264,
		OutputTypeIVF:  MimeTypeVP8,
		OutputTypeWebM: MimeTypeVP8,
		OutputTypeRTMP: MimeTypeH264,
	}

	FileExtensions = map[OutputType]FileExtension{
		OutputTypeOGG:  FileExtensionOGG,
		OutputTypeMP4:  FileExtensionMP4,
		OutputTypeTS:   FileExtensionTS,
		OutputTypeIVF:  FileExtensionIVF,
		OutputTypeWebM: FileExtensionWebM,
	}

	codecCompatibility = map[OutputType]map[MimeType]bool{
		OutputTypeOGG: {
			MimeTypeOpus: true,
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
		OutputTypeIVF: {
			MimeTypeVP8: true,
		},
		OutputTypeWebM: {
			MimeTypeOpus: true,
			MimeTypeVP8:  true,
		},

		OutputTypeRTMP: {
			MimeTypeAAC:  true,
			MimeTypeH264: true,
		},
		OutputTypeRaw: {
			MimeTypeOpus: true,
		},
	}
)

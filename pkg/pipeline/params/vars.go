package params

import "github.com/livekit/protocol/livekit"

var (
	hd30 = Params{
		AudioParams: AudioParams{
			AudioEnabled:   true,
			AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
			AudioBitrate:   128,
			AudioFrequency: 44100,
		},
		VideoParams: VideoParams{
			VideoEnabled: true,
			VideoCodec:   livekit.VideoCodec_DEFAULT_VC,
			Width:        1280,
			Height:       720,
			Depth:        24,
			Framerate:    30,
			VideoBitrate: 3000,
		},
	}

	hd60 = Params{
		AudioParams: AudioParams{
			AudioEnabled:   true,
			AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
			AudioBitrate:   128,
			AudioFrequency: 44100,
		},
		VideoParams: VideoParams{
			VideoEnabled: true,
			VideoCodec:   livekit.VideoCodec_DEFAULT_VC,
			Width:        1280,
			Height:       720,
			Depth:        24,
			Framerate:    60,
			VideoBitrate: 4500,
		},
	}

	fullHD30 = Params{
		AudioParams: AudioParams{
			AudioEnabled:   true,
			AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
			AudioBitrate:   128,
			AudioFrequency: 44100,
		},
		VideoParams: VideoParams{
			VideoEnabled: true,
			VideoCodec:   livekit.VideoCodec_DEFAULT_VC,
			Width:        1920,
			Height:       1080,
			Depth:        24,
			Framerate:    30,
			VideoBitrate: 4500,
		},
	}

	fullHD60 = Params{
		AudioParams: AudioParams{

			AudioEnabled:   true,
			AudioCodec:     livekit.AudioCodec_DEFAULT_AC,
			AudioBitrate:   128,
			AudioFrequency: 44100,
		},
		VideoParams: VideoParams{
			VideoEnabled: true,
			VideoCodec:   livekit.VideoCodec_DEFAULT_VC,
			Width:        1920,
			Height:       1080,
			Depth:        24,
			Framerate:    60,
			VideoBitrate: 6000,
		},
	}

	mp4 = livekit.EncodedFileType_MP4.String()
	// webm = livekit.EncodedFileType_WEBM.String()
	ogg  = livekit.EncodedFileType_OGG.String()
	rtmp = livekit.StreamProtocol_RTMP.String()
	// srt  = livekit.StreamProtocol_SRT.String()

	DefaultAudioCodecs = map[string]livekit.AudioCodec{
		mp4: livekit.AudioCodec_AAC,
		// webm: livekit.AudioCodec_OPUS,
		ogg:  livekit.AudioCodec_OPUS,
		rtmp: livekit.AudioCodec_AAC,
		// srt:  livekit.AudioCodec(-1), // unknown
	}

	DefaultVideoCodecs = map[string]livekit.VideoCodec{
		mp4: livekit.VideoCodec_H264_MAIN,
		// webm: livekit.VideoCodec_VP8,
		// ogg:  livekit.VideoCodec_VP8,
		rtmp: livekit.VideoCodec_H264_MAIN,
		// srt:  livekit.VideoCodec(-1), // unknown
	}

	compatibleAudioCodecs = map[string]map[livekit.AudioCodec]bool{
		mp4: {
			livekit.AudioCodec_AAC:  true,
			livekit.AudioCodec_OPUS: true,
		},
		// webm: {
		// 	livekit.AudioCodec_OPUS: true,
		// },
		ogg: {
			livekit.AudioCodec_OPUS: true,
		},
		rtmp: {
			livekit.AudioCodec_AAC: true,
		},
		// srt: {
		// 	unknown
		// },
	}

	compatibleVideoCodecs = map[string]map[livekit.VideoCodec]bool{
		mp4: {
			livekit.VideoCodec_H264_BASELINE: true,
			livekit.VideoCodec_H264_MAIN:     true,
			livekit.VideoCodec_H264_HIGH:     true,
			// livekit.VideoCodec_HEVC_MAIN:     true,
			// livekit.VideoCodec_HEVC_HIGH:     true,
		},
		// webm: {
		// 	livekit.VideoCodec_VP8: true,
		// 	livekit.VideoCodec_VP9: true,
		// },
		ogg: {
			// livekit.VideoCodec_VP8: true,
		},
		rtmp: {
			livekit.VideoCodec_H264_BASELINE: true,
			livekit.VideoCodec_H264_MAIN:     true,
			livekit.VideoCodec_H264_HIGH:     true,
		},
		// srt: {
		// 	unknown
		// },
	}
)

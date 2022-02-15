package config

import "github.com/livekit/protocol/livekit"

type RecordingOptions struct {
	Width          int32
	Height         int32
	Depth          int32
	Framerate      int32
	AudioCodec     livekit.AudioCodec
	AudioBitrate   int32
	AudioFrequency int32
	VideoCodec     livekit.VideoCodec
	VideoBitrate   int32

	CustomInputURL string
}

var (
	hd30 = RecordingOptions{
		Width:          1280,
		Height:         720,
		Depth:          24,
		Framerate:      30,
		AudioCodec:     livekit.AudioCodec_AAC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoCodec:     livekit.VideoCodec_H264_MAIN,
		VideoBitrate:   3000,
	}
	hd60 = RecordingOptions{
		Width:          1280,
		Height:         720,
		Depth:          24,
		Framerate:      60,
		AudioCodec:     livekit.AudioCodec_AAC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoCodec:     livekit.VideoCodec_H264_MAIN,
		VideoBitrate:   4500,
	}
	fullHD30 = RecordingOptions{
		Width:          1920,
		Height:         1080,
		Depth:          24,
		Framerate:      30,
		AudioCodec:     livekit.AudioCodec_AAC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoCodec:     livekit.VideoCodec_H264_MAIN,
		VideoBitrate:   4500,
	}
	fullHD60 = RecordingOptions{
		Width:          1920,
		Height:         1080,
		Depth:          24,
		Framerate:      60,
		AudioCodec:     livekit.AudioCodec_AAC,
		AudioBitrate:   128,
		AudioFrequency: 44100,
		VideoCodec:     livekit.VideoCodec_H264_MAIN,
		VideoBitrate:   6000,
	}
)

func (c *Config) GetRecordingOptions(request *livekit.StartEgressRequest) *RecordingOptions {
	opts := fullHD30

	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_WebComposite:
		switch options := req.WebComposite.Options.(type) {
		case *livekit.WebCompositeEgressRequest_Preset:
			switch options.Preset {
			case livekit.EncodingOptionsPreset_H264_720P_30:
				return &hd30
			case livekit.EncodingOptionsPreset_H264_720P_60:
				return &hd60
			case livekit.EncodingOptionsPreset_H264_1080P_30:
				return &fullHD30
			case livekit.EncodingOptionsPreset_H264_1080P_60:
				return &fullHD60
			}

		case *livekit.WebCompositeEgressRequest_Advanced:
			adv := options.Advanced

			// display
			if adv.Width != 0 {
				opts.Width = adv.Width
			}
			if adv.Height != 0 {
				opts.Height = adv.Height
			}
			if adv.Depth != 0 {
				opts.Depth = adv.Depth
			}
			if adv.Framerate != 0 {
				opts.Framerate = adv.Framerate
			}

			// audio
			// opts.AudioCodec = adv.AudioCodec
			if adv.AudioBitrate != 0 {
				opts.AudioBitrate = adv.AudioBitrate
			}
			if adv.AudioFrequency != 0 {
				opts.AudioFrequency = adv.AudioFrequency
			}

			// video
			// opts.VideoCodec = adv.VideoCodec
			if adv.VideoBitrate != 0 {
				opts.VideoBitrate = adv.VideoBitrate
			}
		}
	}

	return &opts
}

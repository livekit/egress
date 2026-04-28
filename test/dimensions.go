// Copyright 2026 LiveKit, Inc.
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

//go:build integration

package test

import (
	"fmt"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

// RequestTypes defines all request type dimension values.
var RequestTypes = struct {
	RoomComposite             DimensionValue
	RoomCompositeAudioOnly    DimensionValue
	RoomCompositeVideoOnly    DimensionValue
	Web                       DimensionValue
	WebV2                     DimensionValue
	WebV2AudioOnly            DimensionValue
	WebV2VideoOnly            DimensionValue
	Participant               DimensionValue
	ParticipantAudioOnly      DimensionValue
	TrackComposite            DimensionValue
	TrackCompositeAudioOnly   DimensionValue
	TrackCompositeVideoOnly   DimensionValue
	TrackAudio                DimensionValue
	TrackVideo                DimensionValue
	TemplateGrid              DimensionValue
	TemplateSpeaker           DimensionValue
	TemplateSingleSpeaker     DimensionValue
	Media                     DimensionValue
	MediaAudioOnly            DimensionValue
	MediaVideoOnly            DimensionValue
	MediaAudioRouteByTrackID  DimensionValue
	MediaAudioRouteByIdentity DimensionValue
	MediaAudioRouteByKind     DimensionValue
	MediaMultiRoute           DimensionValue
	MediaParticipantVideo     DimensionValue
}{
	RoomComposite: DimensionValue{
		Name: "RoomComposite", Tags: []string{"audio+video", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeRoomComposite
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.Layout = layoutSpeaker
			tc.MultiParticipant = true
		},
	},
	RoomCompositeAudioOnly: DimensionValue{
		Name: "RoomCompositeAudioOnly", Tags: []string{"audio", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeRoomComposite
			tc.AudioCodec = types.MimeTypeOpus
			tc.AudioOnly = true
		},
	},
	RoomCompositeVideoOnly: DimensionValue{
		Name: "RoomCompositeVideoOnly", Tags: []string{"video", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeRoomComposite
			tc.VideoCodec = types.MimeTypeH264
			tc.VideoOnly = true
			tc.Layout = layoutSpeaker
		},
	},
	Web: DimensionValue{
		Name: "Web", Tags: []string{"video", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeWeb
			tc.VideoOnly = true
		},
	},
	WebV2: DimensionValue{
		Name: "WebV2", Tags: []string{"video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeWeb
			tc.V2 = true
			tc.VideoOnly = true
		},
	},
	WebV2AudioOnly: DimensionValue{
		Name: "WebV2AudioOnly", Tags: []string{"audio", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeWeb
			tc.V2 = true
			tc.AudioOnly = true
			tc.EncodingOptions = &livekit.EncodingOptions{AudioCodec: livekit.AudioCodec_OPUS}
		},
	},
	WebV2VideoOnly: DimensionValue{
		Name: "WebV2VideoOnly", Tags: []string{"video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeWeb
			tc.V2 = true
			tc.VideoOnly = true
		},
	},
	Participant: DimensionValue{
		Name: "Participant", Tags: []string{"audio+video", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeParticipant
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
		},
	},
	ParticipantAudioOnly: DimensionValue{
		Name: "ParticipantAudioOnly", Tags: []string{"audio", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeParticipant
			tc.AudioCodec = types.MimeTypeOpus
			tc.AudioOnly = true
		},
	},
	TrackComposite: DimensionValue{
		Name: "TrackComposite", Tags: []string{"audio+video", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTrackComposite
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeVP8
		},
	},
	TrackCompositeAudioOnly: DimensionValue{
		Name: "TrackCompositeAudioOnly", Tags: []string{"audio", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTrackComposite
			tc.AudioCodec = types.MimeTypeOpus
			tc.AudioOnly = true
		},
	},
	TrackCompositeVideoOnly: DimensionValue{
		Name: "TrackCompositeVideoOnly", Tags: []string{"video", "composite"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTrackComposite
			tc.VideoCodec = types.MimeTypeH264
			tc.VideoOnly = true
		},
	},
	TrackAudio: DimensionValue{
		Name: "TrackAudio", Tags: []string{"audio", "track"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTrack
			tc.AudioCodec = types.MimeTypeOpus
			tc.AudioOnly = true
		},
	},
	TrackVideo: DimensionValue{
		Name: "TrackVideo", Tags: []string{"video", "track"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTrack
			tc.VideoCodec = types.MimeTypeH264
			tc.VideoOnly = true
		},
	},
	TemplateGrid: DimensionValue{
		Name: "TemplateGrid", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTemplate
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.Layout = layoutGrid
			tc.MultiParticipant = true
		},
	},
	TemplateSpeaker: DimensionValue{
		Name: "TemplateSpeaker", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTemplate
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.Layout = layoutSpeaker
			tc.MultiParticipant = true
		},
	},
	TemplateSingleSpeaker: DimensionValue{
		Name: "TemplateSingleSpeaker", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeTemplate
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.Layout = layoutSingleSpeaker
			tc.MultiParticipant = true
		},
	},
	Media: DimensionValue{
		Name: "Media", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.AudioRoutes = []*livekit.AudioRoute{{
				Match: &livekit.AudioRoute_TrackId{TrackId: setAtRuntime},
			}}
		},
	},
	MediaAudioOnly: DimensionValue{
		Name: "MediaAudioOnly", Tags: []string{"audio", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.AudioOnly = true
			tc.AudioRoutes = []*livekit.AudioRoute{{
				Match: &livekit.AudioRoute_TrackId{TrackId: setAtRuntime},
			}}
			tc.EncodingOptions = &livekit.EncodingOptions{AudioCodec: livekit.AudioCodec_OPUS}
		},
	},
	MediaVideoOnly: DimensionValue{
		Name: "MediaVideoOnly", Tags: []string{"video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.VideoCodec = types.MimeTypeH264
			tc.VideoOnly = true
		},
	},
	MediaAudioRouteByTrackID: DimensionValue{
		Name: "MediaAudioRouteByTrackID", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.AudioRoutes = []*livekit.AudioRoute{{
				Match:   &livekit.AudioRoute_TrackId{TrackId: setAtRuntime},
				Channel: livekit.AudioChannel_AUDIO_CHANNEL_LEFT,
			}}
		},
	},
	MediaAudioRouteByIdentity: DimensionValue{
		Name: "MediaAudioRouteByIdentity", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.AudioRoutes = []*livekit.AudioRoute{{
				Match:   &livekit.AudioRoute_ParticipantIdentity{ParticipantIdentity: setAtRuntime},
				Channel: livekit.AudioChannel_AUDIO_CHANNEL_BOTH,
			}}
		},
	},
	MediaAudioRouteByKind: DimensionValue{
		Name: "MediaAudioRouteByKind", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.MultiParticipant = true
			tc.AudioRoutes = []*livekit.AudioRoute{{
				Match:   &livekit.AudioRoute_ParticipantKind{ParticipantKind: livekit.ParticipantInfo_STANDARD},
				Channel: livekit.AudioChannel_AUDIO_CHANNEL_BOTH,
			}}
		},
	},
	MediaMultiRoute: DimensionValue{
		Name: "MediaMultiRoute", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.AudioRoutes = []*livekit.AudioRoute{
				{Match: &livekit.AudioRoute_TrackId{TrackId: setAtRuntime}, Channel: livekit.AudioChannel_AUDIO_CHANNEL_LEFT},
				{Match: &livekit.AudioRoute_ParticipantIdentity{ParticipantIdentity: setAtRuntime}, Channel: livekit.AudioChannel_AUDIO_CHANNEL_RIGHT},
			}
		},
	},
	MediaParticipantVideo: DimensionValue{
		Name: "MediaParticipantVideo", Tags: []string{"audio+video", "composite", "v2"},
		Apply: func(tc *TestConfig) {
			tc.RequestType = types.RequestTypeMedia
			tc.V2 = true
			tc.AudioCodec = types.MimeTypeOpus
			tc.VideoCodec = types.MimeTypeH264
			tc.ParticipantVideo = &livekit.ParticipantVideo{Identity: setAtRuntime}
			tc.AudioRoutes = []*livekit.AudioRoute{{
				Match: &livekit.AudioRoute_TrackId{TrackId: setAtRuntime},
			}}
		},
	},
}

// PublisherCodecs defines publisher codec dimension values.
var PublisherCodecs = struct {
	PublishOpus DimensionValue
	PublishPCMU DimensionValue
	PublishPCMA DimensionValue
	PublishH264 DimensionValue
	PublishVP8  DimensionValue
}{
	PublishOpus: DimensionValue{Name: "PublishOpus", Tags: []string{"audio"}, Apply: func(tc *TestConfig) { tc.AudioCodec = types.MimeTypeOpus }},
	PublishPCMU: DimensionValue{Name: "PublishPCMU", Tags: []string{"audio"}, Apply: func(tc *TestConfig) { tc.AudioCodec = types.MimeTypePCMU }},
	PublishPCMA: DimensionValue{Name: "PublishPCMA", Tags: []string{"audio"}, Apply: func(tc *TestConfig) { tc.AudioCodec = types.MimeTypePCMA }},
	PublishH264: DimensionValue{Name: "PublishH264", Tags: []string{"video"}, Apply: func(tc *TestConfig) { tc.VideoCodec = types.MimeTypeH264 }},
	PublishVP8:  DimensionValue{Name: "PublishVP8", Tags: []string{"video"}, Apply: func(tc *TestConfig) { tc.VideoCodec = types.MimeTypeVP8 }},
}

// OutputTypes defines output type dimension values (singles and pairs).
var OutputTypes = struct {
	File              DimensionValue
	Stream            DimensionValue
	Segments          DimensionValue
	Images            DimensionValue
	FileAndStream     DimensionValue
	FileAndSegments   DimensionValue
	FileAndImages     DimensionValue
	StreamAndSegments DimensionValue
	StreamAndImages   DimensionValue
	SegmentsAndImages DimensionValue
}{
	File: DimensionValue{
		Name: "File", Tags: []string{"composite"},
		Apply: func(tc *TestConfig) {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{
				Filename: "file_{room_name}_{time}.mp4",
			})
		},
	},
	Stream: DimensionValue{
		Name: "Stream", Tags: []string{"composite"},
		Apply: func(tc *TestConfig) {
			tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{
				Protocol: livekit.StreamProtocol_RTMP,
				Urls:     []string{rtmpUrl1, badRtmpUrl1},
			})
		},
	},
	Segments: DimensionValue{
		Name: "Segments", Tags: []string{"composite"},
		Apply: func(tc *TestConfig) {
			tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{
				Prefix:   "seg_{room_name}_{time}",
				Playlist: "seg_{room_name}_{time}.m3u8",
			})
		},
	},
	Images: DimensionValue{
		Name: "Images", Tags: []string{"composite", "video"},
		Apply: func(tc *TestConfig) {
			tc.ImageOutputs = append(tc.ImageOutputs, imageOutputConfig{
				Prefix: "img_{room_name}_{time}",
			})
		},
	},
	FileAndStream: DimensionValue{
		Name: "FileAndStream", Tags: []string{"composite"},
		Apply: func(tc *TestConfig) {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "file_{room_name}_{time}.mp4"})
			tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{Protocol: livekit.StreamProtocol_RTMP, Urls: []string{rtmpUrl1, badRtmpUrl1}})
		},
	},
	FileAndSegments: DimensionValue{
		Name: "FileAndSegments", Tags: []string{"composite"},
		Apply: func(tc *TestConfig) {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "file_{room_name}_{time}.mp4"})
			tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{Prefix: "seg_{room_name}_{time}", Playlist: "seg_{room_name}_{time}.m3u8"})
		},
	},
	FileAndImages: DimensionValue{
		Name: "FileAndImages", Tags: []string{"composite", "video"},
		Apply: func(tc *TestConfig) {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "file_{room_name}_{time}.mp4"})
			tc.ImageOutputs = append(tc.ImageOutputs, imageOutputConfig{Prefix: "img_{room_name}_{time}"})
		},
	},
	StreamAndSegments: DimensionValue{
		Name: "StreamAndSegments", Tags: []string{"composite"},
		Apply: func(tc *TestConfig) {
			tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{Protocol: livekit.StreamProtocol_RTMP, Urls: []string{rtmpUrl1, badRtmpUrl1}})
			tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{Prefix: "seg_{room_name}_{time}", Playlist: "seg_{room_name}_{time}.m3u8"})
		},
	},
	StreamAndImages: DimensionValue{
		Name: "StreamAndImages", Tags: []string{"composite", "video"},
		Apply: func(tc *TestConfig) {
			tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{Protocol: livekit.StreamProtocol_RTMP, Urls: []string{rtmpUrl1, badRtmpUrl1}})
			tc.ImageOutputs = append(tc.ImageOutputs, imageOutputConfig{Prefix: "img_{room_name}_{time}"})
		},
	},
	SegmentsAndImages: DimensionValue{
		Name: "SegmentsAndImages", Tags: []string{"composite", "video"},
		Apply: func(tc *TestConfig) {
			tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{Prefix: "seg_{room_name}_{time}", Playlist: "seg_{room_name}_{time}.m3u8"})
			tc.ImageOutputs = append(tc.ImageOutputs, imageOutputConfig{Prefix: "img_{room_name}_{time}"})
		},
	},
}

// OutputEncodings defines output encoding dimension values.
var OutputEncodings = struct {
	CustomBitrate          DimensionValue
	EncodingAAC            DimensionValue
	EncodingH264Baseline   DimensionValue
	EncodingH264High       DimensionValue
	EncodingH264Main       DimensionValue
	EncodingOpus           DimensionValue
	FileIVF                DimensionValue
	FileMP3                DimensionValue
	FileMP4                DimensionValue
	FileOGG                DimensionValue
	FileTS                 DimensionValue
	FileWebM               DimensionValue
	ImageSuffixIndex       DimensionValue
	ImageSuffixTimestamp   DimensionValue
	KeyFrameInterval2s     DimensionValue
	Resolution1080p        DimensionValue
	Resolution360p         DimensionValue
	Resolution720p         DimensionValue
	SegmentLivePlaylist    DimensionValue
	SegmentSuffixIndex     DimensionValue
	SegmentSuffixTimestamp DimensionValue
	StreamRTMP             DimensionValue
	StreamSRT              DimensionValue
	StreamWebsocket        DimensionValue
}{
	// File types
	FileMP4: DimensionValue{Name: "FileMP4", Tags: []string{"composite"}, Apply: func(tc *TestConfig) {
		if len(tc.FileOutputs) > 0 {
			tc.FileOutputs[0].FileType = livekit.EncodedFileType_MP4
		} else {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "enc_{room_name}_{time}.mp4", FileType: livekit.EncodedFileType_MP4})
		}
	}},
	FileOGG: DimensionValue{Name: "FileOGG", Tags: []string{"audio", "composite"}, Apply: func(tc *TestConfig) {
		if len(tc.FileOutputs) > 0 {
			tc.FileOutputs[0].FileType = livekit.EncodedFileType_OGG
			tc.FileOutputs[0].Filename = "enc_{room_name}_{time}.ogg"
		} else {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "enc_{room_name}_{time}.ogg", FileType: livekit.EncodedFileType_OGG})
		}
	}},
	FileMP3: DimensionValue{Name: "FileMP3", Tags: []string{"audio", "composite"}, Apply: func(tc *TestConfig) {
		if len(tc.FileOutputs) > 0 {
			tc.FileOutputs[0].FileType = livekit.EncodedFileType_MP3
			tc.FileOutputs[0].Filename = "enc_{room_name}_{time}.mp3"
		} else {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "enc_{room_name}_{time}.mp3", FileType: livekit.EncodedFileType_MP3})
		}
	}},
	FileWebM: DimensionValue{Name: "FileWebM", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		if len(tc.FileOutputs) > 0 {
			tc.FileOutputs[0].Filename = "enc_{room_name}_{time}.webm"
		} else {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "enc_{room_name}_{time}.webm"})
		}
	}},
	FileTS: DimensionValue{Name: "FileTS", Tags: []string{"composite"}, Apply: func(tc *TestConfig) {
		if len(tc.FileOutputs) > 0 {
			tc.FileOutputs[0].Filename = "enc_{room_name}_{time}.ts"
		} else {
			tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "enc_{room_name}_{time}.ts"})
		}
	}},
	FileIVF: DimensionValue{Name: "FileIVF", Tags: []string{"video", "track"}, Apply: func(tc *TestConfig) {
		tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "enc_{track_id}_{time}.ivf"})
	}},

	// Stream protocols
	StreamRTMP: DimensionValue{Name: "StreamRTMP", Tags: []string{"composite"}, Apply: func(tc *TestConfig) {
		if len(tc.StreamOutputs) > 0 {
			tc.StreamOutputs[0].Protocol = livekit.StreamProtocol_RTMP
		} else {
			tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{Protocol: livekit.StreamProtocol_RTMP, Urls: []string{rtmpUrl1, badRtmpUrl1}})
		}
	}},
	StreamSRT: DimensionValue{Name: "StreamSRT", Tags: []string{"composite"}, Apply: func(tc *TestConfig) {
		if len(tc.StreamOutputs) > 0 {
			tc.StreamOutputs[0].Protocol = livekit.StreamProtocol_SRT
			tc.StreamOutputs[0].Urls = []string{srtPublishUrl1, badSrtUrl1}
		} else {
			tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{Protocol: livekit.StreamProtocol_SRT, Urls: []string{srtPublishUrl1, badSrtUrl1}})
		}
	}},
	StreamWebsocket: DimensionValue{Name: "StreamWebsocket", Tags: []string{"track", "audio"}, Apply: func(tc *TestConfig) {
		tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{
			Urls: []string{fmt.Sprintf("ws://placeholder-%d.raw", time.Now().Unix())},
		})
	}},

	// Segment options
	SegmentSuffixIndex: DimensionValue{Name: "SegmentSuffixIndex", Tags: []string{"composite"}, Apply: func(tc *TestConfig) {
		if len(tc.SegmentOutputs) > 0 {
			tc.SegmentOutputs[0].Suffix = livekit.SegmentedFileSuffix_INDEX
		} else {
			tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{Prefix: "seg_{room_name}_{time}", Playlist: "seg_{room_name}_{time}.m3u8", Suffix: livekit.SegmentedFileSuffix_INDEX})
		}
	}},
	SegmentSuffixTimestamp: DimensionValue{Name: "SegmentSuffixTimestamp", Tags: []string{"composite"}, Apply: func(tc *TestConfig) {
		if len(tc.SegmentOutputs) > 0 {
			tc.SegmentOutputs[0].Suffix = livekit.SegmentedFileSuffix_TIMESTAMP
		} else {
			tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{Prefix: "seg_{room_name}_{time}", Playlist: "seg_{room_name}_{time}.m3u8", Suffix: livekit.SegmentedFileSuffix_TIMESTAMP})
		}
	}},
	SegmentLivePlaylist: DimensionValue{Name: "SegmentLivePlaylist", Tags: []string{"composite"}, Apply: func(tc *TestConfig) {
		if len(tc.SegmentOutputs) > 0 {
			tc.SegmentOutputs[0].LivePlaylist = "live_{room_name}_{time}.m3u8"
		} else {
			tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{Prefix: "seg_{room_name}_{time}", Playlist: "seg_{room_name}_{time}.m3u8", LivePlaylist: "live_{room_name}_{time}.m3u8"})
		}
	}},

	// Image options
	ImageSuffixIndex: DimensionValue{Name: "ImageSuffixIndex", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		if len(tc.ImageOutputs) > 0 {
			tc.ImageOutputs[0].Suffix = livekit.ImageFileSuffix_IMAGE_SUFFIX_INDEX
		} else {
			tc.ImageOutputs = append(tc.ImageOutputs, imageOutputConfig{Prefix: "img_{room_name}_{time}", Suffix: livekit.ImageFileSuffix_IMAGE_SUFFIX_INDEX})
		}
	}},
	ImageSuffixTimestamp: DimensionValue{Name: "ImageSuffixTimestamp", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		if len(tc.ImageOutputs) > 0 {
			tc.ImageOutputs[0].Suffix = livekit.ImageFileSuffix_IMAGE_SUFFIX_TIMESTAMP
		} else {
			tc.ImageOutputs = append(tc.ImageOutputs, imageOutputConfig{Prefix: "img_{room_name}_{time}", Suffix: livekit.ImageFileSuffix_IMAGE_SUFFIX_TIMESTAMP})
		}
	}},

	// Video encoding
	EncodingH264Main: DimensionValue{Name: "EncodingH264Main", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		tc.ensureEncodingOptions().VideoCodec = livekit.VideoCodec_H264_MAIN
	}},
	EncodingH264High: DimensionValue{Name: "EncodingH264High", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		tc.ensureEncodingOptions().VideoCodec = livekit.VideoCodec_H264_HIGH
	}},
	EncodingH264Baseline: DimensionValue{Name: "EncodingH264Baseline", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		tc.ensureEncodingOptions().VideoCodec = livekit.VideoCodec_H264_BASELINE
	}},

	// Audio encoding
	EncodingOpus: DimensionValue{Name: "EncodingOpus", Tags: []string{"audio", "composite"}, Apply: func(tc *TestConfig) {
		tc.ensureEncodingOptions().AudioCodec = livekit.AudioCodec_OPUS
	}},
	EncodingAAC: DimensionValue{Name: "EncodingAAC", Tags: []string{"audio", "composite"}, Apply: func(tc *TestConfig) {
		tc.ensureEncodingOptions().AudioCodec = livekit.AudioCodec_AAC
	}},

	// Resolution
	Resolution1080p: DimensionValue{Name: "Resolution1080p", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		e := tc.ensureEncodingOptions()
		e.Width = 1920
		e.Height = 1080
	}},
	Resolution720p: DimensionValue{Name: "Resolution720p", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		e := tc.ensureEncodingOptions()
		e.Width = 1280
		e.Height = 720
	}},
	Resolution360p: DimensionValue{Name: "Resolution360p", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		e := tc.ensureEncodingOptions()
		e.Width = 640
		e.Height = 360
	}},

	// Other
	KeyFrameInterval2s: DimensionValue{Name: "KeyFrameInterval2s", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		tc.ensureEncodingOptions().KeyFrameInterval = 2
	}},
	CustomBitrate: DimensionValue{Name: "CustomBitrate", Tags: []string{"video", "composite"}, Apply: func(tc *TestConfig) {
		tc.ensureEncodingOptions().VideoBitrate = 4500
	}},
}

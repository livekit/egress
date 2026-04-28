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

package main

var allDimensions = [][]DimValue{requestTypes, publisherCodecs, outputTypes, outputEncodings}

var requestTypes = []DimValue{
	{Dimension: "RequestType", Name: "RoomComposite", Tags: []string{"audio+video", "composite"}},
	{Dimension: "RequestType", Name: "RoomCompositeAudioOnly", Tags: []string{"audio", "composite"}},
	{Dimension: "RequestType", Name: "RoomCompositeVideoOnly", Tags: []string{"video", "composite"}},
	{Dimension: "RequestType", Name: "Web", Tags: []string{"video", "composite"}},
	{Dimension: "RequestType", Name: "WebV2", Tags: []string{"video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "WebV2AudioOnly", Tags: []string{"audio", "composite", "v2"}},
	{Dimension: "RequestType", Name: "WebV2VideoOnly", Tags: []string{"video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "Participant", Tags: []string{"audio+video", "composite"}},
	{Dimension: "RequestType", Name: "ParticipantAudioOnly", Tags: []string{"audio", "composite"}},
	{Dimension: "RequestType", Name: "TrackComposite", Tags: []string{"audio+video", "composite"}},
	{Dimension: "RequestType", Name: "TrackCompositeAudioOnly", Tags: []string{"audio", "composite"}},
	{Dimension: "RequestType", Name: "TrackCompositeVideoOnly", Tags: []string{"video", "composite"}},
	{Dimension: "RequestType", Name: "TrackAudio", Tags: []string{"audio", "track"}},
	{Dimension: "RequestType", Name: "TrackVideo", Tags: []string{"video", "track"}},
	{Dimension: "RequestType", Name: "TemplateGrid", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "TemplateSpeaker", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "TemplateSingleSpeaker", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "Media", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "MediaAudioOnly", Tags: []string{"audio", "composite", "v2"}},
	{Dimension: "RequestType", Name: "MediaVideoOnly", Tags: []string{"video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "MediaAudioRouteByTrackID", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "MediaAudioRouteByIdentity", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "MediaAudioRouteByKind", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "MediaMultiRoute", Tags: []string{"audio+video", "composite", "v2"}},
	{Dimension: "RequestType", Name: "MediaParticipantVideo", Tags: []string{"audio+video", "composite", "v2"}},
}

var publisherCodecs = []DimValue{
	{Dimension: "PublisherCodec", Name: "PublishOpus", Tags: []string{"audio"}},
	{Dimension: "PublisherCodec", Name: "PublishPCMU", Tags: []string{"audio"}},
	{Dimension: "PublisherCodec", Name: "PublishPCMA", Tags: []string{"audio"}},
	{Dimension: "PublisherCodec", Name: "PublishH264", Tags: []string{"video"}},
	{Dimension: "PublisherCodec", Name: "PublishVP8", Tags: []string{"video"}},
}

var outputTypes = []DimValue{
	{Dimension: "OutputType", Name: "File", Tags: []string{"composite"}},
	{Dimension: "OutputType", Name: "Stream", Tags: []string{"composite"}},
	{Dimension: "OutputType", Name: "Segments", Tags: []string{"composite"}},
	{Dimension: "OutputType", Name: "Images", Tags: []string{"composite", "video"}},
	{Dimension: "OutputType", Name: "FileAndStream", Tags: []string{"composite"}},
	{Dimension: "OutputType", Name: "FileAndSegments", Tags: []string{"composite"}},
	{Dimension: "OutputType", Name: "FileAndImages", Tags: []string{"composite", "video"}},
	{Dimension: "OutputType", Name: "StreamAndSegments", Tags: []string{"composite"}},
	{Dimension: "OutputType", Name: "StreamAndImages", Tags: []string{"composite", "video"}},
	{Dimension: "OutputType", Name: "SegmentsAndImages", Tags: []string{"composite", "video"}},
}

var outputEncodings = []DimValue{
	{Dimension: "OutputEncoding", Name: "FileMP4", Tags: []string{"composite"}},
	{Dimension: "OutputEncoding", Name: "FileOGG", Tags: []string{"audio", "composite"}},
	{Dimension: "OutputEncoding", Name: "FileMP3", Tags: []string{"audio", "composite"}},
	{Dimension: "OutputEncoding", Name: "FileWebM", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "FileTS", Tags: []string{"composite"}},
	{Dimension: "OutputEncoding", Name: "FileIVF", Tags: []string{"video", "track"}},
	{Dimension: "OutputEncoding", Name: "StreamRTMP", Tags: []string{"composite"}},
	{Dimension: "OutputEncoding", Name: "StreamSRT", Tags: []string{"composite"}},
	{Dimension: "OutputEncoding", Name: "StreamWebsocket", Tags: []string{"track", "audio"}},
	{Dimension: "OutputEncoding", Name: "SegmentSuffixIndex", Tags: []string{"composite"}},
	{Dimension: "OutputEncoding", Name: "SegmentSuffixTimestamp", Tags: []string{"composite"}},
	{Dimension: "OutputEncoding", Name: "SegmentLivePlaylist", Tags: []string{"composite"}},
	{Dimension: "OutputEncoding", Name: "ImageSuffixIndex", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "ImageSuffixTimestamp", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "EncodingH264Main", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "EncodingH264High", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "EncodingH264Baseline", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "EncodingOpus", Tags: []string{"audio", "composite"}},
	{Dimension: "OutputEncoding", Name: "EncodingAAC", Tags: []string{"audio", "composite"}},
	{Dimension: "OutputEncoding", Name: "Resolution1080p", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "Resolution720p", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "Resolution360p", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "KeyFrameInterval2s", Tags: []string{"video", "composite"}},
	{Dimension: "OutputEncoding", Name: "CustomBitrate", Tags: []string{"video", "composite"}},
}

// Copyright 2024 LiveKit, Inc.
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
	"path"
	"testing"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

const (
	webUrl       = "https://download.blender.org/peach/bigbuckbunny_movies/BigBuckBunny_320x180.mp4"
	setAtRuntime = "set-at-runtime"
)

type testCase struct {
	name        string
	requestType types.RequestType

	publishOptions

	// encoding options
	encodingOptions *livekit.EncodingOptions
	encodingPreset  livekit.EncodingOptionsPreset

	*fileOptions
	*streamOptions
	*segmentOptions
	*imageOptions
	*v2OutputOptions

	multi  bool
	custom func(*testing.T, *testCase)

	contentCheck func(t *testing.T, path string, info *FFProbeInfo)
}

type publishOptions struct {
	audioCodec     types.MimeType
	audioDelay     time.Duration
	audioUnpublish time.Duration
	audioRepublish time.Duration
	audioOnly      bool
	audioMixing    livekit.AudioMixing
	audioTrackID   string

	videoCodec     types.MimeType
	videoDelay     time.Duration
	videoUnpublish time.Duration
	videoRepublish time.Duration
	videoOnly      bool
	videoTrackID   string

	layout string

	multiParticipant bool

	// v2 Media source fields
	mediaVideoTrackID     string
	mediaParticipantVideo *livekit.ParticipantVideo
	audioRoutes           []*livekit.AudioRoute

	// v2 Template source fields
	templateCustomBaseUrl string
}

type fileOptions struct {
	filename   string
	fileType   livekit.EncodedFileType
	outputType types.OutputType
}

type streamOptions struct {
	streamUrls   []string
	rawFileName  string
	websocketUrl string
	outputType   types.OutputType
}

type segmentOptions struct {
	prefix       string
	playlist     string
	livePlaylist string
	suffix       livekit.SegmentedFileSuffix
}

type imageOptions struct {
	prefix string
	suffix livekit.ImageFileSuffix
}

type v2OutputOptions struct {
	outputs []*livekit.Output
	storage *livekit.StorageConfig
}

func (r *Runner) build(test *testCase) *rpc.StartEgressRequest {
	switch test.requestType {
	case types.RequestTypeRoomComposite:
		room := &livekit.RoomCompositeEgressRequest{
			RoomName:    r.RoomName,
			Layout:      test.layout,
			AudioOnly:   test.audioOnly,
			AudioMixing: test.audioMixing,
			VideoOnly:   test.videoOnly,
		}
		if test.encodingOptions != nil {
			room.Options = &livekit.RoomCompositeEgressRequest_Advanced{
				Advanced: test.encodingOptions,
			}
		} else if test.encodingPreset != 0 {
			room.Options = &livekit.RoomCompositeEgressRequest_Preset{
				Preset: test.encodingPreset,
			}
		}
		if test.fileOptions != nil {
			room.FileOutputs = r.buildFileOutputs(test.fileOptions)
		}
		if test.streamOptions != nil {
			room.StreamOutputs = r.buildStreamOutputs(test.streamOptions)
		}
		if test.segmentOptions != nil {
			room.SegmentOutputs = r.buildSegmentOutputs(test.segmentOptions)
		}
		if test.imageOptions != nil {
			room.ImageOutputs = r.buildImageOutputs(test.imageOptions)
		}
		return &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request:  &rpc.StartEgressRequest_RoomComposite{RoomComposite: room},
		}

	case types.RequestTypeWeb:
		web := &livekit.WebEgressRequest{
			Url:       webUrl,
			AudioOnly: test.audioOnly,
			VideoOnly: test.videoOnly,
		}
		if test.encodingOptions != nil {
			web.Options = &livekit.WebEgressRequest_Advanced{
				Advanced: test.encodingOptions,
			}
		} else if test.encodingPreset != 0 {
			web.Options = &livekit.WebEgressRequest_Preset{
				Preset: test.encodingPreset,
			}
		}
		if test.fileOptions != nil {
			web.FileOutputs = r.buildFileOutputs(test.fileOptions)
		}
		if test.streamOptions != nil {
			web.StreamOutputs = r.buildStreamOutputs(test.streamOptions)
		}
		if test.segmentOptions != nil {
			web.SegmentOutputs = r.buildSegmentOutputs(test.segmentOptions)
		}
		if test.imageOptions != nil {
			web.ImageOutputs = r.buildImageOutputs(test.imageOptions)
		}
		return &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request:  &rpc.StartEgressRequest_Web{Web: web},
		}

	case types.RequestTypeParticipant:
		participant := &livekit.ParticipantEgressRequest{
			RoomName: r.RoomName,
			Identity: r.room.LocalParticipant.Identity(),
		}
		if test.encodingOptions != nil {
			participant.Options = &livekit.ParticipantEgressRequest_Advanced{
				Advanced: test.encodingOptions,
			}
		} else if test.encodingPreset != 0 {
			participant.Options = &livekit.ParticipantEgressRequest_Preset{
				Preset: test.encodingPreset,
			}
		}
		if test.fileOptions != nil {
			participant.FileOutputs = r.buildFileOutputs(test.fileOptions)
		}
		if test.streamOptions != nil {
			participant.StreamOutputs = r.buildStreamOutputs(test.streamOptions)
		}
		if test.segmentOptions != nil {
			participant.SegmentOutputs = r.buildSegmentOutputs(test.segmentOptions)
		}
		if test.imageOptions != nil {
			participant.ImageOutputs = r.buildImageOutputs(test.imageOptions)
		}
		return &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request:  &rpc.StartEgressRequest_Participant{Participant: participant},
		}

	case types.RequestTypeTrackComposite:
		trackComposite := &livekit.TrackCompositeEgressRequest{
			RoomName:     r.RoomName,
			AudioTrackId: test.audioTrackID,
			VideoTrackId: test.videoTrackID,
		}
		if test.encodingOptions != nil {
			trackComposite.Options = &livekit.TrackCompositeEgressRequest_Advanced{
				Advanced: test.encodingOptions,
			}
		} else if test.encodingPreset != 0 {
			trackComposite.Options = &livekit.TrackCompositeEgressRequest_Preset{
				Preset: test.encodingPreset,
			}
		}
		if test.fileOptions != nil {
			trackComposite.FileOutputs = r.buildFileOutputs(test.fileOptions)
		}
		if test.streamOptions != nil {
			trackComposite.StreamOutputs = r.buildStreamOutputs(test.streamOptions)
		}
		if test.segmentOptions != nil {
			trackComposite.SegmentOutputs = r.buildSegmentOutputs(test.segmentOptions)
		}
		if test.imageOptions != nil {
			trackComposite.ImageOutputs = r.buildImageOutputs(test.imageOptions)
		}
		return &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request:  &rpc.StartEgressRequest_TrackComposite{TrackComposite: trackComposite},
		}

	case types.RequestTypeTrack:
		trackID := test.audioTrackID
		if trackID == "" {
			trackID = test.videoTrackID
		}
		track := &livekit.TrackEgressRequest{
			RoomName: r.RoomName,
			TrackId:  trackID,
		}
		if test.fileOptions != nil {
			track.Output = &livekit.TrackEgressRequest_File{
				File: &livekit.DirectFileOutput{
					Filepath: path.Join(r.FilePrefix, test.filename),
				},
			}
		} else if test.streamOptions != nil {
			track.Output = &livekit.TrackEgressRequest_WebsocketUrl{
				WebsocketUrl: test.websocketUrl,
			}
		}
		return &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request:  &rpc.StartEgressRequest_Track{Track: track},
		}
	}

	panic("unknown request type")
}

func (r *Runner) buildFileOutputs(o *fileOptions) []*livekit.EncodedFileOutput {
	if u := r.getUploadConfig(); u != nil {
		output := &livekit.EncodedFileOutput{
			FileType: o.fileType,
			Filepath: path.Join(uploadPrefix, o.filename),
		}

		switch conf := u.(type) {
		case *livekit.S3Upload:
			output.Output = &livekit.EncodedFileOutput_S3{S3: conf}
		case *livekit.GCPUpload:
			output.Output = &livekit.EncodedFileOutput_Gcp{Gcp: conf}
		case *livekit.AzureBlobUpload:
			output.Output = &livekit.EncodedFileOutput_Azure{Azure: conf}
		}

		return []*livekit.EncodedFileOutput{output}
	}

	return []*livekit.EncodedFileOutput{{
		FileType: o.fileType,
		Filepath: path.Join(r.FilePrefix, o.filename),
	}}
}

func (r *Runner) buildStreamOutputs(o *streamOptions) []*livekit.StreamOutput {
	var protocol livekit.StreamProtocol
	switch o.outputType {
	case types.OutputTypeRTMP:
		protocol = livekit.StreamProtocol_RTMP
	case types.OutputTypeSRT:
		protocol = livekit.StreamProtocol_SRT
	default:
		protocol = livekit.StreamProtocol_DEFAULT_PROTOCOL
	}

	return []*livekit.StreamOutput{{
		Protocol: protocol,
		Urls:     o.streamUrls,
	}}
}

func (r *Runner) buildSegmentOutputs(o *segmentOptions) []*livekit.SegmentedFileOutput {
	if u := r.getUploadConfig(); u != nil {
		output := &livekit.SegmentedFileOutput{
			FilenamePrefix:   path.Join(uploadPrefix, o.prefix),
			PlaylistName:     o.playlist,
			LivePlaylistName: o.livePlaylist,
			FilenameSuffix:   o.suffix,
		}

		switch conf := u.(type) {
		case *livekit.S3Upload:
			output.Output = &livekit.SegmentedFileOutput_S3{S3: conf}
		case *livekit.GCPUpload:
			output.Output = &livekit.SegmentedFileOutput_Gcp{Gcp: conf}
		case *livekit.AzureBlobUpload:
			output.Output = &livekit.SegmentedFileOutput_Azure{Azure: conf}
		}

		return []*livekit.SegmentedFileOutput{output}
	}

	return []*livekit.SegmentedFileOutput{{
		FilenamePrefix:   path.Join(r.FilePrefix, o.prefix),
		PlaylistName:     o.playlist,
		LivePlaylistName: o.livePlaylist,
		FilenameSuffix:   o.suffix,
	}}
}

func (r *Runner) buildImageOutputs(o *imageOptions) []*livekit.ImageOutput {
	return []*livekit.ImageOutput{{
		CaptureInterval: 5,
		Width:           1280,
		Height:          720,
		FilenamePrefix:  path.Join(r.FilePrefix, o.prefix),
		FilenameSuffix:  o.suffix,
	}}
}

func (r *Runner) getUploadConfig() interface{} {
	configs := make([]interface{}, 0)
	if r.S3Upload != nil {
		configs = append(configs, r.S3Upload)
	}
	if r.GCPUpload != nil {
		configs = append(configs, r.GCPUpload)
	}
	if r.AzureUpload != nil {
		configs = append(configs, r.AzureUpload)
	}
	if len(configs) == 0 {
		return nil
	}
	return configs[r.testNumber%len(configs)]
}

func (test *testCase) isV2() bool {
	switch test.requestType {
	case types.RequestTypeTemplate, types.RequestTypeMedia:
		return true
	case types.RequestTypeWeb:
		return test.v2OutputOptions != nil
	default:
		return false
	}
}

func (r *Runner) buildRequest(test *testCase) *rpc.StartEgressRequest {
	if test.isV2() {
		return r.buildV2(test)
	}
	return r.build(test)
}

func (r *Runner) getV2StorageConfig() *livekit.StorageConfig {
	u := r.getUploadConfig()
	if u == nil {
		return nil
	}
	switch conf := u.(type) {
	case *livekit.S3Upload:
		return &livekit.StorageConfig{Provider: &livekit.StorageConfig_S3{S3: conf}}
	case *livekit.GCPUpload:
		return &livekit.StorageConfig{Provider: &livekit.StorageConfig_Gcp{Gcp: conf}}
	case *livekit.AzureBlobUpload:
		return &livekit.StorageConfig{Provider: &livekit.StorageConfig_Azure{Azure: conf}}
	case *livekit.AliOSSUpload:
		return &livekit.StorageConfig{Provider: &livekit.StorageConfig_AliOSS{AliOSS: conf}}
	}
	return nil
}

func (r *Runner) buildV2Outputs(test *testCase) []*livekit.Output {
	if test.v2OutputOptions != nil && len(test.outputs) > 0 {
		return test.outputs
	}

	storage := r.getV2StorageConfig()
	var prefix string
	if storage != nil {
		prefix = uploadPrefix
	} else {
		prefix = r.FilePrefix
	}

	var outputs []*livekit.Output

	if test.fileOptions != nil {
		outputs = append(outputs, &livekit.Output{
			Config: &livekit.Output_File{
				File: &livekit.FileOutput{
					FileType: test.fileType,
					Filepath: path.Join(prefix, test.filename),
				},
			},
			Storage: storage,
		})
	}

	if test.streamOptions != nil {
		var protocol livekit.StreamProtocol
		switch test.streamOptions.outputType {
		case types.OutputTypeRTMP:
			protocol = livekit.StreamProtocol_RTMP
		case types.OutputTypeSRT:
			protocol = livekit.StreamProtocol_SRT
		default:
			protocol = livekit.StreamProtocol_DEFAULT_PROTOCOL
		}
		outputs = append(outputs, &livekit.Output{
			Config: &livekit.Output_Stream{
				Stream: &livekit.StreamOutput{
					Protocol: protocol,
					Urls:     test.streamUrls,
				},
			},
		})
	}

	if test.segmentOptions != nil {
		outputs = append(outputs, &livekit.Output{
			Config: &livekit.Output_Segments{
				Segments: &livekit.SegmentedFileOutput{
					FilenamePrefix:   path.Join(prefix, test.segmentOptions.prefix),
					PlaylistName:     test.playlist,
					LivePlaylistName: test.livePlaylist,
					FilenameSuffix:   test.segmentOptions.suffix,
				},
			},
			Storage: storage,
		})
	}

	if test.imageOptions != nil {
		outputs = append(outputs, &livekit.Output{
			Config: &livekit.Output_Images{
				Images: &livekit.ImageOutput{
					CaptureInterval: 5,
					Width:           1280,
					Height:          720,
					FilenamePrefix:  path.Join(r.FilePrefix, test.imageOptions.prefix),
					FilenameSuffix:  test.imageOptions.suffix,
				},
			},
		})
	}

	return outputs
}

func (r *Runner) buildV2(test *testCase) *rpc.StartEgressRequest {
	replayReq := &livekit.ExportReplayRequest{
		ReplayId: "test-replay-id",
		Outputs:  r.buildV2Outputs(test),
	}

	// Source
	switch test.requestType {
	case types.RequestTypeTemplate:
		replayReq.Source = &livekit.ExportReplayRequest_Template{
			Template: &livekit.TemplateSource{
				Layout:        test.layout,
				AudioOnly:     test.audioOnly,
				VideoOnly:     test.videoOnly,
				CustomBaseUrl: test.templateCustomBaseUrl,
			},
		}

	case types.RequestTypeWeb:
		replayReq.Source = &livekit.ExportReplayRequest_Web{
			Web: &livekit.WebSource{
				Url:       webUrl,
				AudioOnly: test.audioOnly,
				VideoOnly: test.videoOnly,
			},
		}

	case types.RequestTypeMedia:
		media := &livekit.MediaSource{}

		// video - use explicit mediaVideoTrackID, or fall back to published videoTrackID
		videoTrackID := test.mediaVideoTrackID
		if videoTrackID == "" && test.videoCodec != "" {
			videoTrackID = test.videoTrackID
		}
		if videoTrackID != "" {
			media.Video = &livekit.MediaSource_VideoTrackId{
				VideoTrackId: videoTrackID,
			}
		} else if test.mediaParticipantVideo != nil {
			pv := test.mediaParticipantVideo
			if pv.Identity == setAtRuntime {
				pv = &livekit.ParticipantVideo{
					Identity:          string(r.room.LocalParticipant.Identity()),
					PreferScreenShare: pv.PreferScreenShare,
				}
			}
			media.Video = &livekit.MediaSource_ParticipantVideo{
				ParticipantVideo: pv,
			}
		}

		// audio - replace placeholder track IDs with actual published IDs
		if len(test.audioRoutes) > 0 {
			routes := make([]*livekit.AudioRoute, len(test.audioRoutes))
			for i, route := range test.audioRoutes {
				routes[i] = route
				if tr, ok := route.Match.(*livekit.AudioRoute_TrackId); ok && tr.TrackId == setAtRuntime {
					routes[i] = &livekit.AudioRoute{
						Match:   &livekit.AudioRoute_TrackId{TrackId: test.audioTrackID},
						Channel: route.Channel,
					}
				}
				if pi, ok := route.Match.(*livekit.AudioRoute_ParticipantIdentity); ok && pi.ParticipantIdentity == setAtRuntime {
					routes[i] = &livekit.AudioRoute{
						Match:   &livekit.AudioRoute_ParticipantIdentity{ParticipantIdentity: string(r.room.LocalParticipant.Identity())},
						Channel: route.Channel,
					}
				}
			}
			media.Audio = &livekit.AudioConfig{Routes: routes}
		}

		replayReq.Source = &livekit.ExportReplayRequest_Media{
			Media: media,
		}
	}

	// Encoding
	if test.encodingOptions != nil {
		replayReq.Encoding = &livekit.ExportReplayRequest_Advanced{
			Advanced: test.encodingOptions,
		}
	} else if test.encodingPreset != 0 {
		replayReq.Encoding = &livekit.ExportReplayRequest_Preset{
			Preset: test.encodingPreset,
		}
	}

	// Global storage
	if test.v2OutputOptions != nil && test.storage != nil {
		replayReq.Storage = test.storage
	}

	// build token since we don't pass a room name
	egressID := utils.NewGuid(utils.EgressPrefix)
	token, _ := egress.BuildEgressToken(egressID, r.ApiKey, r.ApiSecret, r.RoomName)

	return &rpc.StartEgressRequest{
		EgressId: egressID,
		Request:  &rpc.StartEgressRequest_Replay{Replay: replayReq},
		Token:    token,
		WsUrl:    r.WsUrl,
	}
}

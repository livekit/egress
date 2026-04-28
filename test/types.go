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
	"path"
	"testing"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

// Mutation configures a TestConfig. Dimension values are Mutations.
type Mutation func(tc *TestConfig)

// DimensionValue is a named mutation with compatibility tags.
type DimensionValue struct {
	Name  string
	Tags  []string
	Apply Mutation
}

// testCase is the unified test case struct used by both generated and edge case tests.
type testCase struct {
	name    string
	options []Mutation

	// Edge cases only — takes over the test lifecycle after publish + request build.
	// Uses method expression syntax: (*Runner).handlerName
	custom func(r *Runner, t *testing.T, req *rpc.StartEgressRequest, cfg *TestConfig)
}

// TestConfig accumulates state from mutations before building the final request.
type TestConfig struct {
	// Request type
	RequestType types.RequestType

	// Publish metadata
	AudioCodec       types.MimeType
	VideoCodec       types.MimeType
	AudioOnly        bool
	VideoOnly        bool
	MultiParticipant bool
	Layout           string
	AudioMixing      livekit.AudioMixing

	// Encoding
	EncodingOptions *livekit.EncodingOptions

	// Output configs
	FileOutputs    []fileOutputConfig
	StreamOutputs  []streamOutputConfig
	SegmentOutputs []segmentOutputConfig
	ImageOutputs   []imageOutputConfig

	// V2 specific
	V2               bool
	AudioRoutes      []*livekit.AudioRoute
	ParticipantVideo *livekit.ParticipantVideo
	CustomBaseUrl    string
}

type fileOutputConfig struct {
	Filename string
	FileType livekit.EncodedFileType
}

type streamOutputConfig struct {
	Protocol livekit.StreamProtocol
	Urls     []string
}

type segmentOutputConfig struct {
	Prefix       string
	Playlist     string
	LivePlaylist string
	Suffix       livekit.SegmentedFileSuffix
}

type imageOutputConfig struct {
	Prefix          string
	Suffix          livekit.ImageFileSuffix
	CaptureInterval uint32
	Width           int32
	Height          int32
}

func (tc *testCase) applyOptions() *TestConfig {
	cfg := &TestConfig{}
	for _, opt := range tc.options {
		opt(cfg)
	}
	return cfg
}

func (tc *TestConfig) ensureEncodingOptions() *livekit.EncodingOptions {
	if tc.EncodingOptions == nil {
		tc.EncodingOptions = &livekit.EncodingOptions{}
	}
	return tc.EncodingOptions
}

// BuildParams holds runtime values needed to construct a StartEgressRequest.
type BuildParams struct {
	RoomName     string
	AudioTrackID string
	VideoTrackID string
	FilePrefix   string
	ApiKey       string
	ApiSecret    string
	WsUrl        string
	UploadConfig interface{}
}

// Build constructs a StartEgressRequest from the accumulated config.
func (tc *TestConfig) Build(p BuildParams) *rpc.StartEgressRequest {
	egressID := utils.NewGuid(utils.EgressPrefix)

	if tc.V2 {
		return tc.buildV2(egressID, p)
	}
	return tc.buildV1(egressID, p)
}

func (tc *TestConfig) buildV1(egressID string, p BuildParams) *rpc.StartEgressRequest {
	fileOutputs := tc.buildV1FileOutputs(p.FilePrefix, p.UploadConfig)
	streamOutputs := tc.buildV1StreamOutputs()
	segmentOutputs := tc.buildV1SegmentOutputs(p.FilePrefix, p.UploadConfig)
	imageOutputs := tc.buildV1ImageOutputs(p.FilePrefix)

	switch tc.RequestType {
	case types.RequestTypeRoomComposite:
		room := &livekit.RoomCompositeEgressRequest{
			RoomName:       p.RoomName,
			Layout:         tc.Layout,
			AudioOnly:      tc.AudioOnly,
			VideoOnly:      tc.VideoOnly,
			AudioMixing:    tc.AudioMixing,
			FileOutputs:    fileOutputs,
			StreamOutputs:  streamOutputs,
			SegmentOutputs: segmentOutputs,
			ImageOutputs:   imageOutputs,
		}
		if tc.EncodingOptions != nil {
			room.Options = &livekit.RoomCompositeEgressRequest_Advanced{Advanced: tc.EncodingOptions}
		}
		return &rpc.StartEgressRequest{
			EgressId: egressID,
			Request:  &rpc.StartEgressRequest_RoomComposite{RoomComposite: room},
		}

	case types.RequestTypeWeb:
		web := &livekit.WebEgressRequest{
			Url:            webUrl,
			AudioOnly:      tc.AudioOnly,
			VideoOnly:      tc.VideoOnly,
			FileOutputs:    fileOutputs,
			StreamOutputs:  streamOutputs,
			SegmentOutputs: segmentOutputs,
			ImageOutputs:   imageOutputs,
		}
		if tc.EncodingOptions != nil {
			web.Options = &livekit.WebEgressRequest_Advanced{Advanced: tc.EncodingOptions}
		}
		return &rpc.StartEgressRequest{
			EgressId: egressID,
			Request:  &rpc.StartEgressRequest_Web{Web: web},
		}

	case types.RequestTypeParticipant:
		pr := &livekit.ParticipantEgressRequest{
			RoomName:       p.RoomName,
			FileOutputs:    fileOutputs,
			StreamOutputs:  streamOutputs,
			SegmentOutputs: segmentOutputs,
			ImageOutputs:   imageOutputs,
		}
		if tc.EncodingOptions != nil {
			pr.Options = &livekit.ParticipantEgressRequest_Advanced{Advanced: tc.EncodingOptions}
		}
		return &rpc.StartEgressRequest{
			EgressId: egressID,
			Request:  &rpc.StartEgressRequest_Participant{Participant: pr},
		}

	case types.RequestTypeTrackComposite:
		tc2 := &livekit.TrackCompositeEgressRequest{
			RoomName:       p.RoomName,
			AudioTrackId:   p.AudioTrackID,
			VideoTrackId:   p.VideoTrackID,
			FileOutputs:    fileOutputs,
			StreamOutputs:  streamOutputs,
			SegmentOutputs: segmentOutputs,
			ImageOutputs:   imageOutputs,
		}
		if tc.EncodingOptions != nil {
			tc2.Options = &livekit.TrackCompositeEgressRequest_Advanced{Advanced: tc.EncodingOptions}
		}
		return &rpc.StartEgressRequest{
			EgressId: egressID,
			Request:  &rpc.StartEgressRequest_TrackComposite{TrackComposite: tc2},
		}

	case types.RequestTypeTrack:
		trackID := p.AudioTrackID
		if trackID == "" {
			trackID = p.VideoTrackID
		}
		track := &livekit.TrackEgressRequest{
			RoomName: p.RoomName,
			TrackId:  trackID,
		}
		if len(tc.FileOutputs) > 0 {
			track.Output = &livekit.TrackEgressRequest_File{
				File: &livekit.DirectFileOutput{
					Filepath: path.Join(p.FilePrefix, tc.FileOutputs[0].Filename),
				},
			}
		} else if len(tc.StreamOutputs) > 0 && len(tc.StreamOutputs[0].Urls) > 0 {
			track.Output = &livekit.TrackEgressRequest_WebsocketUrl{
				WebsocketUrl: tc.StreamOutputs[0].Urls[0],
			}
		}
		return &rpc.StartEgressRequest{
			EgressId: egressID,
			Request:  &rpc.StartEgressRequest_Track{Track: track},
		}
	}

	panic("unknown request type: " + string(tc.RequestType))
}

func (tc *TestConfig) buildV2(egressID string, p BuildParams) *rpc.StartEgressRequest {
	storage := buildV2Storage(p.UploadConfig)
	prefix := p.FilePrefix
	if storage != nil {
		prefix = uploadPrefix
	}

	replayReq := &livekit.ExportReplayRequest{
		ReplayId: "test-replay-id",
		Outputs:  tc.buildV2Outputs(prefix, p.FilePrefix, storage),
		Storage:  storage,
	}

	switch tc.RequestType {
	case types.RequestTypeTemplate:
		replayReq.Source = &livekit.ExportReplayRequest_Template{
			Template: &livekit.TemplateSource{
				Layout:        tc.Layout,
				AudioOnly:     tc.AudioOnly,
				VideoOnly:     tc.VideoOnly,
				CustomBaseUrl: tc.CustomBaseUrl,
			},
		}
	case types.RequestTypeWeb:
		replayReq.Source = &livekit.ExportReplayRequest_Web{
			Web: &livekit.WebSource{
				Url:       webUrl,
				AudioOnly: tc.AudioOnly,
				VideoOnly: tc.VideoOnly,
			},
		}
	case types.RequestTypeMedia:
		media := &livekit.MediaSource{}
		if p.VideoTrackID != "" {
			media.Video = &livekit.MediaSource_VideoTrackId{VideoTrackId: p.VideoTrackID}
		} else if tc.ParticipantVideo != nil {
			media.Video = &livekit.MediaSource_ParticipantVideo{ParticipantVideo: tc.ParticipantVideo}
		}
		if len(tc.AudioRoutes) > 0 {
			media.Audio = &livekit.AudioConfig{Routes: tc.AudioRoutes}
		}
		replayReq.Source = &livekit.ExportReplayRequest_Media{Media: media}
	}

	if tc.EncodingOptions != nil {
		replayReq.Encoding = &livekit.ExportReplayRequest_Advanced{Advanced: tc.EncodingOptions}
	}

	token, _ := egress.BuildEgressToken(egressID, p.ApiKey, p.ApiSecret, p.RoomName)
	return &rpc.StartEgressRequest{
		EgressId: egressID,
		Request:  &rpc.StartEgressRequest_Replay{Replay: replayReq},
		Token:    token,
		WsUrl:    p.WsUrl,
	}
}

func (tc *TestConfig) buildV2Outputs(prefix, filePrefix string, storage *livekit.StorageConfig) []*livekit.Output {
	var outputs []*livekit.Output
	for _, f := range tc.FileOutputs {
		outputs = append(outputs, &livekit.Output{
			Config:  &livekit.Output_File{File: &livekit.FileOutput{FileType: f.FileType, Filepath: path.Join(prefix, f.Filename)}},
			Storage: storage,
		})
	}
	for _, s := range tc.StreamOutputs {
		outputs = append(outputs, &livekit.Output{
			Config: &livekit.Output_Stream{Stream: &livekit.StreamOutput{Protocol: s.Protocol, Urls: s.Urls}},
		})
	}
	for _, seg := range tc.SegmentOutputs {
		outputs = append(outputs, &livekit.Output{
			Config: &livekit.Output_Segments{Segments: &livekit.SegmentedFileOutput{
				FilenamePrefix: path.Join(prefix, seg.Prefix), PlaylistName: seg.Playlist,
				LivePlaylistName: seg.LivePlaylist, FilenameSuffix: seg.Suffix,
			}},
			Storage: storage,
		})
	}
	for _, img := range tc.ImageOutputs {
		interval := img.CaptureInterval
		if interval == 0 {
			interval = 5
		}
		w := img.Width
		if w == 0 {
			w = 1280
		}
		h := img.Height
		if h == 0 {
			h = 720
		}
		outputs = append(outputs, &livekit.Output{
			Config: &livekit.Output_Images{Images: &livekit.ImageOutput{
				CaptureInterval: interval, Width: w, Height: h,
				FilenamePrefix: path.Join(filePrefix, img.Prefix), FilenameSuffix: img.Suffix,
			}},
		})
	}
	return outputs
}

func (tc *TestConfig) buildV1FileOutputs(prefix string, upload interface{}) []*livekit.EncodedFileOutput {
	if len(tc.FileOutputs) == 0 {
		return nil
	}
	var out []*livekit.EncodedFileOutput
	for _, f := range tc.FileOutputs {
		fp := path.Join(prefix, f.Filename)
		if upload != nil {
			fp = path.Join(uploadPrefix, f.Filename)
		}
		o := &livekit.EncodedFileOutput{FileType: f.FileType, Filepath: fp}
		attachUpload(o, upload)
		out = append(out, o)
	}
	return out
}

func (tc *TestConfig) buildV1StreamOutputs() []*livekit.StreamOutput {
	if len(tc.StreamOutputs) == 0 {
		return nil
	}
	var out []*livekit.StreamOutput
	for _, s := range tc.StreamOutputs {
		out = append(out, &livekit.StreamOutput{Protocol: s.Protocol, Urls: s.Urls})
	}
	return out
}

func (tc *TestConfig) buildV1SegmentOutputs(prefix string, upload interface{}) []*livekit.SegmentedFileOutput {
	if len(tc.SegmentOutputs) == 0 {
		return nil
	}
	var out []*livekit.SegmentedFileOutput
	for _, s := range tc.SegmentOutputs {
		fp := path.Join(prefix, s.Prefix)
		if upload != nil {
			fp = path.Join(uploadPrefix, s.Prefix)
		}
		o := &livekit.SegmentedFileOutput{
			FilenamePrefix: fp, PlaylistName: s.Playlist,
			LivePlaylistName: s.LivePlaylist, FilenameSuffix: s.Suffix,
		}
		attachSegmentUpload(o, upload)
		out = append(out, o)
	}
	return out
}

func (tc *TestConfig) buildV1ImageOutputs(prefix string) []*livekit.ImageOutput {
	if len(tc.ImageOutputs) == 0 {
		return nil
	}
	var out []*livekit.ImageOutput
	for _, img := range tc.ImageOutputs {
		interval := img.CaptureInterval
		if interval == 0 {
			interval = 5
		}
		w := img.Width
		if w == 0 {
			w = 1280
		}
		h := img.Height
		if h == 0 {
			h = 720
		}
		out = append(out, &livekit.ImageOutput{
			CaptureInterval: interval, Width: w, Height: h,
			FilenamePrefix: path.Join(prefix, img.Prefix), FilenameSuffix: img.Suffix,
		})
	}
	return out
}

func attachUpload(o *livekit.EncodedFileOutput, upload interface{}) {
	switch conf := upload.(type) {
	case *livekit.S3Upload:
		o.Output = &livekit.EncodedFileOutput_S3{S3: conf}
	case *livekit.GCPUpload:
		o.Output = &livekit.EncodedFileOutput_Gcp{Gcp: conf}
	case *livekit.AzureBlobUpload:
		o.Output = &livekit.EncodedFileOutput_Azure{Azure: conf}
	}
}

func attachSegmentUpload(o *livekit.SegmentedFileOutput, upload interface{}) {
	switch conf := upload.(type) {
	case *livekit.S3Upload:
		o.Output = &livekit.SegmentedFileOutput_S3{S3: conf}
	case *livekit.GCPUpload:
		o.Output = &livekit.SegmentedFileOutput_Gcp{Gcp: conf}
	case *livekit.AzureBlobUpload:
		o.Output = &livekit.SegmentedFileOutput_Azure{Azure: conf}
	}
}

func buildV2Storage(upload interface{}) *livekit.StorageConfig {
	switch conf := upload.(type) {
	case *livekit.S3Upload:
		return &livekit.StorageConfig{Provider: &livekit.StorageConfig_S3{S3: conf}}
	case *livekit.GCPUpload:
		return &livekit.StorageConfig{Provider: &livekit.StorageConfig_Gcp{Gcp: conf}}
	case *livekit.AzureBlobUpload:
		return &livekit.StorageConfig{Provider: &livekit.StorageConfig_Azure{Azure: conf}}
	}
	return nil
}

// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func TestMCAPEncodedFileType(t *testing.T) {
	require.Equal(t, types.OutputTypeMCAP, fileTypeToOutputType(livekit.EncodedFileType_MCAP))
	require.Equal(t, types.FileExtensionMCAP, string(types.FileExtensionForOutputType[types.OutputTypeMCAP]))
	require.Equal(t, types.MimeTypeOpus, types.DefaultAudioCodecs[types.OutputTypeMCAP])
	require.Equal(t, types.MimeTypeH264, types.DefaultVideoCodecs[types.OutputTypeMCAP])
}

func TestValidateMediaTracksOutput(t *testing.T) {
	valid := &PipelineConfig{Outputs: map[types.EgressType][]OutputConfig{
		types.EgressTypeFile: {&FileConfig{outputConfig: outputConfig{OutputType: types.OutputTypeMCAP}}},
	}}
	require.NoError(t, valid.validateMediaTracksOutput())

	wrongType := &PipelineConfig{Outputs: map[types.EgressType][]OutputConfig{
		types.EgressTypeFile: {&FileConfig{outputConfig: outputConfig{OutputType: types.OutputTypeMP4}}},
	}}
	require.Error(t, wrongType.validateMediaTracksOutput())

	multiple := &PipelineConfig{Outputs: map[types.EgressType][]OutputConfig{
		types.EgressTypeFile: {
			&FileConfig{outputConfig: outputConfig{OutputType: types.OutputTypeMCAP}},
			&FileConfig{outputConfig: outputConfig{OutputType: types.OutputTypeMCAP}},
		},
	}}
	require.Error(t, multiple.validateMediaTracksOutput())
}

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
)

func TestMCAPEncodedFileType(t *testing.T) {
	require.Equal(t, types.OutputTypeMCAP, fileTypeToOutputType(encodedFileTypeMCAP))
	require.Equal(t, types.FileExtensionMCAP, types.FileExtensionForOutputType[types.OutputTypeMCAP])
	require.Equal(t, types.MimeTypeOpus, types.DefaultAudioCodecs[types.OutputTypeMCAP])
	require.Equal(t, types.MimeTypeH264, types.DefaultVideoCodecs[types.OutputTypeMCAP])
}

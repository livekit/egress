package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetMapIntersection(t *testing.T) {
	list := make(map[MimeType]bool)

	res := GetMapIntersection(list, CodecCompatibility[OutputTypeUnknownFile])
	require.Empty(t, res)

	list[MimeTypeH264] = true
	res = GetMapIntersection(list, CodecCompatibility[OutputTypeOGG])
	require.Empty(t, res)

	list[MimeTypeVP8] = true
	res = GetMapIntersection(list, CodecCompatibility[OutputTypeMP4])
	require.Equal(t, map[MimeType]bool{MimeTypeH264: true}, res)
}

func TestGetOutputTypesCompatibleWithCodecs(t *testing.T) {
	outputTypes := make([]OutputType, 0)
	audioCodecs := make(map[MimeType]bool)
	videoCodecs := make(map[MimeType]bool)

	res := GetOutputTypeCompatibleWithCodecs(outputTypes, audioCodecs, videoCodecs)
	require.Empty(t, res)

	outputTypes = append(outputTypes, OutputTypeOGG, OutputTypeMP4)
	res = GetOutputTypeCompatibleWithCodecs(outputTypes, audioCodecs, videoCodecs)
	require.Empty(t, res)

	audioCodecs[MimeTypeAAC] = true
	outputTypes = append(outputTypes, OutputTypeMP4)
	res = GetOutputTypeCompatibleWithCodecs(outputTypes, audioCodecs, videoCodecs)
	require.Empty(t, res)

	videoCodecs[MimeTypeVP8] = true
	outputTypes = append(outputTypes, OutputTypeMP4)
	res = GetOutputTypeCompatibleWithCodecs(outputTypes, audioCodecs, videoCodecs)
	require.Empty(t, res)

	videoCodecs[MimeTypeH264] = true
	outputTypes = append(outputTypes, OutputTypeMP4)
	res = GetOutputTypeCompatibleWithCodecs(outputTypes, audioCodecs, videoCodecs)
	require.Equal(t, OutputTypeMP4, res)
}

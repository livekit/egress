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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/m3u8"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func (r *Runner) verifyFile(t *testing.T, cfg *TestConfig, p *config.PipelineConfig, res *livekit.EgressInfo) {
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	require.NotEmpty(t, fileRes.Location)
	require.Greater(t, fileRes.Size, int64(0))
	require.Greater(t, fileRes.Duration, int64(0))

	storagePath := fileRes.Filename
	require.NotEmpty(t, storagePath)
	require.False(t, strings.Contains(storagePath, "{"))
	storageFilename := path.Base(storagePath)

	localPath := path.Join(r.FilePrefix, storageFilename)
	download(t, p.GetFileConfig().StorageConfig, localPath, storagePath, false)

	manifestLocal := path.Join(path.Dir(localPath), res.EgressId+".json")
	manifestStorage := path.Join(path.Dir(storagePath), res.EgressId+".json")
	manifest := loadManifest(t, p.GetFileConfig().StorageConfig, manifestLocal, manifestStorage)
	require.NotNil(t, manifest)

	info := verify(t, localPath, p, res, types.EgressTypeFile, r.Muting, r.sourceFramerate, false)

	r.runContentCheck(t, cfg, localPath, info)
}

func (r *Runner) verifyStreams(t *testing.T, cfg *TestConfig, p *config.PipelineConfig, urls ...string) {
	for _, url := range urls {
		info := verify(t, url, p, nil, types.EgressTypeStream, false, r.sourceFramerate, false)
		if cfg != nil {
			r.runContentCheck(t, cfg, url, info)
		}
	}
}

func (r *Runner) runStreamTest(t *testing.T, cfg *TestConfig, req *rpc.StartEgressRequest) {
	if cfg.RequestType == types.RequestTypeTrack {
		r.runWebsocketTest(t, cfg, req)
		return
	}

	ctx := context.Background()
	outputType := types.OutputTypeRTMP
	if len(cfg.StreamOutputs) > 0 && cfg.StreamOutputs[0].Protocol == livekit.StreamProtocol_SRT {
		outputType = types.OutputTypeSRT
	}
	urls := streamUrls[outputType]
	egressID := r.startEgress(t, req)

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	if !cfg.AudioOnly {
		require.True(t, p.VideoEncoding)
	}

	time.Sleep(time.Second * 5)
	r.verifyStreams(t, cfg, p, urls[0][2])
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		urls[0][1]: livekit.StreamInfo_ACTIVE,
		urls[1][1]: livekit.StreamInfo_FAILED,
	})

	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:      egressID,
		AddOutputUrls: []string{urls[2][0], urls[3][0]},
	})
	require.NoError(t, err)
	time.Sleep(time.Second * 5)

	r.verifyStreams(t, cfg, p, urls[0][2], urls[2][2])
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		urls[0][1]: livekit.StreamInfo_ACTIVE,
		urls[1][1]: livekit.StreamInfo_FAILED,
		urls[2][1]: livekit.StreamInfo_ACTIVE,
		urls[3][1]: livekit.StreamInfo_FAILED,
	})

	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:         egressID,
		RemoveOutputUrls: []string{urls[0][0]},
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 5)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	r.verifyStreams(t, cfg, p, urls[2][2])
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		urls[0][1]: livekit.StreamInfo_FINISHED,
		urls[1][1]: livekit.StreamInfo_FAILED,
		urls[2][1]: livekit.StreamInfo_ACTIVE,
		urls[3][1]: livekit.StreamInfo_FAILED,
	})

	time.Sleep(time.Second * 5)
	res := r.stopEgress(t, egressID)

	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	require.Len(t, res.StreamResults, 4)
	for _, info := range res.StreamResults {
		require.NotZero(t, info.StartedAt)
		require.NotZero(t, info.EndedAt)

		switch info.Url {
		case urls[0][1]:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 15.0)
		case urls[2][1]:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 10.0)
		default:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())
		}
	}
}

func (r *Runner) runWebsocketTest(t *testing.T, _ *TestConfig, req *rpc.StartEgressRequest) {
	rawFileName := fmt.Sprintf("track-ws-%v.raw", time.Now().Unix())
	filepath := path.Join(r.FilePrefix, rawFileName)
	wss := newTestWebsocketServer(filepath)
	s := httptest.NewServer(http.HandlerFunc(wss.handleWebsocket))
	defer func() {
		wss.close()
		s.Close()
	}()

	// Inject actual websocket URL into request
	wsUrl := "ws" + strings.TrimPrefix(s.URL, "http")
	if track := req.GetTrack(); track != nil {
		track.Output = &livekit.TrackEgressRequest_WebsocketUrl{WebsocketUrl: wsUrl}
	}

	egressID := r.startEgress(t, req)

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	time.Sleep(time.Second * 30)

	res := r.stopEgress(t, egressID)
	verify(t, filepath, p, res, types.EgressTypeWebsocket, r.Muting, r.sourceFramerate, false)
}

func (r *Runner) verifySegments(
	t *testing.T, cfg *TestConfig, p *config.PipelineConfig,
	filenameSuffix livekit.SegmentedFileSuffix,
	res *livekit.EgressInfo, enableLivePlaylist bool,
) {
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	require.Len(t, res.GetSegmentResults(), 1)
	segments := res.GetSegmentResults()[0]

	require.Greater(t, segments.Size, int64(0))
	require.Greater(t, segments.Duration, int64(0))

	r.verifySegmentOutput(t, cfg, p, filenameSuffix, segmentPlaylist{
		name:         segments.PlaylistName,
		location:     segments.PlaylistLocation,
		segmentCount: int(segments.SegmentCount),
		playlistType: m3u8.PlaylistTypeEvent,
	}, res)
	if enableLivePlaylist {
		r.verifySegmentOutput(t, cfg, p, filenameSuffix, segmentPlaylist{
			name:         segments.LivePlaylistName,
			location:     segments.LivePlaylistLocation,
			segmentCount: 5,
			playlistType: m3u8.PlaylistTypeLive,
		}, res)
	}
}

func (r *Runner) verifySegmentOutput(
	t *testing.T, cfg *TestConfig, p *config.PipelineConfig,
	filenameSuffix livekit.SegmentedFileSuffix,
	pl segmentPlaylist,
	res *livekit.EgressInfo,
) {
	require.NotEmpty(t, pl.name)
	require.NotEmpty(t, pl.location)

	storedPlaylistPath := pl.name

	localPlaylistPath := path.Join(r.FilePrefix, path.Base(storedPlaylistPath))
	download(t, p.GetSegmentConfig().StorageConfig, localPlaylistPath, storedPlaylistPath, false)

	if pl.playlistType == m3u8.PlaylistTypeEvent {
		manifestLocal := path.Join(path.Dir(localPlaylistPath), res.EgressId+".json")
		manifestStorage := path.Join(path.Dir(storedPlaylistPath), res.EgressId+".json")
		manifest := loadManifest(t, p.GetSegmentConfig().StorageConfig, manifestLocal, manifestStorage)

		for _, playlist := range manifest.Playlists {
			require.Equal(t, pl.segmentCount, len(playlist.Segments))
			for _, segment := range playlist.Segments {
				localPath := path.Join(r.FilePrefix, path.Base(segment.Filename))
				download(t, p.GetSegmentConfig().StorageConfig, localPath, segment.Filename, false)
			}
		}
	}

	verifyPlaylistProgramDateTime(t, filenameSuffix, localPlaylistPath, pl.playlistType)

	info := verify(t, localPlaylistPath, p, res, types.EgressTypeSegments, r.Muting, r.sourceFramerate, pl.playlistType == m3u8.PlaylistTypeLive)
	r.runContentCheck(t, cfg, localPlaylistPath, info)
}

func (r *Runner) verifyImages(t *testing.T, p *config.PipelineConfig, res *livekit.EgressInfo) {
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	require.Len(t, res.GetImageResults(), 1)
	images := res.GetImageResults()[0]

	require.Greater(t, images.ImageCount, int64(0))

	imageConfig := p.GetImageConfigs()[0]
	for i := range images.ImageCount {
		storagePath := fmt.Sprintf("%s_%05d%s", images.FilenamePrefix, i, imageConfig.ImageExtension)
		localPath := path.Join(r.FilePrefix, path.Base(storagePath))
		download(t, imageConfig.StorageConfig, localPath, storagePath, true)
	}
}

// Stream URL maps (used by runStreamTest)
var streamUrls = map[types.OutputType][][]string{
	types.OutputTypeRTMP: {
		{rtmpUrl1, rtmpUrl1Redacted, rtmpUrl1},
		{badRtmpUrl1, badRtmpUrl1Redacted, ""},
		{rtmpUrl2, rtmpUrl2Redacted, rtmpUrl2},
		{badRtmpUrl2, badRtmpUrl2Redacted, ""},
	},
	types.OutputTypeSRT: {
		{srtPublishUrl1, srtPublishUrl1, srtReadUrl1},
		{badSrtUrl1, badSrtUrl1, ""},
		{srtPublishUrl2, srtPublishUrl2, srtReadUrl2},
		{badSrtUrl2, badSrtUrl2, ""},
	},
}

// Stream URL constants and variables
const (
	badRtmpUrl1         = "rtmp://localhost:1936/wrong/stream"
	badRtmpUrl1Redacted = "rtmp://localhost:1936/wrong/{st...am}"
	badRtmpUrl2         = "rtmp://localhost:1936/live/stream"
	badRtmpUrl2Redacted = "rtmp://localhost:1936/live/{st...am}"
	badSrtUrl1          = "srt://localhost:8891?streamid=publish:wrongport&pkt_size=1316"
	badSrtUrl2          = "srt://localhost:8891?streamid=publish:badstream&pkt_size=1316"
)

var (
	streamKey1          = utils.NewGuid("")
	streamKey2          = utils.NewGuid("")
	streamKey3          = utils.NewGuid("")
	streamKey4          = utils.NewGuid("")
	rtmpUrl1            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey1)
	rtmpUrl2            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey2)
	rtmpUrl3            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey3)
	rtmpUrl4            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey4)
	rtmpUrl1Redacted, _ = utils.RedactStreamKey(rtmpUrl1)
	rtmpUrl2Redacted, _ = utils.RedactStreamKey(rtmpUrl2)
	rtmpUrl3Redacted, _ = utils.RedactStreamKey(rtmpUrl3)
	rtmpUrl4Redacted, _ = utils.RedactStreamKey(rtmpUrl4)
	srtPublishUrl1      = fmt.Sprintf("srt://localhost:8890?streamid=publish:%s&pkt_size=1316", streamKey1)
	srtReadUrl1         = fmt.Sprintf("srt://localhost:8890?streamid=read:%s", streamKey1)
	srtPublishUrl2      = fmt.Sprintf("srt://localhost:8890?streamid=publish:%s&pkt_size=1316", streamKey2)
	srtReadUrl2         = fmt.Sprintf("srt://localhost:8890?streamid=read:%s", streamKey2)
)

// Websocket test server
type websocketTestServer struct {
	path string
	file *os.File
	conn *websocket.Conn
	done chan struct{}
}

func newTestWebsocketServer(filepath string) *websocketTestServer {
	return &websocketTestServer{
		path: filepath,
		done: make(chan struct{}),
	}
}

func (s *websocketTestServer) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	var err error

	s.file, err = os.Create(s.path)
	if err != nil {
		logger.Errorw("could not create file", err)
		return
	}

	upgrader := websocket.Upgrader{}
	s.conn, err = upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Errorw("could not accept ws connection", err)
		return
	}

	go func() {
		defer func() {
			_ = s.file.Close()
			if !websocket.IsUnexpectedCloseError(err) {
				_ = s.conn.Close()
			}
		}()

		for {
			select {
			case <-s.done:
				return
			default:
				mt, msg, err := s.conn.ReadMessage()
				if err != nil {
					if !websocket.IsUnexpectedCloseError(err) {
						logger.Errorw("unexpected ws close", err)
					}
					return
				}

				switch mt {
				case websocket.BinaryMessage:
					_, err = s.file.Write(msg)
					if err != nil {
						logger.Errorw("could not write to file", err)
						return
					}
				}
			}
		}
	}()
}

func (s *websocketTestServer) close() {
	close(s.done)
}

// Playlist types (moved from segments.go)
type segmentPlaylist struct {
	name         string
	location     string
	segmentCount int
	playlistType m3u8.PlaylistType
}

func verifyPlaylistProgramDateTime(t *testing.T, filenameSuffix livekit.SegmentedFileSuffix, localPlaylistPath string, plType m3u8.PlaylistType) {
	p, err := readPlaylist(localPlaylistPath)
	require.NoError(t, err)
	require.Equal(t, string(plType), p.MediaType)
	require.True(t, p.Closed)

	now := time.Now()

	for i, s := range p.Segments {
		const leeway = 50 * time.Millisecond

		require.InDelta(t, now.Unix(), s.ProgramDateTime.Unix(), 120)

		if filenameSuffix == livekit.SegmentedFileSuffix_TIMESTAMP {
			m := segmentTimeRegexp.FindStringSubmatch(s.Filename)
			require.Equal(t, 3, len(m))

			tm, err := time.Parse("20060102150405", m[1])
			require.NoError(t, err)

			ms, err := strconv.Atoi(m[2])
			require.NoError(t, err)

			tm = tm.Add(time.Duration(ms) * time.Millisecond)

			require.InDelta(t, s.ProgramDateTime.UnixNano(), tm.UnixNano(), float64(time.Millisecond))
		}

		if i < len(p.Segments)-2 {
			nextSegmentStartDate := p.Segments[i+1].ProgramDateTime

			dateDuration := nextSegmentStartDate.Sub(s.ProgramDateTime)
			require.InDelta(t, time.Duration(s.Duration*float64(time.Second)), dateDuration, float64(leeway))
		}
	}
}

type Playlist struct {
	Version        int
	MediaType      string
	TargetDuration int
	Segments       []*Segment
	Closed         bool
}

type Segment struct {
	ProgramDateTime time.Time
	Duration        float64
	Filename        string
}

func readPlaylist(filename string) (*Playlist, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var segmentLineStart = 5
	var i = 1

	lines := strings.Split(string(b), "\n")
	version, _ := strconv.Atoi(strings.Split(lines[i], ":")[1])
	i++
	var mediaType string
	if strings.Contains(string(b), "#EXT-X-PLAYLIST-TYPE") {
		mediaType = strings.Split(lines[i], ":")[1]
		segmentLineStart++
		i++
	}
	i++ // #EXT-X-ALLOW-CACHE:NO hardcoded
	targetDuration, _ := strconv.Atoi(strings.Split(lines[i], ":")[1])

	p := &Playlist{
		Version:        version,
		MediaType:      mediaType,
		TargetDuration: targetDuration,
		Segments:       make([]*Segment, 0),
	}

	for i = segmentLineStart; i < len(lines)-3; i += 3 {
		startTime, _ := time.Parse("2006-01-02T15:04:05.999Z07:00", strings.SplitN(lines[i], ":", 2)[1])
		durStr := strings.Split(lines[i+1], ":")[1]
		durStr = durStr[:len(durStr)-1]
		duration, _ := strconv.ParseFloat(durStr, 64)

		p.Segments = append(p.Segments, &Segment{
			ProgramDateTime: startTime,
			Duration:        duration,
			Filename:        lines[i+2],
		})
	}

	if lines[len(lines)-2] == "#EXT-X-ENDLIST" {
		p.Closed = true
	}

	return p, nil
}

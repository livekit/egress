//go:build integration
// +build integration

package test

import (
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mackerelio/go-osstat/cpu"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/pipeline"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

const (
	videoTestInput  = "https://www.youtube.com/watch?v=4cJpiOPKH14&t=25s"
	audioTestInput  = "https://www.youtube.com/watch?v=eAcFPtCyDYY&t=59s"
	audioTestInput2 = "https://www.youtube.com/watch?v=BlPbAq1dW3I&t=45s"
	staticTestInput = "https://www.livekit.io"
	defaultConfig   = `
log_level: error
api_key: fake_key
api_secret: fake_secret
ws_url: wss://fake-url.com
`
)

type testCase struct {
	name       string
	inputUrl   string
	audioOnly  bool
	fileType   livekit.EncodedFileType
	options    *livekit.EncodingOptions
	filePrefix string
}

func getTestConfig(t *testing.T) *config.Config {
	confString := defaultConfig
	confFile := os.Getenv("EGRESS_CONFIG_FILE")
	if confFile != "/out/" {
		b, err := ioutil.ReadFile(confFile)
		if err == nil {
			confString = string(b)
		}
	}

	conf, err := config.NewConfig(confString)
	require.NoError(t, err)

	return conf
}

func getFileInfo(conf *config.Config, test *testCase, testType string) (string, string) {
	var name string
	if test.audioOnly {
		if test.options != nil && test.options.AudioCodec != livekit.AudioCodec_DEFAULT_AC {
			name = test.options.AudioCodec.String()
		} else {
			name = params.DefaultAudioCodecs[test.fileType.String()].String()
		}
	} else {
		if test.options != nil && test.options.VideoCodec != livekit.VideoCodec_DEFAULT_VC {
			name = test.options.VideoCodec.String()
		} else {
			name = params.DefaultVideoCodecs[test.fileType.String()].String()
		}
	}

	var filename string
	if test.filePrefix == "" {
		filename = fmt.Sprintf("%s-%s-%d.%s",
			testType, strings.ToLower(name), time.Now().Unix(), strings.ToLower(test.fileType.String()),
		)
	} else {
		filename = fmt.Sprintf("%s-%d.%s", test.filePrefix, time.Now().Unix(), strings.ToLower(test.fileType.String()))
	}

	filepath := fmt.Sprintf("/out/output/%s", filename)

	if conf.FileUpload != nil {
		return filepath, filename
	}

	return filepath, filepath
}

func runFileTest(t *testing.T, conf *config.Config, test *testCase, req *livekit.StartEgressRequest, filename string) {
	p, err := params.GetPipelineParams(conf, req)
	require.NoError(t, err)

	if !strings.HasPrefix(conf.ApiKey, "API") || test.inputUrl == staticTestInput {
		p.CustomInputURL = test.inputUrl
	}

	rec, err := pipeline.FromParams(conf, p)
	require.NoError(t, err)

	// record for ~15s. Takes about 5s to start
	time.AfterFunc(time.Second*90, func() {
		rec.Stop()
	})
	res := rec.Run()

	// check results
	require.Empty(t, res.Error)
	fileRes := res.GetFile()
	require.NotNil(t, fileRes)
	require.NotZero(t, fileRes.StartedAt)
	require.NotZero(t, fileRes.EndedAt)
	require.NotEmpty(t, fileRes.Filename)
	require.NotEmpty(t, fileRes.Location)

	verify(t, filename, p, res, false, test.fileType)
}

func printLoadAvg(t *testing.T, name string, done chan struct{}) {
	prev, _ := cpu.Get()
	var count, userTotal, userMax, systemTotal, systemMax, idleTotal float64
	idleMin := 100.0
	for {
		time.Sleep(time.Second)
		select {
		case <-done:
			avg := 100 - idleTotal/count
			max := 100 - idleMin
			numCPUs := runtime.NumCPU()
			t.Logf("%s cpu usage\n  avg: %.2f%% (%.2f/%v)\n  max: %.2f%% (%.2f/%v)",
				name,
				avg, avg*float64(numCPUs)/100, numCPUs,
				max, max*float64(numCPUs)/100, numCPUs,
			)
			return
		default:
			cpuInfo, _ := cpu.Get()
			total := float64(cpuInfo.Total - prev.Total)
			user := float64(cpuInfo.User-prev.User) / total * 100
			userTotal += user
			if user > userMax {
				userMax = user
			}

			system := float64(cpuInfo.System-prev.System) / total * 100
			systemTotal += system
			if system > systemMax {
				systemMax = system
			}

			idle := float64(cpuInfo.Idle-prev.Idle) / total * 100
			idleTotal += idle
			if idle < idleMin {
				idleMin = idle
			}

			count++
			prev = cpuInfo
		}
	}
}

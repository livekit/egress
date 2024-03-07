// Copyright 2023 LiveKit, Inc.
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

package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const (
	startRecordingLog = "START_RECORDING"
	endRecordingLog   = "END_RECORDING"
)

type WebSource struct {
	pulseSink    string
	xvfb         *exec.Cmd
	chromeCancel context.CancelFunc

	startRecording chan struct{}
	endRecording   chan struct{}

	info *livekit.EgressInfo
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func NewWebSource(ctx context.Context, p *config.PipelineConfig) (*WebSource, error) {
	ctx, span := tracer.Start(ctx, "WebInput.New")
	defer span.End()

	p.Display = fmt.Sprintf(":%d", 10+rand.Intn(2147483637))

	s := &WebSource{
		endRecording: make(chan struct{}),
		info:         p.Info,
	}
	if p.AwaitStartSignal {
		s.startRecording = make(chan struct{})
	}

	if err := s.createPulseSink(ctx, p); err != nil {
		logger.Errorw("failed to create pulse sink", err)
		s.Close()
		return nil, err
	}

	if err := s.launchXvfb(ctx, p); err != nil {
		logger.Errorw("failed to launch xvfb", err, "display", p.Display)
		s.Close()
		return nil, err
	}

	if err := s.launchChrome(ctx, p, p.Insecure); err != nil {
		logger.Warnw("failed to launch chrome", err, "display", p.Display)
		s.Close()
		return nil, err
	}

	return s, nil
}

func (s *WebSource) StartRecording() chan struct{} {
	return s.startRecording
}

func (s *WebSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *WebSource) GetStartedAt() int64 {
	return time.Now().UnixNano()
}

func (s *WebSource) GetEndedAt() int64 {
	return time.Now().UnixNano()
}

func (s *WebSource) Close() {
	if s.chromeCancel != nil {
		logger.Debugw("closing chrome")
		s.chromeCancel()
		s.chromeCancel = nil
	}

	if s.xvfb != nil {
		logger.Debugw("closing X display")
		err := s.xvfb.Process.Signal(os.Interrupt)
		if err != nil {
			logger.Errorw("failed to kill xvfb", err)
		}
		s.xvfb = nil
	}

	if s.pulseSink != "" {
		logger.Debugw("unloading pulse module")
		err := exec.Command("pactl", "unload-module", s.pulseSink).Run()
		if err != nil {
			logger.Errorw("failed to unload pulse sink", err)
		}
	}
}

type infoLogger struct {
	cmd string
}

func (l *infoLogger) Write(p []byte) (int, error) {
	logger.Infow(fmt.Sprintf("%s: %s", l.cmd, string(p)))
	return len(p), nil
}

// creates a new pulse audio sink
func (s *WebSource) createPulseSink(ctx context.Context, p *config.PipelineConfig) error {
	ctx, span := tracer.Start(ctx, "WebInput.createPulseSink")
	defer span.End()

	logger.Debugw("creating pulse sink")
	cmd := exec.Command("pactl",
		"load-module", "module-null-sink",
		fmt.Sprintf("sink_name=\"%s\"", p.Info.EgressId),
		fmt.Sprintf("sink_properties=device.description=\"%s\"", p.Info.EgressId),
	)
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &infoLogger{cmd: "pactl"}
	err := cmd.Run()
	if err != nil {
		return errors.Fatal(errors.ErrProcessStartFailed(err))
	}

	s.pulseSink = strings.TrimRight(b.String(), "\n")
	return nil
}

// creates a new xvfb display
func (s *WebSource) launchXvfb(ctx context.Context, p *config.PipelineConfig) error {
	ctx, span := tracer.Start(ctx, "WebInput.launchXvfb")
	defer span.End()

	dims := fmt.Sprintf("%dx%dx%d", p.Width, p.Height, p.Depth)
	logger.Debugw("creating X display", "display", p.Display, "dims", dims)
	xvfb := exec.Command("Xvfb", p.Display, "-screen", "0", dims, "-ac", "-nolisten", "tcp", "-nolisten", "unix")
	xvfb.Stderr = &infoLogger{cmd: "xvfb"}
	if err := xvfb.Start(); err != nil {
		return errors.Fatal(errors.ErrProcessStartFailed(err))
	}

	s.xvfb = xvfb
	return nil
}

// launches chrome and navigates to the url
func (s *WebSource) launchChrome(ctx context.Context, p *config.PipelineConfig, insecure bool) error {
	ctx, span := tracer.Start(ctx, "WebInput.launchChrome")
	defer span.End()

	webUrl := p.WebUrl
	if webUrl == "" {
		// build input url
		inputUrl, err := url.Parse(p.BaseUrl)
		if err != nil {
			return err
		}
		values := inputUrl.Query()
		values.Set("layout", p.Layout)
		values.Set("url", p.WsUrl)
		values.Set("token", p.Token)
		inputUrl.RawQuery = values.Encode()
		webUrl = inputUrl.String()
	}

	logger.Debugw("launching chrome", "url", webUrl, "sandbox", p.EnableChromeSandbox, "insecure", p.Insecure)

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,

		// puppeteer default behavior
		chromedp.Flag("disable-infobars", true),
		chromedp.Flag("excludeSwitches", "enable-automation"),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("enable-features", "NetworkService,NetworkServiceInProcess"),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-features", "AudioServiceOutOfProcess,site-per-process,TranslateUI,BlinkGenPropertyTrees"),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-ipc-flooding-protection", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("force-color-profile", "srgb"),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("safebrowsing-disable-auto-update", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("use-mock-keychain", true),

		// custom args
		chromedp.Flag("kiosk", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		chromedp.Flag("window-position", "0,0"),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", p.Width, p.Height)),

		// output
		chromedp.Env(fmt.Sprintf("PULSE_SINK=%s", p.Info.EgressId)),
		chromedp.Flag("display", p.Display),

		// sandbox
		chromedp.Flag("no-sandbox", !p.EnableChromeSandbox),
	}

	if insecure {
		opts = append(opts,
			chromedp.Flag("disable-web-security", true),
			chromedp.Flag("allow-running-insecure-content", true),
		)
	}

	allocCtx, _ := chromedp.NewExecAllocator(context.Background(), opts...)
	chromeCtx, cancel := chromedp.NewContext(allocCtx)
	s.chromeCancel = cancel

	chromedp.ListenTarget(chromeCtx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			for _, arg := range ev.Args {
				var val interface{}
				err := json.Unmarshal(arg.Value, &val)
				if err != nil {
					continue
				}

				switch fmt.Sprint(val) {
				case startRecordingLog:
					logger.Infow("chrome: START_RECORDING")
					if s.startRecording != nil {
						select {
						case <-s.startRecording:
							continue
						default:
							close(s.startRecording)
						}
					}
				case endRecordingLog:
					logger.Infow("chrome: END_RECORDING")
					if s.endRecording != nil {
						select {
						case <-s.endRecording:
							continue
						default:
							close(s.endRecording)
						}
					}
				}
			}

		case *runtime.EventExceptionThrown:
			logChrome("exception", ev)
			if s.info.Details == "" {
				if exceptionDetails := ev.ExceptionDetails; exceptionDetails != nil {
					if exception := exceptionDetails.Exception; exception != nil {
						s.info.Details = fmt.Sprintf("Uncaught chrome exception: %s", exception.Description)
					}
				}
			}
		}
	})

	var errString string
	err := chromedp.Run(chromeCtx,
		chromedp.Navigate(webUrl),
		chromedp.Evaluate(`
			if (document.querySelector('div.error')) {
				document.querySelector('div.error').innerText;
			} else {
				''
			}`, &errString,
		),
	)
	if err == nil && errString != "" {
		err = errors.New(errString)
	}
	return err
}

func logChrome(eventType string, ev interface{ MarshalJSON() ([]byte, error) }) {
	values := make([]interface{}, 0)
	if j, err := ev.MarshalJSON(); err == nil {
		m := make(map[string]interface{})
		_ = json.Unmarshal(j, &m)
		for k, v := range m {
			values = append(values, k, v)
		}
	}
	logger.Debugw(fmt.Sprintf("chrome %s", eventType), values...)
}

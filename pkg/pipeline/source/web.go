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
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/chromedp/cdproto/inspector"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/frostbyte73/core"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
)

const (
	startRecordingLog = "START_RECORDING"
	endRecordingLog   = "END_RECORDING"

	chromeFailedToStart = "chrome failed to start:"
	chromeTimeout       = time.Second * 30
	chromeRetries       = 3
)

type WebSource struct {
	pulseSink    string
	xvfb         *exec.Cmd
	closeChrome  context.CancelFunc
	chromeLogger *lumberjack.Logger

	startRecording core.Fuse
	endRecording   core.Fuse
	closed         core.Fuse

	info *livekit.EgressInfo
}

func NewWebSource(ctx context.Context, p *config.PipelineConfig) (*WebSource, error) {
	ctx, span := tracer.Start(ctx, "WebInput.New")
	defer span.End()

	p.Display = fmt.Sprintf(":%d", 10+rand.Intn(2147483637))

	s := &WebSource{
		info: p.Info,
	}
	if !p.AwaitStartSignal {
		s.startRecording.Break()
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

	if err := s.launchChrome(ctx, p); err != nil {
		logger.Warnw("failed to launch chrome", err)
		s.Close()
		return nil, err
	}

	return s, nil
}

func (s *WebSource) StartRecording() <-chan struct{} {
	return s.startRecording.Watch()
}

func (s *WebSource) EndRecording() <-chan struct{} {
	return s.endRecording.Watch()
}

func (s *WebSource) GetStartedAt() int64 {
	return time.Now().UnixNano()
}

func (s *WebSource) GetEndedAt() int64 {
	return time.Now().UnixNano()
}

func (s *WebSource) Close() {
	s.closed.Once(func() {
		if s.closeChrome != nil {
			logger.Debugw("closing chrome")
			s.closeChrome()
		}

		if s.xvfb != nil {
			logger.Debugw("closing X display")
			_ = s.xvfb.Process.Kill()
			_ = s.xvfb.Wait()
		}

		if s.pulseSink != "" {
			logger.Debugw("unloading pulse module")
			if err := exec.Command("pactl", "unload-module", s.pulseSink).Run(); err != nil {
				logger.Errorw("failed to unload pulse sink", err)
			}
		}
		if s.chromeLogger != nil {
			_ = s.chromeLogger.Close()
			s.chromeLogger = nil
		}
	})
}

// creates a new pulse audio sink
func (s *WebSource) createPulseSink(ctx context.Context, p *config.PipelineConfig) error {
	_, span := tracer.Start(ctx, "WebInput.createPulseSink")
	defer span.End()

	logger.Debugw("creating pulse sink")
	cmd := exec.Command("pactl",
		"load-module", "module-null-sink",
		fmt.Sprintf("sink_name=\"%s\"", p.Info.EgressId),
		fmt.Sprintf("sink_properties=device.description=\"%s\"", p.Info.EgressId),
	)
	var b bytes.Buffer
	l := medialogutils.NewCmdLogger(func(s string) {
		logger.Infow(fmt.Sprintf("pactl: %s", s))
	})
	cmd.Stdout = &b
	cmd.Stderr = l
	err := cmd.Run()
	if err != nil {
		if out := b.Bytes(); out != nil {
			_, _ = l.Write(out)
		}
		return errors.ErrProcessFailed("pulse", err)
	}

	s.pulseSink = strings.TrimRight(b.String(), "\n")
	return nil
}

// creates a new xvfb display
func (s *WebSource) launchXvfb(ctx context.Context, p *config.PipelineConfig) error {
	_, span := tracer.Start(ctx, "WebInput.launchXvfb")
	defer span.End()

	dims := fmt.Sprintf("%dx%dx%d", p.Width, p.Height, p.Depth)
	logger.Debugw("creating X display", "display", p.Display, "dims", dims)
	xvfb := exec.Command("Xvfb", p.Display, "-screen", "0", dims, "-ac", "-nolisten", "tcp", "-nolisten", "unix")
	if err := xvfb.Start(); err != nil {
		return errors.ErrProcessFailed("xvfb", err)
	}

	s.xvfb = xvfb
	return nil
}

func newChromeLogger(tmpDir string) *lumberjack.Logger {
	writer := &lumberjack.Logger{
		Filename:   filepath.Join(tmpDir, "chrome.log"),
		MaxSize:    100, // MB per file (smallest unit)
		MaxBackups: 1,   // current + 1 backup = 2 files total
		MaxAge:     7,   // days
		Compress:   false,
	}
	return writer
}

// launches chrome and navigates to the url
func (s *WebSource) launchChrome(ctx context.Context, p *config.PipelineConfig) error {
	_, span := tracer.Start(ctx, "WebInput.launchChrome")
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

	if p.Debug.EnableChromeLogging {
		s.chromeLogger = newChromeLogger(os.TempDir())
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
		chromedp.Flag("disable-features", "AudioServiceOutOfProcess,site-per-process,Translate,TranslateUI,BlinkGenPropertyTrees"),
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

		// config
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", p.Width, p.Height)),
		chromedp.Flag("disable-web-security", p.Insecure),
		chromedp.Flag("allow-running-insecure-content", p.Insecure),
		chromedp.Flag("no-sandbox", !p.EnableChromeSandbox),

		// output
		chromedp.Env(fmt.Sprintf("PULSE_SINK=%s", p.Info.EgressId)),
		chromedp.Flag("display", p.Display),
	}

	// custom
	for k, v := range p.ChromeFlags {
		opts = append(opts, chromedp.Flag(k, v))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	var err error
	var retryable bool
	for i := range chromeRetries {
		if i > 0 {
			logger.Debugw("navigation timed out, reloading")
		}

		chromeCtx, chromeCancel := chromedp.NewContext(allocCtx)
		s.closeChrome = func() {
			chromeCancel()
			allocCancel()
		}

		err, retryable = s.navigate(chromeCtx, chromeCancel, webUrl)
		if !retryable {
			break
		}
	}

	return err
}

func (s *WebSource) navigate(chromeCtx context.Context, chromeCancel context.CancelFunc, webUrl string) (error, bool) {
	chromedp.ListenTarget(chromeCtx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			if s.chromeLogger != nil {
				if b, err := json.Marshal(ev); err == nil {
					_, _ = s.chromeLogger.Write(append(b, '\n'))
				}
			}

			for _, arg := range ev.Args {
				var val interface{}
				err := json.Unmarshal(arg.Value, &val)
				if err != nil {
					continue
				}

				switch fmt.Sprint(val) {
				case startRecordingLog:
					logger.Infow("chrome: START_RECORDING")
					s.startRecording.Break()

				case endRecordingLog:
					logger.Infow("chrome: END_RECORDING")
					s.endRecording.Break()
				}
			}

		case *runtime.EventExceptionThrown:
			if s.chromeLogger != nil {
				if b, err := json.Marshal(ev); err == nil {
					_, _ = s.chromeLogger.Write(append(b, '\n'))
				}
			}
			logger.Debugw("chrome exception", "err", ev.ExceptionDetails.Error())

		case *target.EventTargetCrashed:
			logger.Errorw("chrome crashed", nil, "targetId", ev.TargetID, "status", ev.Status, "errorCode", ev.ErrorCode)

		case *inspector.EventTargetCrashed:
			logger.Errorw("chrome crashed", nil)
		}
	})

	// navigate
	var timeout *time.Timer
	var errString string
	if err := chromedp.Run(chromeCtx,
		chromedp.ActionFunc(func(_ context.Context) error {
			logger.Debugw("chrome initialized")
			// set page load timeout
			timeout = time.AfterFunc(chromeTimeout, chromeCancel)
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// use RunResponse wrapped in ActionFunc to get the response details
			r, err := chromedp.RunResponse(ctx, chromedp.Navigate(webUrl))
			if err != nil {
				return err
			}
			if r.Status >= 400 {
				return errors.PageLoadError(r.StatusText)
			}
			return nil
		}),
		chromedp.ActionFunc(func(_ context.Context) error {
			// cancel timer
			timeout.Stop()
			return nil
		}),
		chromedp.Evaluate(`
			if (document.querySelector('div.error')) {
				document.querySelector('div.error').innerText;
			} else {
				''
			}`, &errString),
	); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return errors.PageLoadError("timed out"), true
		}
		if strings.HasPrefix(err.Error(), chromeFailedToStart) {
			return errors.ChromeError(err), false
		}
		return errors.PageLoadError(err.Error()), false
	} else if errString != "" {
		return errors.TemplateError(errString), false
	}

	return nil, false
}

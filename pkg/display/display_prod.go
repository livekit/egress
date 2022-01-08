//go:build !test
// +build !test

package display

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-recorder/pkg/config"
)

const (
	startRecording = "START_RECORDING"
	endRecording   = "END_RECORDING"
)

type Display struct {
	xvfb         *exec.Cmd
	chromeCancel context.CancelFunc
	startChan    chan struct{}
	endChan      chan struct{}
}

func New() *Display {
	return &Display{
		startChan: make(chan struct{}, 1),
		endChan:   make(chan struct{}, 1),
	}
}

func (d *Display) Launch(conf *config.Config, url string, opts *livekit.RecordingOptions, isTemplate bool) error {
	if err := d.launchXvfb(conf.Display, opts.Width, opts.Height, opts.Depth); err != nil {
		return err
	}
	if err := d.launchChrome(conf, url, opts.Width, opts.Height, isTemplate); err != nil {
		return err
	}
	return nil
}

func (d *Display) launchXvfb(display string, width, height, depth int32) error {
	dims := fmt.Sprintf("%dx%dx%d", width, height, depth)
	logger.Debugw("launching xvfb", "dims", dims)
	xvfb := exec.Command("Xvfb", display, "-screen", "0", dims, "-ac", "-nolisten", "tcp")
	if err := xvfb.Start(); err != nil {
		return err
	}
	d.xvfb = xvfb
	return nil
}

func (d *Display) launchChrome(conf *config.Config, url string, width, height int32, isTemplate bool) error {
	logger.Debugw("launching chrome", "url", url)

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,
		chromedp.NoSandbox,

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
		chromedp.Flag("disable-features", "site-per-process,TranslateUI,BlinkGenPropertyTrees"),
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
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		chromedp.Flag("window-position", "0,0"),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", width, height)),
		chromedp.Flag("display", conf.Display),
	}

	if conf.Insecure {
		opts = append(opts,
			chromedp.Flag("disable-web-security", true),
			chromedp.Flag("allow-running-insecure-content", true),
		)
	}

	allocCtx, _ := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	d.chromeCancel = cancel

	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			args := make([]string, 0, len(ev.Args))
			for _, arg := range ev.Args {
				var val interface{}
				err := json.Unmarshal(arg.Value, &val)
				if err != nil {
					continue
				}
				msg := fmt.Sprint(val)
				args = append(args, msg)
				switch msg {
				case startRecording:
					d.startChan <- struct{}{}
				case endRecording:
					d.endChan <- struct{}{}
				default:
				}
			}
			logger.Debugw(fmt.Sprintf("chrome console %s", ev.Type.String()), "msg", strings.Join(args, " "))
		}
	})

	var err error
	var errString string
	if isTemplate {
		err = chromedp.Run(ctx,
			chromedp.Navigate(url),
			chromedp.Evaluate(`
				if (document.querySelector('div.error')) {
					document.querySelector('div.error').innerText;
				} else {
					''
				}`, &errString,
			),
		)
	} else {
		err = chromedp.Run(ctx, chromedp.Navigate(url))
	}
	if err == nil && errString != "" {
		err = errors.New(errString)
	}
	return err
}

func (d *Display) WaitForRoom() {
	<-d.startChan
}

func (d *Display) EndMessage() chan struct{} {
	return d.endChan
}

func (d *Display) Close() {
	if d.chromeCancel != nil {
		d.chromeCancel()
		d.chromeCancel = nil
	}
	close(d.endChan)
	if d.xvfb != nil {
		err := d.xvfb.Process.Signal(os.Interrupt)
		if err != nil {
			logger.Errorw("failed to kill xvfb", err)
		}
		d.xvfb = nil
	}
}

// +build !test

package display

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/chromedp/chromedp"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-recorder/pkg/config"
)

type Display struct {
	xvfb         *exec.Cmd
	chromeCancel func()
}

func New() *Display { return &Display{} }

func (d *Display) Launch(url string, width, height, depth int) error {
	if err := d.launchXvfb(width, height, depth); err != nil {
		return err
	}
	if err := d.launchChrome(url, width, height); err != nil {
		return err
	}
	return nil
}

func (d *Display) launchXvfb(width, height, depth int) error {
	dims := fmt.Sprintf("%dx%dx%d", width, height, depth)
	logger.Debugw("launching xvfb", "dims", dims)
	xvfb := exec.Command("Xvfb", config.Display, "-screen", "0", dims, "-ac", "-nolisten", "tcp")
	if err := xvfb.Start(); err != nil {
		return err
	}
	d.xvfb = xvfb
	return nil
}

func (d *Display) launchChrome(url string, width, height int) error {
	logger.Debugw("launching chrome")

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
		chromedp.Flag("display", config.Display),
	}

	allocCtx, _ := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	d.chromeCancel = cancel
	return chromedp.Run(ctx, chromedp.Navigate(url))
}

func (d *Display) Close() {
	if d.chromeCancel != nil {
		d.chromeCancel()
		d.chromeCancel = nil
	}
	if d.xvfb != nil {
		err := d.xvfb.Process.Signal(os.Interrupt)
		if err != nil {
			logger.Errorw("failed to kill xvfb", err)
		}
		d.xvfb = nil
	}
}

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/tracer"
)

// creates a new pulse audio sink
func (s *WebInput) createPulseSink(ctx context.Context, p *params.Params) error {
	ctx, span := tracer.Start(ctx, "WebInput.createPulseSink")
	defer span.End()

	cmd := exec.Command("pactl",
		"load-module", "module-null-sink",
		fmt.Sprintf("sink_name=\"%s\"", p.Info.EgressId),
		fmt.Sprintf("sink_properties=device.description=\"%s\"", p.Info.EgressId),
	)
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		return err
	}

	s.pulseSink = b.String()
	return nil
}

// creates a new xvfb display
func (s *WebInput) launchXvfb(ctx context.Context, p *params.Params) error {
	ctx, span := tracer.Start(ctx, "WebInput.launchXvfb")
	defer span.End()

	dims := fmt.Sprintf("%dx%dx%d", p.Width, p.Height, p.Depth)
	s.logger.Debugw("launching xvfb", "display", p.Display, "dims", dims)
	xvfb := exec.Command("Xvfb", p.Display, "-screen", "0", dims, "-ac", "-nolisten", "tcp")
	if err := xvfb.Start(); err != nil {
		return err
	}

	s.xvfb = xvfb
	return nil
}

// launches chrome and navigates to the url
func (s *WebInput) launchChrome(ctx context.Context, p *params.Params, insecure bool) error {
	ctx, span := tracer.Start(ctx, "WebInput.launchChrome")
	defer span.End()

	webUrl := p.WebUrl
	if webUrl == "" {
		// create start and end channels
		s.startRecording = make(chan struct{})
		s.endRecording = make(chan struct{})

		// build input url
		inputUrl, err := url.Parse(p.TemplateBase)
		if err != nil {
			return err
		}
		values := inputUrl.Query()
		values.Set("layout", p.Layout)
		values.Set("url", p.LKUrl)
		values.Set("token", p.Token)
		inputUrl.RawQuery = values.Encode()
		webUrl = inputUrl.String()
	}

	s.logger.Debugw("launching chrome", "url", webUrl)

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
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", p.Width, p.Height)),

		// output
		chromedp.Env(fmt.Sprintf("PULSE_SINK=%s", p.Info.EgressId)),
		chromedp.Flag("display", p.Display),
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
				case startRecordingLog:
					select {
					case <-s.startRecording:
						continue
					default:
						close(s.startRecording)
					}
				case endRecordingLog:
					select {
					case <-s.endRecording:
						continue
					default:
						close(s.endRecording)
					}
				}
			}
			s.logger.Debugw(fmt.Sprintf("chrome %s: %s", ev.Type.String(), strings.Join(args, " ")))
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

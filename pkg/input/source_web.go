package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"

	"github.com/livekit/livekit-egress/pkg/config"
)

const (
	startRecording = "START_RECORDING"
	endRecording   = "END_RECORDING"
)

type webSource struct {
	xvfb         *exec.Cmd
	chromeCancel context.CancelFunc
	startChan    chan struct{}
	endChan      chan struct{}
}

func newWebSource(conf *config.Config, params *Params, opts *config.RecordingOptions) (Source, error) {
	s := &webSource{
		startChan: make(chan struct{}),
		endChan:   make(chan struct{}),
	}

	var inputUrl string
	var err error
	if opts.CustomInputURL != "" {
		inputUrl = opts.CustomInputURL
		close(s.startChan)
	} else {
		inputUrl, err = getInputUrl(conf, params.Layout, params.RoomName, params.CustomBase)
		if err != nil {
			return nil, err
		}
	}

	if err = s.launchXvfb(conf.Display, opts.Width, opts.Height, opts.Depth); err != nil {
		logger.Errorw("failed to launch xvfb", err)
		return nil, err
	}
	if err = s.launchChrome(conf, inputUrl, opts.Width, opts.Height); err != nil {
		logger.Errorw("failed to launch chrome", err)
		s.Close()
		return nil, err
	}

	return s, nil
}

func getInputUrl(conf *config.Config, layout, roomName, customBase string) (string, error) {
	token, err := buildToken(conf, roomName)
	if err != nil {
		return "", err
	}

	baseUrl := conf.TemplateAddress
	if customBase != "" {
		baseUrl = customBase
	}

	return fmt.Sprintf("%s/%s?url=%s&token=%s",
		baseUrl, layout, url.QueryEscape(conf.WsUrl), token), nil
}

func buildToken(conf *config.Config, roomName string) (string, error) {
	f := false
	t := true
	grant := &auth.VideoGrant{
		RoomJoin:       true,
		Room:           roomName,
		CanSubscribe:   &t,
		CanPublish:     &f,
		CanPublishData: &f,
		Hidden:         true,
		Recorder:       true,
	}

	at := auth.NewAccessToken(conf.ApiKey, conf.ApiSecret).
		AddGrant(grant).
		SetIdentity(utils.NewGuid(utils.EgressPrefix)).
		SetValidFor(24 * time.Hour)

	return at.ToJWT()
}

func (s *webSource) launchXvfb(display string, width, height, depth int32) error {
	dims := fmt.Sprintf("%dx%dx%d", width, height, depth)
	logger.Debugw("launching xvfb", "display", display, "dims", dims)
	xvfb := exec.Command("Xvfb", display, "-screen", "0", dims, "-ac", "-nolisten", "tcp")
	if err := xvfb.Start(); err != nil {
		return err
	}
	s.xvfb = xvfb
	return nil
}

func (s *webSource) launchChrome(conf *config.Config, url string, width, height int32) error {
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
	s.chromeCancel = cancel

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
					close(s.startChan)
				case endRecording:
					close(s.endChan)
				default:
				}
			}
			logger.Debugw(fmt.Sprintf("chrome console %s", ev.Type.String()), "msg", strings.Join(args, " "))
		}
	})

	var errString string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
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

func (s *webSource) RoomStarted() chan struct{} {
	return s.startChan
}

func (s *webSource) RoomEnded() chan struct{} {
	return s.endChan
}

func (s *webSource) Close() {
	if s.chromeCancel != nil {
		s.chromeCancel()
		s.chromeCancel = nil
	}

	if s.xvfb != nil {
		err := s.xvfb.Process.Signal(os.Interrupt)
		if err != nil {
			logger.Errorw("failed to kill xvfb", err)
		}
		s.xvfb = nil
	}
}

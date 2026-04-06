package gstreamer

import (
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"
)

var gstInitOnce sync.Once

func initGStreamer(t *testing.T) {
	t.Helper()
	gstInitOnce.Do(func() {
		gst.Init(nil)
	})
}

// TestAddSourceBinRace attempts to reproduce the race condition where a source
// bin is added via AddSourceBin concurrently with the pipeline's PAUSED → PLAYING
// transition. In production this causes either FlowFlushing (priv->flushing stuck
// true) or a dead streaming thread (need-data never fires).
//
// Pipeline topology matches production:
//
//	Source bins: appsrc → rtpopusdepay → opusdec → audiorate → queue(leaky) → audioconvert → audioresample → capsfilter
//	Sink bin:   audiotestsrc → capsfilter | audiomixer → capsfilter → queue → fakesink
//
// Production-matching timing:
//   - GLib main loop running during the race (bus messages processed via watch, same as production)
//   - Bus watch triggers concurrent PushBuffer (simulates pushSamples waking on Playing fuse)
//   - Timing variants: bin-add-first (production pattern), simultaneous, playing-first
func TestAddSourceBinRace(t *testing.T) {
	initGStreamer(t)

	const iterations = 1000

	var (
		flowFlushing int
		needDataMiss int
		pushOK       int
		watchFired   int
		loopConfirm  int
		needDataOK   int
		errCount     int
		byVariant    [3]int // [bin-add-first, simultaneous, playing-first]
	)

	for i := range iterations {
		r := runRaceIteration(t, i)
		if r.flowFlushing {
			flowFlushing++
		}
		if r.needDataMissing {
			needDataMiss++
		}
		if r.pushFlowOK {
			pushOK++
		}
		if r.watchTriggered {
			watchFired++
		}
		if r.loopRan {
			loopConfirm++
		}
		if r.needDataReceived {
			needDataOK++
		}
		if r.setupError {
			errCount++
		}
		byVariant[r.variant]++
	}

	t.Logf("results over %d iterations:", iterations)
	t.Logf("  race hits: flowFlushing=%d needDataMiss=%d", flowFlushing, needDataMiss)
	t.Logf("  healthy:   loopConfirmed=%d watchFired=%d pushOK=%d needDataOK=%d",
		loopConfirm, watchFired, pushOK, needDataOK)
	t.Logf("  errors:    setupErrors=%d", errCount)
	t.Logf("  variants:  binAddFirst=%d simultaneous=%d playingFirst=%d",
		byVariant[0], byVariant[1], byVariant[2])

	if flowFlushing > 0 || needDataMiss > 0 {
		t.Logf("RACE DETECTED: reproduced the dynamic bin addition race condition")
	}

	// Verify the test machinery actually worked on every non-error iteration
	healthy := iterations - errCount
	require.Equal(t, healthy, loopConfirm, "GLib main loop should have run on every non-error iteration")
	require.Equal(t, healthy, watchFired, "bus watch should have fired on every non-error iteration")
	require.Equal(t, healthy, pushOK+flowFlushing, "PushBuffer should have returned OK or FlowFlushing on every non-error iteration")
	require.Equal(t, healthy, needDataOK+needDataMiss, "need-data should have been checked on every non-error iteration")
}

const (
	raceAudioCaps = "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved"
	raceRTPCaps   = "application/x-rtp,media=audio,payload=111,encoding-name=OPUS,clock-rate=48000"
	// 3ms tolerance matches production audioRateTolerance
	raceAudioRateTolerance = 3 * time.Millisecond
	// 1s latency for queue max-size-time (production uses PipelineLatency)
	raceQueueLatency = time.Second

	variantBinAddFirst  = 0
	variantSimultaneous = 1
	variantPlayingFirst = 2
)

type raceResult struct {
	flowFlushing     bool
	needDataMissing  bool
	pushFlowOK       bool
	watchTriggered   bool
	loopRan          bool
	needDataReceived bool
	setupError       bool
	variant          int
}

// buildAudioConverterChain creates the production audio converter chain:
// audiorate → queue(leaky=downstream) → audioconvert → audioresample → capsfilter
func buildAudioConverterChain(t *testing.T) []*gst.Element {
	t.Helper()

	audioRate, err := gst.NewElement("audiorate")
	require.NoError(t, err)
	require.NoError(t, audioRate.SetProperty("skip-to-first", true))
	require.NoError(t, audioRate.SetProperty("tolerance", uint64(raceAudioRateTolerance)))

	audioQueue, err := gst.NewElement("queue")
	require.NoError(t, err)
	require.NoError(t, audioQueue.SetProperty("max-size-time", uint64(raceQueueLatency)))
	require.NoError(t, audioQueue.SetProperty("max-size-bytes", uint(0)))
	require.NoError(t, audioQueue.SetProperty("max-size-buffers", uint(0)))
	audioQueue.SetArg("leaky", "downstream")

	audioConvert, err := gst.NewElement("audioconvert")
	require.NoError(t, err)

	audioResample, err := gst.NewElement("audioresample")
	require.NoError(t, err)

	capsFilter, err := gst.NewElement("capsfilter")
	require.NoError(t, err)
	require.NoError(t, capsFilter.SetProperty("caps", gst.NewCapsFromString(raceAudioCaps)))

	return []*gst.Element{audioRate, audioQueue, audioConvert, audioResample, capsFilter}
}

func runRaceIteration(t *testing.T, iteration int) raceResult {
	t.Helper()

	var result raceResult

	// Determine timing variant
	switch v := iteration % 10; {
	case v < 5:
		result.variant = variantBinAddFirst
	case v < 8:
		result.variant = variantSimultaneous
	default:
		result.variant = variantPlayingFirst
	}

	callbacks := &Callbacks{
		GstReady:   make(chan struct{}),
		BuildReady: make(chan struct{}),
	}
	close(callbacks.GstReady)
	close(callbacks.BuildReady)

	p, err := NewPipeline("test_race", 0, callbacks)
	require.NoError(t, err)

	// Build sink bin: audiotestsrc → capsfilter | audiomixer → capsfilter → queue → fakesink
	audioBin := p.NewBin("audio")

	audioTestSrc, err := gst.NewElement("audiotestsrc")
	require.NoError(t, err)
	require.NoError(t, audioTestSrc.SetProperty("volume", 0.0))
	require.NoError(t, audioTestSrc.SetProperty("do-timestamp", true))
	require.NoError(t, audioTestSrc.SetProperty("is-live", true))
	require.NoError(t, audioTestSrc.SetProperty("samplesperbuffer", 960)) // 20ms @ 48kHz

	testSrcCaps, err := gst.NewElement("capsfilter")
	require.NoError(t, err)
	require.NoError(t, testSrcCaps.SetProperty("caps", gst.NewCapsFromString(raceAudioCaps)))

	testSrcBin := p.NewBin("audio_test_src")
	require.NoError(t, testSrcBin.AddElements(audioTestSrc, testSrcCaps))

	audioMixer, err := gst.NewElement("audiomixer")
	require.NoError(t, err)

	mixerCaps, err := gst.NewElement("capsfilter")
	require.NoError(t, err)
	require.NoError(t, mixerCaps.SetProperty("caps", gst.NewCapsFromString(raceAudioCaps)))

	outQueue, err := gst.NewElement("queue")
	require.NoError(t, err)

	fakeSink, err := gst.NewElement("fakesink")
	require.NoError(t, err)
	require.NoError(t, fakeSink.SetProperty("async", false))

	require.NoError(t, audioBin.AddElements(audioMixer, mixerCaps, outQueue, fakeSink))
	require.NoError(t, audioBin.AddSourceBin(testSrcBin))
	require.NoError(t, p.AddSinkBin(audioBin))

	// Pre-existing source bins with full production chain to widen race window
	const preExistingSources = 5
	preAppSrcs := make([]*app.Source, preExistingSources)
	for i := range preExistingSources {
		src, err := app.NewAppSrc()
		require.NoError(t, err)
		src.SetArg("format", "time")
		require.NoError(t, src.SetProperty("is-live", true))
		require.NoError(t, src.SetProperty("caps", gst.NewCapsFromString(raceRTPCaps)))

		rtpDepay, err := gst.NewElement("rtpopusdepay")
		require.NoError(t, err)
		opusDec, err := gst.NewElement("opusdec")
		require.NoError(t, err)

		srcBin := p.NewBin(fmt.Sprintf("app_pre_%d", i))
		srcBin.SetEOSFunc(func() bool { return false })
		require.NoError(t, srcBin.AddElement(src.Element))
		require.NoError(t, srcBin.AddElements(rtpDepay, opusDec))
		require.NoError(t, srcBin.AddElements(buildAudioConverterChain(t)...))
		require.NoError(t, audioBin.AddSourceBin(srcBin))
		preAppSrcs[i] = src
	}

	require.NoError(t, p.Link())

	_, ok := p.UpgradeState(StateStarted)
	require.True(t, ok)

	require.NoError(t, p.SetState(gst.StatePaused))

	// Prepare the race source bin with full production chain:
	// appsrc → rtpopusdepay → opusdec → audiorate → queue(leaky) → audioconvert → audioresample → capsfilter
	raceSrc, err := app.NewAppSrc()
	require.NoError(t, err)
	raceSrc.SetArg("format", "time")
	require.NoError(t, raceSrc.SetProperty("is-live", true))
	require.NoError(t, raceSrc.SetProperty("caps", gst.NewCapsFromString(raceRTPCaps)))

	rtpOpusDepay, err := gst.NewElement("rtpopusdepay")
	require.NoError(t, err)

	opusDec, err := gst.NewElement("opusdec")
	require.NoError(t, err)

	raceBin := p.NewBin("app_race_track")
	raceBin.SetEOSFunc(func() bool { return false })
	require.NoError(t, raceBin.AddElement(raceSrc.Element))
	require.NoError(t, raceBin.AddElements(rtpOpusDepay, opusDec))
	require.NoError(t, raceBin.AddElements(buildAudioConverterChain(t)...))

	// Install need-data callback BEFORE the race so we don't miss the first emission.
	needData := make(chan struct{}, 1)
	raceSrc.SetCallbacks(&app.SourceCallbacks{
		NeedDataFunc: func(_ *app.Source, _ uint) {
			select {
			case needData <- struct{}{}:
			default:
			}
		},
	})

	// Install a bus watch (processed by the GLib main loop, same as production).
	// When the race bin reports PLAYING, break a "fuse" (channel close) so a
	// separate pushSamples goroutine wakes up — matching the production path:
	//   bus watch → Playing(trackID) → opChan → track worker → writer.Playing()
	//     → fuse breaks → pushSamples goroutine wakes on separate OS thread
	// Return false once done to remove the GSource from the default context.
	var watchDone atomic.Bool
	playingFuse := make(chan struct{})
	p.SetWatch(func(msg *gst.Message) bool {
		if watchDone.Load() {
			return false // remove watch
		}
		if msg.Type() == gst.MessageStateChanged {
			_, newState := msg.ParseStateChanged()
			if newState == gst.StatePlaying && strings.HasPrefix(msg.Source(), "app_race") {
				watchDone.Store(true)
				close(playingFuse) // break the fuse
				return false       // remove watch
			}
		}
		return true
	})

	// Simulate pushSamples: a separate goroutine (own OS thread) waits for the
	// playing fuse, then pushes buffers in a tight loop — matching production
	// where pushSamples continuously dequeues from the jitter buffer and calls
	// PushBuffer. This catches narrow flushing windows that a single push misses.
	const pushCount = 50
	pushResult := make(chan gst.FlowReturn, 1)
	go func() {
		runtime.LockOSThread()
		select {
		case <-playingFuse:
		case <-time.After(2 * time.Second):
			pushResult <- gst.FlowError
			return
		}
		for i := range pushCount {
			buf := gst.NewBufferWithSize(96)
			pts := gst.ClockTime(uint64(i) * uint64(time.Millisecond))
			buf.SetPresentationTimestamp(pts)
			buf.SetDuration(gst.ClockTime(time.Millisecond))
			if flow := raceSrc.PushBuffer(buf); flow != gst.FlowOK {
				pushResult <- flow
				return
			}
		}
		pushResult <- gst.FlowOK
	}()

	// Start the GLib main loop (same as production's Pipeline.Run()).
	// Use IdleAdd to confirm the loop is actually iterating before proceeding.
	loopRunning := make(chan struct{})
	loopDone := make(chan struct{})
	glib.IdleAdd(func() bool {
		select {
		case <-loopRunning:
		default:
			close(loopRunning)
		}
		return false // remove idle source
	})
	go func() {
		runtime.LockOSThread()
		p.loop.Run()
		close(loopDone)
	}()

	select {
	case <-loopRunning:
		result.loopRan = true
	case <-time.After(time.Second):
		t.Fatal("GLib main loop did not start within 1s")
	}

	// Race: SetState(PLAYING) vs AddSourceBin
	playingDone := make(chan error, 1)
	addDone := make(chan error, 1)

	switch result.variant {
	case variantBinAddFirst:
		// Bin-add-first with 0-100μs head start
		// (matches production: "track subscribes ~1ms before PAUSED→PLAYING")
		go func() {
			runtime.LockOSThread()
			addDone <- audioBin.AddSourceBin(raceBin)
		}()
		time.Sleep(time.Duration(rand.Int63n(100)) * time.Microsecond)
		go func() {
			runtime.LockOSThread()
			playingDone <- p.SetState(gst.StatePlaying)
		}()

	case variantSimultaneous:
		// Simultaneous: barrier-synchronized start
		barrier := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			<-barrier
			playingDone <- p.SetState(gst.StatePlaying)
		}()
		go func() {
			runtime.LockOSThread()
			<-barrier
			addDone <- audioBin.AddSourceBin(raceBin)
		}()
		close(barrier)

	case variantPlayingFirst:
		// Playing-first with 0-100μs head start
		go func() {
			runtime.LockOSThread()
			playingDone <- p.SetState(gst.StatePlaying)
		}()
		time.Sleep(time.Duration(rand.Int63n(100)) * time.Microsecond)
		go func() {
			runtime.LockOSThread()
			addDone <- audioBin.AddSourceBin(raceBin)
		}()
	}

	err1 := <-playingDone
	err2 := <-addDone

	if err1 != nil || err2 != nil {
		result.setupError = true
		watchDone.Store(true)
		p.loop.Quit()
		<-loopDone
		_ = p.SetState(gst.StateNull)
		return result
	}

	// Wait for pushSamples goroutine result (fuse break + 50 buffer pushes)
	select {
	case flow := <-pushResult:
		result.watchTriggered = true // fuse broke (bus watch fired)
		switch flow {
		case gst.FlowFlushing:
			result.flowFlushing = true
		case gst.FlowOK:
			result.pushFlowOK = true // all 50 pushes succeeded
		}
	case <-time.After(2 * time.Second):
		// Bus watch never saw PLAYING for the race bin
		result.needDataMissing = true
	}

	// Check need-data — if the streaming thread is alive, it should have
	// fired by now (or shortly after PushBuffer woke it from cond_wait).
	if !result.flowFlushing && !result.needDataMissing {
		select {
		case <-needData:
			result.needDataReceived = true
		case <-time.After(500 * time.Millisecond):
			result.needDataMissing = true
		}
	}

	if result.flowFlushing || result.needDataMissing {
		t.Logf("--- RACE HIT iter=%d (flowFlushing=%v, needDataMissing=%v) ---",
			iteration, result.flowFlushing, result.needDataMissing)
		t.Logf("appsrc state: %s", raceSrc.Element.GetCurrentState().String())
		t.Logf("race bin state: %s", raceBin.bin.GetCurrentState().String())
		t.Logf("pipeline state: %s", p.pipeline.GetCurrentState().String())
	}

	// Cleanup: quit loop first, then destroy pipeline
	watchDone.Store(true)
	p.loop.Quit()
	<-loopDone
	_ = raceSrc.EndStream()
	for _, src := range preAppSrcs {
		_ = src.EndStream()
	}
	_ = p.SetState(gst.StateNull)
	time.Sleep(10 * time.Millisecond)

	return result
}

import { loadConfig, getUrl, upload} from "./config"
import { Browser, Page, launch } from 'puppeteer'
import { spawn } from 'child_process'

const Xvfb = require('xvfb');

(async () => {
	const conf = loadConfig()

	// start xvfb
	const xvfb = new Xvfb({
		displayNum: 10,
		silent: true,
		xvfb_args: ['-screen', '0', `${conf.options.input_width}x${conf.options.input_height}x${conf.options.depth}`, '-ac']
	})
	xvfb.start((err: Error) => { if (err) { console.log(err) } })

	// launch puppeteer
	const browser: Browser = await launch({
		headless: false,
		defaultViewport: {width: conf.options.input_width, height: conf.options.input_height},
		ignoreDefaultArgs: ["--enable-automation"],
		args: [
			'--kiosk', // full screen, no info bar
			'--no-sandbox', // required when running as root
			'--autoplay-policy=no-user-gesture-required', // autoplay
			'--window-position=0,0',
			`--window-size=${conf.options.input_width},${conf.options.input_height}`,
			`--display=${xvfb.display()}`,
		]
	})

	// load page
	const url = getUrl(conf)
	const page: Page = await browser.newPage()
	await page.goto(url, {waitUntil: "load"})

	// ffmpeg args
	const args: string[] = [
		'-fflags', 'nobuffer', // reduce delay
		'-fflags', '+igndts', // generate dts
		'-y', // automatically overwrite

		// audio (pulse grab)
		'-thread_queue_size', '1024', // avoid thread message queue blocking
		'-ac', '2', // 2 channels
		'-f', 'pulse', '-i', 'grab.monitor',

		// video (x11 grab)
		"-draw_mouse", "0", // don't draw the mouse
		'-thread_queue_size', '1024', // avoid thread message queue blocking
		'-s', `${conf.options.input_width}x${conf.options.input_height}`,
		'-r', `${conf.options.framerate}`,
		'-f', 'x11grab', '-i', `${xvfb.display()}.0`,

		// output audio
		'-c:a', 'aac', '-b:a', `${conf.options.audio_bitrate}k`, '-ar', `${conf.options.audio_frequency}`,
		'-ac', '2', '-af', 'aresample=async=1',
		// output video
		'-c:v', 'libx264', '-preset', 'veryfast', '-tune', 'zerolatency',
		'-b:v', `${conf.options.video_bitrate}k`,
	]

	// output scaling
	if (conf.options.output_width && conf.options.output_height) {
		args.push('-s', `${conf.options.output_width}x${conf.options.output_height}`)
	}

	// output location
	if (conf.output.rtmp) {
		args.push(
			// streaming settings
			'-maxrate', `${conf.options.video_bitrate}k`,
			'-bufsize', `${conf.options.video_bitrate * 2}k`,
			'-f', 'flv', conf.output.rtmp,
		)
		console.log(`Streaming to ${conf.output.rtmp}`)
	} else if (conf.output.file) {
		const filename = conf.output.file
		args.push(filename)
		console.log(`Writing to ${filename}`)
	} else {
		throw Error("Missing ffmpeg output")
	}

	// spawn ffmpeg
	console.log('Start recording')
	const ffmpeg = spawn('ffmpeg', args)
	ffmpeg.stdout.pipe(process.stdout)
	ffmpeg.stderr.pipe(process.stderr)
	ffmpeg.on('error', (err) => {
		console.log(`ffmpeg error: ${err}`)
	})
	ffmpeg.on('close', (code, signal) => {
		console.log(`ffmpeg closed. code: ${code}, signal: ${signal}`)
		xvfb.stop()
		upload(conf)
	});

	let stopped = false
	const stop = async () => {
		if (stopped) {
			return
		}
		stopped = true
		console.log('End recording')
		ffmpeg.kill('SIGINT')
		await browser.close()
	}
	process.once('SIGINT', await stop)
	process.once('SIGTERM', await stop)

	// wait for END_RECORDING
	page.on('console', async (msg) => {
		if (msg.text() === 'END_RECORDING') {
			await stop()
		}
	})
})().catch((err) => {
	console.log(err)
});

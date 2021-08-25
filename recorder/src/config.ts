type Config = {
    api_key?: string
    api_secret?: string
    input: {
        url?: string
        template?: {
            layout: string
            ws_url: string
            token?: string
            room_name?: string
        }
    }
    output: {
        file?: string
        rtmp?: string
        s3?: {
            bucket: string
            key: string
            access_key?: string
            secret?: string
        }

    }
    options: {
        preset?: string | number
        input_width: number
        input_height: number
        output_width?: number
        output_height?: number
        depth: number
        framerate: number
        audio_bitrate: number
        audio_frequency: number
        video_bitrate: number
    }
}

export function loadConfig(): Config {
    if (!process.env.LIVEKIT_RECORDER_CONFIG) {
        throw Error('LIVEKIT_RECORDER_CONFIG, LIVEKIT_URL or Template required')
    }

    // load config from env
    const json = JSON.parse(process.env.LIVEKIT_RECORDER_CONFIG)
    const conf: Config = {
        api_key: json.api_key,
        api_secret: json.api_secret,
        input: json.input,
        output: json.output,
        options: {
            input_width: 1920,
            input_height: 1080,
            depth: 24,
            framerate: 30,
            audio_bitrate: 128,
            audio_frequency: 44100,
            video_bitrate: 4500,
        }
    }

    switch(json.options?.preset) {
        case "720p30":
        case "HD_30":
        case 1:
            conf.options.input_width = 1280
            conf.options.input_height = 720
            conf.options.video_bitrate = 3000
            break
        case "720p60":
        case "HD_60":
        case 2:
            conf.options.input_width = 1280
            conf.options.input_height = 720
            conf.options.framerate = 60
            break
        case "1080p30":
        case "FULL_HD_30":
        case 3:
            // default
            break
        case "1080p60":
        case "FULL_HD_60":
        case 4:
            conf.options.framerate = 60
            conf.options.video_bitrate = 6000
            break
        default:
            conf.options = {...conf.options, ...json.options}
    }

    // write to file if no output specified
    if (!(conf.output.file || conf.output.rtmp)) {
        const now = new Date().toISOString().
            replace(/T/, '_').
            replace(/\..+/, '')
        conf.output.file = `recording_${now}.mp4`
    }

    return conf
}

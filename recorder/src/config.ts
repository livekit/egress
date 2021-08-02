type Config = {
    api_key?: string
    api_secret?: string
    input: {
        url?: string
        template?: {
            type: string
            ws_url: string
            token?: string
            room_name?: string
        }
        width: number
        height: number
        depth: number
        framerate: number
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
        width?: number
        height?: number
        audio_bitrate: string
        audio_frequency: string
        video_bitrate: string
        video_buffer: string
    }
}

export function loadConfig(): Config {
    const conf: Config = {
        input: {
            width: 1920,
            height: 1080,
            depth: 24,
            framerate: 30,
        },
        output: {
            audio_bitrate: '128k',
            audio_frequency: '44100',
            video_bitrate: '2976k',
            video_buffer: '5952k'
        }
    }

    if (process.env.LIVEKIT_RECORDER_CONFIG) {
        // load config from env
        const json = JSON.parse(process.env.LIVEKIT_RECORDER_CONFIG)
        conf.input = {...conf.input, ...json.input}
        conf.output = {...conf.output, ...json.output}
    } else {
        throw Error('LIVEKIT_RECORDER_CONFIG, LIVEKIT_URL or Template required')
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

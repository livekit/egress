type Config = {
    apiKey?: string
    apiSecret?: string
    input: {
        url?: string
        template?: {
            type: string
            wsUrl: string
            token?: string
            roomName?: string
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
            accessKey?: string
            secret?: string
        }
        width?: number
        height?: number
        audioBitrate: string
        audioFrequency: string
        videoBitrate: string
        videoBuffer: string
    }
}

export function loadConfig(): Config {
    const conf: Config = {
        input: {
            width: 1920,
            height: 1080,
            depth: 24,
            framerate: 25,
        },
        output: {
            audioBitrate: '128k',
            audioFrequency: '44100',
            videoBitrate: '2976k',
            videoBuffer: '5952k'
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
    if (!(conf.output.file || conf.output.rtmp || conf.output.s3)) {
        conf.output.file = 'recording.mp4'
    }

    return conf
}

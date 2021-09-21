import {AccessToken} from "livekit-server-sdk";
import {S3} from "aws-sdk";
import {readFileSync} from "fs";

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

export function getUrl(conf: Config): string {
    const template = conf.input.template
    if (template) {
        let token: string
        if (template.token) {
            token = template.token
        } else if (template.room_name && conf.api_key && conf.api_secret) {
            token = buildRecorderToken(template.room_name, conf.api_key, conf.api_secret)
        } else {
            throw Error('Either token, or room name, api key, and secret required')
        }
        return `https://recorder.livekit.io/#/${template.layout}?url=${encodeURIComponent(template.ws_url)}&token=${token}`
    } else if (conf.input.url) {
        return conf.input.url
    }

    throw Error('Input url or template required')
}

export function upload(conf: Config): void {
    if (!conf.output.s3) {
        return
    }
    if (!conf.output.file) {
        throw Error("output missing")
    }

    let s3: S3
    if (conf.output.s3.access_key && conf.output.s3.secret) {
        s3 = new S3({accessKeyId: conf.output.s3.access_key, secretAccessKey: conf.output.s3.secret})
    } else {
        s3 = new S3()
    }

    const params = {
        Bucket: conf.output.s3.bucket,
        Key: conf.output.s3.key,
        Body: readFileSync(conf.output.file)
    }

    s3.upload(params, undefined,function(err, data) {
        if (err) {
            console.log(err)
        } else {
            console.log(`file uploaded to ${data.Location}`)
        }
    })
}

function buildRecorderToken(room: string, key: string, secret: string): string {
    const at = new AccessToken(key, secret, {
        identity: 'recorder-'+(Math.random()+1).toString(36).substring(2),
    })
    at.addGrant({
        roomJoin: true,
        room: room,
        canPublish: false,
        canSubscribe: true,
        hidden: true,
    })
    return at.toJwt()
}

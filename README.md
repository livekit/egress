# LiveKit Recording

All your live recording needs in one place.  
Record any website using our recorder, or deploy our service to manage it for you.

## How it works

The recorder launches Chrome and navigates to the supplied url, grabs audio from pulse and video from a virtual frame buffer, and feeds them into GStreamer.
You can write the output as mp4 to a file or upload it to s3, or forward the output to one or multiple rtmp streams.

## Config

Both the standalone recorder and recorder service take a yaml config file. If you will be using templates with your recording requests, `ws_url` is required, and to record by room name instead of token, `api_key` and
`api_secret` are also required. When running in service mode, `redis` config is required (with the same db as your LiveKit server), as this is how it receives requests.

All config options:

```yaml
api_key: livekit server api key (required if using templates without supplying tokens)
api_secret: livekit server api secret (required if using templates without supplying tokens)
ws_url: livekit server ws url (required if using templates)
health_port: http port to serve status (optional)
log_level: valid levels are debug, info, warn, error, fatal, or panic. Defaults to debug
template_address: template address override (for hosting your own templates). Defaults to https://recorder.livekit.io
redis: (service mode only)
    address: redis address, including port
    username: redis username (optional)
    password: redis password (optional)
    db: redis db (optional)
file_output:
    local: true/false (will default to true if you don't supply s3 config)
    s3: (required if using s3 output)
        access_key: s3 access key
        secret: s3 access secret
        region: s3 region
        bucket: s3 bucket
        endpoint: s3 server endpoint (optional - for use with minio)
    azblob: (required if using azure blob output)
        account_name: azure blob account
        account_key: azure blob access key
        container_name: azure blob container name
defaults:
    preset: defaults to "NONE", see options below
    width: defaults to 1920
    height: defaults to 1080
    depth: defaults to 24
    framerate: defaults to 30
    audio_bitrate: defaults to 128 (kbps)
    audio_frequency: defaults to 44100 (Hz)
    video_bitrate: defaults to 4500 (kbps)
```

### Presets

| Preset       | width | height | framerate | video_bitrate |
|---           |---    |---     |---        |---            |
| "HD_30"      | 1280  | 720    | 30        | 3000          |
| "HD_60"      | 1280  | 720    | 60        | 4500          |
| "FULL_HD_30" | 1920  | 1080   | 30        | 4500          |
| "FULL_HD_60" | 1920  | 1080   | 60        | 6000          |

If you don't supply any options with your config defaults or the request, it defaults to FULL_HD_30.

## Request

See StartRecordingRequest [here](https://github.com/livekit/protocol/blob/main/livekit_recording.proto#L16).
When using standalone mode, the request can be input as a json file. In service mode, these requests will be made through
the LiveKit server's recording api.

### Input

You can input either a `url` to record from, or choose a `template` and a `layout`, and supply either a `room_name` or `token`.

We currently have 4 templates available - grid or speaker, each available in light or dark.
Your config will need your server api key and secret, along with the websocket url.  
Check out our [web README](https://github.com/livekit/livekit-recorder/tree/main/web) to learn more or create your own.

### Output

You can either output to a `filepath` or write to one or more `rtmp` `urls`. Depending on your config, the `filepath` 
output will either write to a local file or upload to s3 or azure blob.

### Options

You can also override any defaults set in your `config.yaml`.

All request options:
```json
{
    "url": "<your-recording-domain.com>",
    "template": {
        "layout": "<grid|speaker>-<light|dark>",
        "room_name": "<room-to-record>",
        "token": "<token>"
    },
    "filepath": "path/recording.mp4",
    "rtmp": {
        "urls": ["<rtmp://stream-url.com>"]
    },
    "options": {
        "preset": "FULL_HD_60",
        "width": 1920,
        "height": 1080,
        "depth": 24,
        "framerate": 60,
        "audio_bitrate": 128,
        "audio_frequency": 44100,
        "video_bitrate": 6000
    }
}
```

# Service Mode

Simply deploy the service, and submit requests through your LiveKit server.

### How it works

The service listens to a redis subscription and waits for the LiveKit server to make a reservation. Once the reservation
is made to ensure availability, the service waits for a StartRecording request from the server before launching the recorder.
The recorder will be stopped by either a `END_RECORDING` signal from the server, or automatically when the last participant leaves if using our templates.

A single service instance can record one room at a time.

### Deployment

See guides and deployment docs at https://docs.livekit.io/guides/recording

### Running locally

If you want to try running against a local livekit server, you'll need to make a couple changes:
* open `/usr/local/etc/redis.conf` and comment out the line that says `bind 127.0.0.1`
* change `protected-mode yes` to `protected-mode no` in the same file
* add `--network host` to your `docker run` command
* update your redis address from `localhost` to your host ip as docker sees it:
    * on linux, this should be `172.17.0.1`
    * on mac or windows, run `docker run -it --rm alpine nslookup host.docker.internal` and you should see something like
      `Name:	host.docker.internal
      Address: 192.168.65.2`

These changes allow the service to connect to your local redis instance from inside the docker container.
Finally, to build and run:
```bash
docker build -t livekit-recorder .
docker run --network host \
    -e SERVICE_MODE=1 \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    livekit-recorder
```

You can then use our [cli](https://github.com/livekit/livekit-cli) to submit recording requests to your server.

# Examples

Start by filling in a config.yaml:

```
api_key: <livekit-server-api-key>
api_secret: <livekit-server-api-secret>
ws_url: <livekit-server-ws-url>
file_output:
    local: true
```

## Basic recording

basic.json:
```json
{
  "template": {
    "layout": "speaker-dark",
    "room_name": "my-room"
  },
  "filepath": "/out/demo.mp4"
}
```
```bash
mkdir -p ~/livekit/output

docker run --rm \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat basic.json)" \
    -v ~/livekit/recordings:/out \
    livekit/livekit-recorder
```

## Record at 720p, with 2048kbps video bitrate, and upload result to s3

update your config.yaml and replace 
```yaml
file_output:
    local: true
```
with
```yaml
file_output:
    s3:
        access_key: <s3-access-key>
        secret: <s3-secret>
        region: <s3-region>
        bucket: <s3-bucket>
```

s3.json:
```json
{
    "url": "https://www.youtube.com/watch?v=BHACKCNDMW8",
    "filepath": "path/filename.mp4",
    "options": {
        "width": "1280",
        "height": "720",
        "video_bitrate": 2048
    }
}
```
```bash
docker run --name my-recorder --rm \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat s3.json)" \
    livekit/livekit-recorder
```
```bash
docker stop my-recorder
```

## Stream to Twitch at 1080p, 60fps

twitch.json:
```json
{
    "template": {
        "layout": "speaker-dark",
        "token": "<recording-token>"
    },
    "rtmp": {
        "urls": ["rtmp://live.twitch.tv/app/<stream-key>"]
    },
    "options": {
        "preset": "FULL_HD_60"
    }
}
```
```bash
docker run --rm \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat twitch.json)" \
    livekit/livekit-recorder
```

## Ending a recording

Once started, there are a number of ways to end the recording:
* `docker stop <container>`
* if using our templates, the recorder will stop automatically when the last participant leaves
* if using your own webpage, logging `END_RECORDING` to the console

With any of these methods, the recorder will stop gstreamer and finish uploading before shutting down.

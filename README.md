# LiveKit Recording

All your live recording needs in one place.  
Record any website using our recorder, or deploy our service to manage it for you.

## How it works

The recorder launches Chrome and navigates to the supplied url, grabs audio from pulse and video from a virtual 
frame buffer, and feeds them into GStreamer. You can write the output as mp4 to a file or upload it to s3, or forward 
the output to one or multiple rtmp streams.

## Quick start

Start by creating a `config.yaml`:
```
file_output:
    local: true
```

Next, create a `request.json`:
```json
{
  "url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
  "filepath": "/out/demo.mp4"
}
```

Start the recording:
```shell
mkdir -p ~/livekit/recordings

docker run --rm --name quick-demo \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat basic.json)" \
    -v ~/livekit/recordings:/out \
    livekit/livekit-recorder
```

Then, to stop the recording:
```shell
docker stop quick-demo
```

You should find a `~/livekit/recordings/demo.mp4`.

## Recording LiveKit rooms

If you already have a LiveKit server deployed with SSL, recording a room is simple. If not, skip to the next example.

Update your `config.yaml` with the same key and secret as your deployed server, along with your server websocket address:
```yaml
api_key: <livekit-server-api-key>
api_secret: <livekit-server-api-secret>
ws_url: <livekit-server-ws-url>
```

Create a `room.json`:
```json
{
  "template": {
      "layout": "speaker-dark",
      "room_name": "my-room"
  },
  "filepath": "out/room.mp4"
}
```

Join the room, either using https://example.livekit.io, or using your own client

Start the recording:
```shell
docker run --rm --name room-demo \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat room.json)" \
    -v ~/livekit/recordings:/out \
    livekit/livekit-recorder
```

To stop recording, either leave the room, or `docker stop room-demo`. You'll find the file at `~/livekit/recordings/room.mp4`

## Recording local rooms

First, find your IP as seen by docker:
* on linux, this should be `172.17.0.1`
* on mac or windows, run `docker run -it --rm alpine nslookup host.docker.internal` and you should see something like
  `Name:	host.docker.internal Address: 192.168.65.2`

Update your `config.yaml` with `ws_url` using this IP, along with adding your `api_key` and `api_secret`, and `insecure`:
```yaml
api_key: <livekit-server-api-key>
api_secret: <livekit-server-api-secret>
ws_url: ws://192.168.65.2:7880
insecure: true
```

Create a `room.json`:
```json
{
  "template": {
      "layout": "speaker-dark",
      "room_name": "my-room"
  },
  "filepath": "out/room.mp4"
}
```

Generate a token for yourself using livekit-server:
```shell
./bin/livekit-server --keys "{api_key}: {api_secret}" create-join-token --room my-room --identity me
```

Start your server using `node-ip` from above:
```shell
./bin/livekit-server --keys "{api_key}: {api_secret}" --node-ip 192.168.65.2 --dev
```

Open https://example.livekit.io, enter the `token` you generated, and connect (keep `ws://localhost:7880` as the LiveKit URL).

Start the recording:
```shell
docker run --rm --network host --name local-demo \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat room.json)" \
    -v ~/livekit/recordings:/out \
    livekit/livekit-recorder
```

To stop recording, either leave the room, or `docker stop local-demo`. You'll find the file at `~/livekit/recordings/room.mp4`

## Uploading to S3

Update `file_output` in your `config.yaml`:
```yaml
file_output:
    s3:
        access_key: <s3-access-key>
        secret: <s3-secret>
        region: <s3-region>
        bucket: <s3-bucket>
```

Create a `s3.json`:
```json
{
    "template": {
        "layout": "speaker-dark",
        "room_name": "my-room"
    },
    "filepath": "path/filename.mp4",
    "options": {
        "preset": "HD_60"
    }
}
```
This time, we've added the `HD_60` preset. This will record at 1280x720, 60fps (the default is 1920x1080, 30fps).
You can find the other presets and options [below](#presets).


Join the room, and start the recording:
```shell
docker run --rm --name s3-demo \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat s3.json)" \
    livekit/livekit-recorder
```

End the recording:
```shell
docker stop s3-demo
```

After the recording is stopped, the file will be uploaded to your S3 bucket.

## Rtmp Output

Create a `rtmp.json` (if you have a Twitch account you can fill in your stream key, otherwise replace the rtmp url with your provider):
```json
{
    "template": {
        "layout": "speaker-dark",
        "room_name": "my-room"
    },
    "rtmp": {
        "urls": ["rtmp://live.twitch.tv/app/<stream-key>"]
    },
    "options": {
        "width": "1280",
        "height": "720",
        "video_bitrate": 2048
    }
}
```
This time, we've set custom options to output 720p with a lower bitrate (2048 kbps - the default is 3000 kpbs). If you have sufficient bandwidth, try using `preset: FULL_HD_60` instead for a high quality stream.

Join the room, then start the stream:
```shell
docker run --rm --name rtmp-demo \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    -e RECORDING_REQUEST="$(cat rtmp.json)" \
    livekit/livekit-recorder
```
Note: with Twitch, it will take about 25 seconds for them to process before they begin showing the stream. 
May be different with other providers.

Stop the stream:
```shell
docker stop rtmp-demo
```

## Config

Below is a full config, with all optional parameters.

* `api_key`, `api_secret`, and `ws_url` are required for recording LiveKit rooms
* `log_level: error` is recommended for production setups. GStreamer logs can be noisy
* `redis` is required for [service mode](#service-mode)
* Only one of `file_output.s3` or `file_output.azblob` should be set.
* If `preset` is set, it will override any other options. Any and all `defaults` can be overridden by a request

All config parameters:

```yaml
api_key: livekit server api key
api_secret: livekit server api secret
ws_url: livekit server ws url
health_port: http port to serve status (optional)
log_level: valid levels are debug, info, warn, error, fatal, or panic. Defaults to debug
template_address: template url base, can be used to host your own templates. Defaults to https://recorder.livekit.io/#
insecure: should only be used for local testing
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

## Requests

See [StartRecordingRequest](https://github.com/livekit/protocol/blob/main/livekit_recording.proto#L24).
When using standalone mode, the request can be input as a json file. In service mode, these requests will be made through
the LiveKit server's recording api.

* Input: either `url` or `template`
  * `url`: any url that chrome can connect to for recording
  * `template`: `layout` and `room_name` required. `base_url` is optional, used for custom templates
  * We currently have 4 templates available; `speaker-light`, `speaker-dark`, `grid-light`, and `grid-dark`. Check out our [web README](https://github.com/livekit/livekit-recorder/tree/main/web) to learn more or create your own.
* Output: either `filepath` or `rtmp`. File output and stream output cannot be mixed
  * `filepath`: whether writing to a local file, s3, or azure blob storage, this path will be used. Must end with `.mp4`
  * `rtmp`: a list of rtmp urls to stream to
* `options`: will override anything in `config.defaults`. Using `preset` will override all other options

All request options:
```json
{
    "url": "website-to-record.com",
    "template": {
        "layout": "<grid|speaker>-<light|dark>",
        "room_name": "my-room",
        "base_url": "my-template-host.com"
    },
    "filepath": "path/output.mp4",
    "rtmp": {
        "urls": ["rtmp://stream-url-1.com", "rtmp://stream-url-2.com"]
    },
    "options": {
        "preset": "FULL_HD_30",
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

## Service Mode

Simply deploy the service, and submit requests through your LiveKit server.

### How it works

The service listens to a redis subscription and waits for the LiveKit server to make a reservation. Once the reservation
is made to ensure availability, the server sends a StartRecording request to the reserved instance.


A single service instance can record one room at a time.

### Deployment

See guides and deployment docs at https://docs.livekit.io/guides/deploy/recorder.

### Development

This is **not** recommended for a production setup - the following redis changes make your redis server completely 
accessible to the internet, and using `--network host` with your docker run command is also not recommended in production.

To run against a local livekit server, you'll need to do the following:
* open `/usr/local/etc/redis.conf` and comment out the line that says `bind 127.0.0.1`
* change `protected-mode yes` to `protected-mode no` in the same file
* add `--network host` to your `docker run` command
* find your IP as seen by docker
  * `ws_url` needs to be set using the IP as Docker sees it
  * on linux, this should be `172.17.0.1`
  * on mac or windows, run `docker run -it --rm alpine nslookup host.docker.internal` and you should see something like
    `Name:	host.docker.internal
    Address: 192.168.65.2`
* update your `redis` and `ws_url` to use this IP instead of `localhost`, and set `insecure` to true in your `config.yaml`
* your livekit-server must be run using `--node-ip` set to the above IP

These changes allow the service to connect to your local redis instance from inside the docker container.
Finally, to build and run:
```shell
docker build -t livekit-recorder .
docker run --network host \
    -e SERVICE_MODE=1 \
    -e LIVEKIT_RECORDER_CONFIG="$(cat config.yaml)" \
    livekit-recorder
```
You can then use our [cli](https://github.com/livekit/livekit-cli) to submit recording requests to your server.

## FAQ

### It doesn't work at all

* Make sure you're using the latest cli, server sdks, livekit-server and livekit-recorder.
  This is still in beta, and we still occasionally release breaking changes.

### I get a `"no recorders available"` error when sending a StartRecording request

* Your livekit server cannot reach your recorder through redis. Make sure they are both able to reach the same redis db.
  
### Docker fails to run this on my computer

* The recorder currently only works on `linux` OS with `amd64` architecture. Unfortunately, this means it does not run on the new M1 macbooks.

### I'm getting a broken mp4 file

* GStreamer needs to be properly shut down - if the process is killed, the file will be unusable.   
  Make sure you're stopping the recording with either a `docker stop` or an `EndRecordingRequest`.

### Still getting a broken file. How can I debug?

* Start with a `url` request recording from, for example, YouTube.
* If the `url` request works, try a `template` request next.
* Join the room yourself by connecting on https://example.livekit.io - recording an empty room will not work.
* If the `template` request doesn't work, the recorder probably isn't connecting to the room.
  Using `log_level: debug`, the recorder should print a message that says `launching chrome` along with a url. 
  Try navigating to this url in your browser to see if it connects to the room.

### My rtmp output is missing video. How can I debug?
* Start with a `url` request recording from, for example, YouTube.
* If the `url` request works, try a `template` request next.
* Join the room yourself by connecting on https://example.livekit.io - recording an empty room will not work.
* Try streaming to a different rtmp endpoint. For testing, we use Twitch and [RTSP Simple Server](https://github.com/aler9/rtsp-simple-server).

### I'm seeing GStreamer errors. Is this normal?

* It's recommended to use `log_level`: `error` in a production setup. This will remove most of the GStreamer logs.
* `Failed to load plugin '/usr/lib/x86_64-linux-gnu/gstreamer-1.0/libgstvaapi.so': libva-wayland.so.2: cannot open shared object file: No such file or directory`
  * Expected - safe to ignore. This is caused by the wayland plugin being present in the Dockerfile, but its dependencies are not installed. We do not use this plugin - it will be removed from the Dockerfile in the future.
* `WARN flvmux ... Got backwards dts! (0:01:10.379000000 < 0:01:10.457000000)`
  * Occurs when streaming to rtmp - safe to ignore. These warnings occur due to live sources being used for the flvmux. The dts difference should be small (under 150ms).

### Can I run this without docker?

* It's possible, but not recommended. To do so, you would need gstreamer and all the plugins installed, along with xvfb,
  and have a pulseaudio server running.
* Since it records from system audio, each recorder needs to be run on its own machine or VM. 

### How do I autoscale?
* Currently, it's not possible to keep X recorders available at all times, although we plan to add that in the future.
* In the meantime, you can autoscale based on CPU - each instance consistently uses 2.5-3 CPU while recording.
* Autoscaling by listening to webhooks and launching a new instance is possible, but not recommended.
  Note that each instance requires its own VM, and they are CPU intensive - with 16 cores on the host you should only 
  expect to be able to run 4 or 5 instances.

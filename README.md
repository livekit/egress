# LiveKit Egress

WebRTC is fantastic for last-mile media delivery, but interoperability with other services can be challenging.
An application may want to do things like store a session for future playback, relay a stream to a CDN, or process a track through a transcription service – workflows where media travels through a different system or protocol.
LiveKit Egress is the solution to these interoperability challenges. It provides a consistent set of APIs that gives you
universal export of your LiveKit sessions and tracks.

## Capabilities

1. **Room composite** for exporting an entire room.
2. **Web egress** for recordings that aren't attached to a single LiveKit room.
3. **Track composite** for exporting synchronized tracks of a single participant.
4. **Track egress** for exporting individual tracks.

Depending on your request type, the egress service will either launch Chrome using a web template
(room composite requests) or a supplied url (web requests), or it will use the Go SDK directly (track and track composite requests).
Irrespective of method used, when moving between protocols, containers or encodings, LiveKit's egress service will automatically transcode streams for you using GStreamer.

## Supported Output

| Egress Type     | MP4 File | OGG File | WebM File | HLS (TS Segments) | RTMP(s) Stream | WebSocket Stream |
|-----------------|----------|----------|-----------|-------------------|----------------|------------------|
| Room Composite  | ✅        | ✅        |           | ✅                 | ✅              |                  |
| Web             | ✅        | ✅        |           | ✅                 | ✅              |                  |
| Track Composite | ✅        | ✅        |           | ✅                 | ✅              |                  |
| Track           | ✅        | ✅        | ✅         |                   |                | ✅                |

Files can be uploaded to any S3 compatible storage, Azure, or GCP.

## Documentation

Full docs available [here](https://docs.livekit.io/guides/egress/)

### Config

The Egress service takes a yaml config file:

```yaml
# required fields
api_key: livekit server api key. LIVEKIT_API_KEY env can be used instead
api_secret: livekit server api secret. LIVEKIT_API_SECRET env can be used instead
ws_url: livekit server websocket url. LIVEKIT_WS_URL can be used instead
redis:
  address: must be the same redis address used by your livekit server
  username: redis username
  password: redis password
  db: redis db

# optional fields
health_port: if used, will open an http port for health checks
prometheus_port: port used to collect prometheus metrics. Used for autoscaling
log_level: debug, info, warn, or error (default info)
template_base: can be used to host custom templates (default https://egress-composite.livekit.io)
insecure: can be used to connect to an insecure websocket (default false)
local_directory: base path where to store media files before they get uploaded to blob storage. This does not affect the storage path if no upload location is given.

# file upload config - only one of the following. Can be overridden
s3:
  access_key: AWS_ACCESS_KEY_ID env can be used instead
  secret: AWS_SECRET_ACCESS_KEY env can be used instead
  region: AWS_DEFAULT_REGION env can be used instead
  endpoint: optional custom endpoint
  bucket: bucket to upload files to
azure:
  account_name: AZURE_STORAGE_ACCOUNT env can be used instead
  account_key: AZURE_STORAGE_KEY env can be used instead
  container_name: container to upload files to
gcp:
  credentials_json: GOOGLE_APPLICATION_CREDENTIALS env can be used instead
  bucket: bucket to upload files to
alioss:
  access_key: Ali OSS AccessKeyId
  secret: Ali OSS AccessKeySecret
  region: Ali OSS region
  endpoint: optional custom endpoint (example https://oss-cn-hangzhou.aliyuncs.com)
  bucket: bucket to upload files to
# cpu costs for various egress types with their default values
cpu_cost:
  room_composite_cpu_cost: 3.0
  web_cpu_cost: 3.0
  track_composite_cpu_cost: 2.0
  track_cpu_cost: 1.0
```

The config file can be added to a mounted volume with its location passed in the EGRESS_CONFIG_FILE env var, or its body can be passed in the EGRESS_CONFIG_BODY env var.

### Filenames

The below templates can also be used in filename/filepath parameters:

| Egress Type     | {room_id} | {room_name} | {time} | {publisher_identity} | {track_id} | {track_type} | {track_source} |
|-----------------|-----------|-------------|--------|----------------------|------------|--------------|----------------|
| Room Composite  | ✅         | ✅           | ✅      |                      |            |              |                |
| Web             |           |             | ✅      |                      |            |              |                |
| Track Composite | ✅         | ✅           | ✅      | ✅                    |            |              |                |
| Track           | ✅         | ✅           | ✅      | ✅                    | ✅          | ✅            | ✅              |

* If no filename is provided with a request, one will be generated in the form of `"{room_name}-{time}"`.
* If your filename ends with a `/`, a file will be generated in that directory.

Examples:

| Request filename                         | Resulting filename                                |
|------------------------------------------|---------------------------------------------------|
| ""                                       | testroom-2022-10-04T011306.mp4                    |
| "livekit-recordings/"                    | livekit-recordings/testroom-2022-10-04T011306.mp4 |
| "{room_name}/{time}"                     | testroom/2022-10-04T011306.mp4                    |
| "{room_id}-{publisher_identity}.mp4"     | 10719607-f7b0-4d82-afe1-06b77e91fe12-david.mp4    |
| "{track_type}-{track_source}-{track_id}" | audio-microphone-TR_SKasdXCVgHsei.ogg             |

### Running locally

These changes are **not** recommended for a production setup.

To run against a local livekit server, you'll need to do the following:

- open `/usr/local/etc/redis.conf` and comment out the line that says `bind 127.0.0.1`
- change `protected-mode yes` to `protected-mode no` in the same file
- find your IP as seen by docker
  - `ws_url` needs to be set using the IP as Docker sees it
  - on linux, this should be `172.17.0.1`
  - on mac or windows, run `docker run -it --rm alpine nslookup host.docker.internal` and you should see something like
    `Name: host.docker.internal Address: 192.168.65.2`

These changes allow the service to connect to your local redis instance from inside the docker container.

Create a directory to mount. In this example, we will use `~/egress-test`.

Create a config.yaml in the above directory.

- `redis` and `ws_url` should use the above IP instead of `localhost`
- `insecure` should be set to true

```yaml
log_level: debug
api_key: your-api-key
api_secret: your-api-secret
ws_url: ws://192.168.65.2:7880
insecure: true
redis:
  address: 192.168.65.2:6379
```

Then to run the service:

```shell
docker run --rm \
    -e EGRESS_CONFIG_FILE=/out/config.yaml \
    -v ~/egress-test:/out \
    livekit/egress
```

You can then use our [cli](https://github.com/livekit/livekit-cli) to submit egress requests to your server.

## FAQ

### I get a `"no response from egress service"` error when sending a request

- Your livekit server cannot connect to an egress instance through redis. Make sure they are both able to reach the same redis db.
- Each instance currently only accepts one RoomCompositeRequest at a time - if it's already in use, you'll need to deploy more instances or set up autoscaling.

### I get a different error when sending a request

- Make sure your egress, livekit, server sdk, and livekit-cli are all up to date.

### I'm getting a broken (0 byte) mp4 file

- This is caused by the process being killed - GStreamer needs to be properly shut down to close the file.
- Make sure your instance has enough CPU and memory, and is being stopped correctly.

### I'm seeing GStreamer warnings/errors. Is this normal?

- `GStreamer-CRITICAL **: 20:22:13.875: gst_mini_object_unref: assertion 'GST_MINI_OBJECT_REFCOUNT_VALUE (mini_object) > 0' failed`
  - Occurs during audio-only egress - this is a gst bug, and is safe to ignore.
- `WARN flvmux ... Got backwards dts! (0:01:10.379000000 < 0:01:10.457000000)`
  - Occurs when streaming to rtmp - safe to ignore. These warnings occur due to live sources being used for the flvmux.
    The dts difference should be small (under 150ms).

### Can I run this without docker?

- It's possible, but not recommended. To do so, you would need gstreamer and all the plugins installed, along with xvfb,
  and have a pulseaudio server running.

## Testing and Development

To run the test against your own LiveKit rooms, a deployed LiveKit server with a secure websocket url is required.
First, create `egress/test/config.yaml`:

```yaml
log_level: debug
gst_debug: 1
api_key: your-api-key
api_secret: your-api-secret
ws_url: wss://your-livekit-url.com
redis:
  address: 192.168.65.2:6379
local_directory: /out/output
room_name: your-room
room_only: false
web_only: false
track_composite_only: false
track_only: false
file_only: false
stream_only: false
segments_only: false
muting: false
```

Join a room using https://example.livekit.io or your own client, then run `mage integration test/config.yaml`.
This will test recording different file types, output settings, and streams against your room.

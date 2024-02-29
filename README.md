<!--BEGIN_BANNER_IMAGE-->

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="/.github/banner_dark.png">
  <source media="(prefers-color-scheme: light)" srcset="/.github/banner_light.png">
  <img style="width:100%;" alt="The LiveKit icon, the name of the repository and some sample code in the background." src="https://raw.githubusercontent.com/livekit/egress/main/.github/banner_light.png">
</picture>

<!--END_BANNER_IMAGE-->

# LiveKit Egress

<!--BEGIN_DESCRIPTION-->
WebRTC is fantastic for last-mile media delivery, but interoperability with other services can be challenging.
An application may want to do things like store a session for future playback, relay a stream to a CDN, or process a track through a transcription service – workflows where media travels through a different system or protocol.
LiveKit Egress is the solution to these interoperability challenges. It provides a consistent set of APIs that gives you
universal export of your LiveKit sessions and tracks.
<!--END_DESCRIPTION-->

## Capabilities

1. **Room composite** for exporting an entire room.
2. **Web egress** for recordings that aren't attached to a single LiveKit room.
3. **Track composite** for exporting synchronized tracks of a single participant.
4. **Track egress** for exporting individual tracks.

Depending on your request type, the egress service will either launch Chrome using a web template
(room composite requests) or a supplied url (web requests), or it will use the Go SDK directly (track and track composite requests).
Irrespective of method used, when moving between protocols, containers or encodings, LiveKit's egress service will automatically transcode streams for you using GStreamer.

## Supported Output

| Egress Type     | MP4 File | OGG File | WebM File | HLS (TS Segments) | RTMP(s) Stream | WebSocket Stream | Thumbnails (JPEGs) |
|-----------------|----------|----------|-----------|-------------------|----------------|------------------|--------------------|
| Room Composite  | ✅        | ✅        |           | ✅                 | ✅              |                  | ✅                  |
| Web             | ✅        | ✅        |           | ✅                 | ✅              |                  | ✅                  |
| Track Composite | ✅        | ✅        |           | ✅                 | ✅              |                  | ✅                  |
| Track           | ✅        | ✅        | ✅         |                   |                | ✅                |                    |

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
health_port: port used for http health checks (default 0)
template_port: port used to host default templates (default 7980)
prometheus_port: port used to collect prometheus metrics (default 0)
debug_handler_port: port used to host http debug handlers (default 0)
logging:
  level: debug, info, warn, or error (default info)
  json: true
template_base: can be used to host custom templates (default http://localhost:<template_port>/)
backup_storage: files will be moved here when uploads fail. location must have write access granted for all users
enable_chrome_sandbox: if true, egress will run Chrome with sandboxing enabled. This requires a specific Docker setup, see below.
cpu_cost: # optionally override cpu cost estimation, used when accepting or denying requests
  room_composite_cpu_cost: 3.0
  web_cpu_cost: 3.0
  track_composite_cpu_cost: 2.0
  track_cpu_cost: 1.0
session_limits: # optional egress duration limits - once hit, egress will end with status EGRESS_LIMIT_REACHED
  file_output_max_duration: 1h
  stream_output_max_duration: 90m
  segment_output_max_duration: 3h

# file upload config - only one of the following. Can be overridden per request
s3:
  access_key: AWS_ACCESS_KEY_ID env or IAM role can be used instead
  secret: AWS_SECRET_ACCESS_KEY env or IAM role can be used instead
  region: AWS_DEFAULT_REGION env or IAM role can be used instead
  endpoint: (optional) custom endpoint
  bucket: bucket to upload files to
  # the following s3 options can only be set in config, *not* per request, they will be added to any per-request options
  proxy: (optional, no default) proxy url
  max_retries: (optional, default=3) number or retries to attempt
  max_retry_delay: (optional, default=5s) max delay between retries (e.g. 5s, 100ms, 1m...)
  min_retry_delay: (optional, default=500ms) min delay between retries (e.g. 100ms, 1s...)
  aws_log_level: (optional, default=LogOff) log level for aws sdk (LogDebugWithRequestRetries, LogDebug, ...) 
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
  endpoint: (optional) custom endpoint
  bucket: bucket to upload files to

# dev/debugging fields
insecure: can be used to connect to an insecure websocket (default false)
debug:
  enable_profiling: create and upload pipeline dot file and pprof file on pipeline failure
  s3: upload config for dotfiles (see above)
  azure: upload config for dotfiles (see above)
  gcp: upload config for dotfiles (see above)
  alioss: upload config for dotfiles (see above)
```

The config file can be added to a mounted volume with its location passed in the EGRESS_CONFIG_FILE env var, or its body can be passed in the EGRESS_CONFIG_BODY env var.

### Filenames

The below templates can also be used in filename/filepath parameters:

| Egress Type     | {room_id} | {room_name} | {time} | {utc} | {publisher_identity} | {track_id} | {track_type} | {track_source} |
|-----------------|-----------|-------------|--------|-------|----------------------|------------|--------------|----------------|
| Room Composite  | ✅         | ✅           | ✅      | ✅     |                      |            |              |                |
| Web             |           |             | ✅      | ✅     |                      |            |              |                |
| Track Composite | ✅         | ✅           | ✅      | ✅     | ✅                    |            |              |                |
| Track           | ✅         | ✅           | ✅      | ✅     | ✅                    | ✅          | ✅            | ✅              |

* If no filename is provided with a request, one will be generated in the form of `"{room_name}-{time}"`.
* If your filename ends with a `/`, a file will be generated in that directory.
* For 1/2/2006, 3:04:05.789 PM, {time} format would display "2006-01-02T150405", and {utc} format "20060102150405789"

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

### Chrome sandboxing

By default, Room Composite and Web egresses run with Chrome sandboxing disabled. This is because the default docker security settings prevent Chrome from
switching to a different kernel namespace, which is needed by Chrome to setup its sandbox.

Chrome sandboxing within Egress can be reenabled by setting the the `enable_chrome_sandbox` option to `true` in the egress configuration, and launching docker using the [provided
seccomp security profile](https://github.com/livekit/egress/blob/main/chrome-sandboxing-seccomp-profile.json):

```shell
docker run --rm \
    -e EGRESS_CONFIG_FILE=/out/config.yaml \
    -v ~/egress-test:/out \
    --security-opt seccomp=chrome-sandboxing-seccomp-profile.json \
    livekit/egress
```

This profile is based on the [default docker seccomp security profile](https://github.com/moby/moby/blob/master/profiles/seccomp/default.json) and allows
the 2 extra system calls (`clone` and `unshare`) that Chrome needs to setup the sandbox.

Note that kubernetes disables seccomp entirely by default, which means that running with Chrome sandboxing enabled is possible on a kubernetes cluster with
the default security settings.

## FAQ

### Can I store the files locally instead of uploading to cloud storage?
- Yes, you can mount a volume with your `docker run` command (e.g. `-v ~/livekit-egress:/out/`), and use the mounted
directory in your filenames (e.g. `/out/my-recording.mp4`). Since egress is not run as the root user, write permissions
will need to be enabled for all users.

### I get a `"no response from egress service"` error when sending a request

- Your livekit server cannot connect to an egress instance through redis. Make sure they are both able to reach the same redis db.
- If all of your egress instances are full, you'll need to deploy more instances or set up autoscaling.

### I get a different error when sending a request

- Make sure your egress, livekit, server sdk, and livekit-cli are all up to date.

### Can I run this without docker?

- It's possible, but not recommended. To do so, you would need to install gstreamer along with its plugins, chrome, xvfb,
  and have a pulseaudio server running.

## Testing and Development

To run the test against your own LiveKit rooms, a deployed LiveKit server with a secure websocket url is required.
First, create `egress/test/config.yaml`:

```yaml
log_level: debug
api_key: your-api-key
api_secret: your-api-secret
ws_url: wss://your-livekit-url.com
redis:
  address: 192.168.65.2:6379
room_only: false
web_only: false
track_composite_only: false
track_only: false
file_only: false
stream_only: false
segments_only: false
muting: false
dot_files: false
short: false
```

Join a room using https://example.livekit.io or your own client, then run `mage integration test/config.yaml`.
This will test recording different file types, output settings, and streams against your room.

<!--BEGIN_REPO_NAV-->
<br/><table>
<thead><tr><th colspan="2">LiveKit Ecosystem</th></tr></thead>
<tbody>
<tr><td>Real-time SDKs</td><td><a href="https://github.com/livekit/components-js">React Components</a> · <a href="https://github.com/livekit/client-sdk-js">JavaScript</a> · <a href="https://github.com/livekit/client-sdk-swift">iOS/macOS</a> · <a href="https://github.com/livekit/client-sdk-android">Android</a> · <a href="https://github.com/livekit/client-sdk-flutter">Flutter</a> · <a href="https://github.com/livekit/client-sdk-react-native">React Native</a> · <a href="https://github.com/livekit/client-sdk-rust">Rust</a> · <a href="https://github.com/livekit/client-sdk-python">Python</a> · <a href="https://github.com/livekit/client-sdk-unity-web">Unity (web)</a> · <a href="https://github.com/livekit/client-sdk-unity">Unity (beta)</a></td></tr><tr></tr>
<tr><td>Server APIs</td><td><a href="https://github.com/livekit/server-sdk-js">Node.js</a> · <a href="https://github.com/livekit/server-sdk-go">Golang</a> · <a href="https://github.com/livekit/server-sdk-ruby">Ruby</a> · <a href="https://github.com/livekit/server-sdk-kotlin">Java/Kotlin</a> · <a href="https://github.com/livekit/client-sdk-python">Python</a> · <a href="https://github.com/livekit/client-sdk-rust">Rust</a> · <a href="https://github.com/agence104/livekit-server-sdk-php">PHP (community)</a></td></tr><tr></tr>
<tr><td>Agents Frameworks</td><td><a href="https://github.com/livekit/agents">Python</a> · <a href="https://github.com/livekit/agent-playground">Playground</a></td></tr><tr></tr>
<tr><td>Services</td><td><a href="https://github.com/livekit/livekit">Livekit server</a> · <b>Egress</b> · <a href="https://github.com/livekit/ingress">Ingress</a> · <a href="https://github.com/livekit/sip">SIP</a></td></tr><tr></tr>
<tr><td>Resources</td><td><a href="https://docs.livekit.io">Docs</a> · <a href="https://github.com/livekit-examples">Example apps</a> · <a href="https://livekit.io/cloud">Cloud</a> · <a href="https://docs.livekit.io/oss/deployment">Self-hosting</a> · <a href="https://github.com/livekit/livekit-cli">CLI</a></td></tr>
</tbody>
</table>
<!--END_REPO_NAV-->

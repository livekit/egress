# LiveKit Egress

Record or stream your LiveKit rooms

## How it works

Depending on your request type, the egress service will either launch a web template in Chrome and connect to the room 
(room composite requests), or it will use the sdk directly (track and track composite requests). It uses GStreamer to
encode, and can output to a file or to one or more streams.

## Capabilities

### Egress Types

* Room Composite - export an entire room's video, audio, or both using a web layout
* Track Composite - transcode up to one audio track and one video track
* Track - export tracks directly, without transcoding

| Egress Type     | File Output | Stream Output | Websocket Output |
|-----------------|-------------|---------------|------------------|
| Room Composite  | ✅           | ✅             |                  |
| Track Composite | Coming soon | Coming soon   |                  |
| Track           | Coming soon |               | Coming soon      |

### Supported Codecs and Output Types

* Room Composite and Track Composite egress can be output to an mp4 or ogg file, or an rtmp or rtmps stream.

| Output type  | H264 Baseline | H264 Main   | H264 High | AAC         | OPUS        | 
|--------------|---------------|-------------|-----------|-------------|-------------|
| mp4 file     | ✅             | ✅ (default) | ✅         | ✅ (default) | ✅           |
| ogg file     |               |             |           |             | ✅ (default) |
| rtmp stream  | ✅             | ✅ (default) | ✅         | ✅ (default) |             |
| rtmps stream | ✅             | ✅ (default) | ✅         | ✅ (default) |             |

* Track egress is exported directly, without transcoding.

| Track codec | File Output |
|-------------|-------------|
| h264        | h264 file   |
| vp8         | ivf file    |
| opus        | ogg file    |

## Running locally

These changes are **not** recommended for a production setup.

To run against a local livekit server, you'll need to do the following:
* open `/usr/local/etc/redis.conf` and comment out the line that says `bind 127.0.0.1`
* change `protected-mode yes` to `protected-mode no` in the same file
* find your IP as seen by docker
  * `ws_url` needs to be set using the IP as Docker sees it
  * on linux, this should be `172.17.0.1`
  * on mac or windows, run `docker run -it --rm alpine nslookup host.docker.internal` and you should see something like
    `Name:	host.docker.internal
    Address: 192.168.65.2`
* your livekit-server must be run using `--node-ip` set to the above IP

These changes allow the service to connect to your local redis instance from inside the docker container.

Create a directory to mount. In this example, we will use `~/livekit-egress`.

Create a config.yaml in the above directory.
* `redis` and `ws_url` should use the above IP instead of `localhost` 
* `insecure` should be set to true

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
    -v ~/livekit-egress:/out \
    livekit-egress
```

You can then use our [cli](https://github.com/livekit/livekit-cli) to submit egress requests to your server.

## API

All RPC definitions and options can be found [here](https://github.com/livekit/protocol/blob/main/livekit_egress.proto).  

The Egress API exists within our server sdks ([Go Egress Client](https://github.com/livekit/server-sdk-go/blob/main/egressclient.go) 
and [JS Egress Client](https://github.com/livekit/server-sdk-js/blob/main/src/EgressClient.ts)), and our
[cli](https://github.com/livekit/livekit-cli/blob/main/cmd/livekit-cli/egress.go) can be used to submit requests manually.  
The API is part of LiveKit Server, which uses redis to communicate with the Egress service.

The following examples are using the Go SDK.

```go
ctx := context.Background()
egressClient := lksdk.NewEgressClient(url, apiKey, secret)
```

### StartRoomCompositeEgress

Starts a web composite egress (uses a web template).

File example:
```go
fileRequest := &livekit.RoomCompositeEgressRequest{
    RoomName:  "my-room",
    Layout:    "speaker-dark",
    Output: &livekit.RoomCompositeEgressRequest_File{
        File: &livekit.EncodedFileOutput{
            FileType: livekit.EncodedFileType_MP4,
            Filepath: "my-room-test.mp4",
            Output: &livekit.EncodedFileOutput_S3{
                S3: &livekit.S3Upload{
                    AccessKey: "<AWS access key>",
                    Secret:    "<AWS access secret>",
                    Region:    "<AWS region>",
                    Bucket:    "my-bucket",
                },
            },
        },
    },
}

info, err := egressClient.StartRoomCompositeEgress(ctx, fileRequest)
egressID := info.EgressId
```

* `fileRequest.Output.File.Output` can be left empty if one of `s3`, `azure`, or `gcp` is supplied with your config (see [below](#config)).
* `fileRequest.Output.File.Filepath` can be left empty, and a unique filename will be generated based on the date and room name.

Stream example:
```go
streamRequest := &livekit.RoomCompositeEgressRequest{
    RoomName:  "my-room",
    Layout:    "speaker-dark",
    Output: &livekit.RoomCompositeEgressRequest_Stream{
        Stream: &livekit.StreamOutput{
            Protocol: livekit.StreamProtocol_RTMP,
            Urls:     []string{"rtmp://live.twitch.tv/app/<stream-key>"},
        },
    },
}

info, err := egressClient.StartRoomCompositeEgress(ctx, streamRequest)
streamEgressID := info.EgressId
```

Built-in layouts include `speaker-dark`, `speaker-light`, `grid-dark`, and `grid-light`.  
To create your own web templates, see our [web README](https://github.com/livekit/livekit-egress/blob/main/web/README.md).


### StartTrackEgress

Exports a track without transcoding or processing. You can either export the file via an HTTP POST to a URL,
or streaming it via WebSocket.

In this mode, `Content-Type` header will be set to the MIME-Type of the track. The streams will be 
exported without a container. I.e. `audio/opus` for audio tracks, and `video/h264` or `video/vp8` for video tracks.

#### Websocket stream

When a `TrackEgressRequest` is started with a websocket URL, we'll initiate a WebSocket request to the desired URL.

This gives you a way of receiving a real-time stream of media from LiveKit rooms. It's helpful for when additional
processing is desired on the media streams. For example, you could stream out audio tracks and send them to a real-time
transcription service.

We'll send a combination of binary and text frames. Binary frames would contain audio or video data, in the 
encoding specified by `Content-Type`. The text frames will contain end user events on the tracks. For example: if the
track was muted, you will receive the following:

```json
{ "muted": true }
```

And when unmuted:

```json
{ "muted": false }
```

The WebSocket connection will terminate when the track is unpublished (or if the participant leaves the room).

### UpdateLayout (coming soon)

Used to change the web layout on an active RoomCompositeEgress.

```go
info, err := egressClient.UpdateLayout(ctx, &livekit.UpdateLayoutRequest{
    EgressId: egressID,
    Layout:   "grid-dark",
})
```

### UpdateStream

Used to add or remove stream urls from an active stream

```go
info, err := egressClient.UpdateStream(ctx, &livekit.UpdateStreamRequest{
	EgressId:      streamEgressID,
	AddOutputUrls: []string{"rtmp://a.rtmp.youtube.com/live2/<stream-key>"}
})
```

### ListEgress

Used to list active egress. Does not include completed egress.

```go
res, err := egressClient.ListEgress(ctx, &livekit.ListEgressRequest{})
for _, item := range res.Items {
	fmt.Println(item.EgressId)
}
```


### StopEgress

Stops an active egress.

```go
info, err := egressClient.StopEgress(ctx, &livekit.StopEgressRequest{
	EgressId: egressID,
})
```

## Deployment

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
template_base: can be used to host custom templates (default https://egress-composite.livekit.io/#)
insecure: can be used to connect to an insecure websocket (default false)

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
```

The config file can be added to a mounted volume with its location passed in the EGRESS_CONFIG_FILE env var, or its body can be passed in the EGRESS_CONFIG_BODY env var.

### Version Compatibility

Egress is still in beta and subject to change. The following versions are compatible:

| livekit-egress | protocol | livekit-server | server-sdk-go | server-sdk-js | livekit-cli |
|----------------|----------|----------------|---------------|---------------|-------------|
| v0.4+          | v0.13.0+ | v0.15.7+       | v0.9.3+       | v0.5.11+      | v0.7.2+     |

### Autoscaling

The `livekit_egress_available` Prometheus metric is provided to support autoscaling. `prometheus_port` must be defined in your config.

## FAQ

### I get a `"no response from egress service"` error when sending a request

* Your livekit server cannot an egress instance through redis. Make sure they are both able to reach the same redis db.
* Each instance currently only accepts one RoomCompositeRequest at a time - if it's already in use, you'll need to deploy more instances or set up autoscaling.

### I get a different error when sending a request

* Make sure your livekit-egress, livekit-server, server-sdk-go, server-sdk-js, and livekit-cli are compatible (see [version compatibility](#version-compatibility)).

### I'm getting a broken mp4 file

* GStreamer needs to be properly shut down - if the process is killed, the file will be unusable. Make sure you're stopping the recording with a `StopEgressRequest`.

### I'm seeing GStreamer warnings/errors. Is this normal?

* `GStreamer-CRITICAL **: 20:22:13.875: gst_mini_object_unref: assertion 'GST_MINI_OBJECT_REFCOUNT_VALUE (mini_object) > 0' failed`
  * Occurs during audio-only egress - this is a gst bug, and is safe to ignore.
* `WARN flvmux ... Got backwards dts! (0:01:10.379000000 < 0:01:10.457000000)`
  * Occurs when streaming to rtmp - safe to ignore. These warnings occur due to live sources being used for the flvmux. The dts difference should be small (under 150ms).

### Can I run this without docker?

* It's possible, but not recommended. To do so, you would need gstreamer and all the plugins installed, along with xvfb,
  and have a pulseaudio server running.
* Since it records from system audio, each recorder needs to be run on its own machine or VM.

## Testing and Development

Running `mage test` will run the test suite on your machine, and will dump the resulting files into livekit-egress/test.

To run these tests against your own LiveKit rooms, a deployed LiveKit server with a secure websocket url is required.
First, create a `livekit-egress/test/config.yaml`:

```yaml
log_level: info
api_key: your-api-key
api_secret: your-api-secret
ws_url: your-wss-url
```

Join a room using https://example.livekit.io/#/ or your own client, then run `LIVEKIT_ROOM_NAME=my-room mage integration test/config.yaml`.  
This will test recording different file types, output settings, and streams against your room.

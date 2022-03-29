# LiveKit Egress

All your live recording needs in one place.  
Record any website using our recorder, or deploy our service to manage it for you.

## How it works

Depending on your request type, the egress service will either launch a web template in Chrome and connect to the room 
(web composite requests), or it will use the sdk directly (track and track composite requests). It uses GStreamer to
encode, and can output to a file or to one or more streams.

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

### StartWebCompositeEgress

Starts a web composite egress (uses a web template).

```go
request := &livekit.WebCompositeEgressRequest{
    RoomName:  "my-room",
    Layout:    "speaker-dark",
    Output: &livekit.WebCompositeEgressRequest_Stream{
        Stream: &livekit.StreamOutput{
            Protocol: livekit.StreamProtocol_RTMP,
            Urls:     []string{"rtmp://live.twitch.tv/app/<stream-key>"},
        },
    },
}

info, err := egressClient.StartWebCompositeEgress(ctx, request)
egressID := info.EgressId
```

Available layouts include `speaker-dark`, `speaker-light`, `grid-dark`, and `grid-light`.  
To create your own web templates, see our [web README](https://github.com/livekit/livekit-egress/blob/main/web/README.md).

### UpdateLayout

Used to change the layout on an active WebCompositeEgress.

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
	EgressId:      egressID,
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
template_base: can be used to host custom templates (default https://recorder.livekit.io/#)
insecure: can be used to connect to an insecure websocket (default false)

# file upload config - only one of the following
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

### Autoscaling

The `livekit_egress_available` Prometheus metric is provided to support autoscaling. `prometheus_port` must be defined in your config.

## Local Testing and Development

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

## FAQ

### I get a `"no response from egress service"` error when sending a request

* Your livekit server cannot an egress instance through redis. Make sure they are both able to reach the same redis db.
* Each instance currently only accepts one WebCompositeRequest at a time - if it's already in use, you'll need to deploy more instances or set up autoscaling.

### I get a different error when sending a request

* Make sure you've updated to the latest cli, server sdks, livekit-server and livekit-egress. It's still in beta, and we are updating things regularly.

### I'm getting a broken mp4 file

* GStreamer needs to be properly shut down - if the process is killed, the file will be unusable. Make sure you're stopping the recording with a `StopEgressRequest`.

### I'm seeing GStreamer warnings/errors. Is this normal?

* `GStreamer-CRITICAL **: 20:22:13.875: gst_mini_object_unref: assertion 'GST_MINI_OBJECT_REFCOUNT_VALUE (mini_object) > 0' failed`
  * Occurs when streaming to rtmp - this is a gst bug, and is safe to ignore.
* `WARN flvmux ... Got backwards dts! (0:01:10.379000000 < 0:01:10.457000000)`
  * Occurs when streaming to rtmp - safe to ignore. These warnings occur due to live sources being used for the flvmux. The dts difference should be small (under 150ms).

### Can I run this without docker?

* It's possible, but not recommended. To do so, you would need gstreamer and all the plugins installed, along with xvfb,
  and have a pulseaudio server running.
* Since it records from system audio, each recorder needs to be run on its own machine or VM.

# LiveKit Recorder Service

## How it works

The service listens to a redis subscription and waits for the LiveKit server to make a reservation. Once the reservation 
is made to ensure availability, the service waits for a `START_RECORDING` signal from the server before launching the
[recorder](https://github.com/livekit/livekit-recorder/tree/main/recorder). The recorder will be stopped by 
either a `END_RECORDING` signal from the server, or automatically when the last participant leaves if using our templates.

A single instance can record one room at a time.

## Guides

See guides and deployment docs at https://docs.livekit.io/guides/recording

## Config

The only required field is redis address. This must be the same redis address used by your LiveKit server.  
If you want to use templates without supplying your own tokens, `api_key` and 
`api_secret` are also required.

```yaml
redis:
    address: redis address, including port (required)
    username: redis username (optional)
    password: redis password (optional)
    db: redis db (optional)
api_key: livekit server api key (required if using templates without supplying tokens)
api_secret: livekit server api secret (required if using templates without supplying tokens)
# default recording input options (optional)
input:
    width: defaults to 1920
    height: defaults to 1080
    depth: defaults to 24
    framerate: defaults to 30
# default recording output options (optional)
output:
    width: defaults to 0 (no scaling)
    height: defaults to 0 (no scaling)
    audio_bitrate: defaults to "128k"
    audio_frequency: defaults to "44100"
    video_bitrate: defaults to "2976k"
    video_buffer: defaults to "5952k". Using (videoBitrate * 2) is recommended.
log_level: valid levels are debug, info, warn, error, fatal, or panic (optional)

```

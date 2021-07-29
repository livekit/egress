# LiveKit Recorder Service

## How it works

The service listens to a redis subscription and waits for the LiveKit server to make a reservation. Once the reservation 
is made to ensure availability, the service waits for a `START_RECORDING` signal from the server before launching the
[recorder](https://github.com/livekit/livekit-recorder/tree/main/recorder). The recorder will be stopped by 
either a `END_RECORDING` signal from the server, or when the room closes.

A single instance can record one room at a time.

## Config

The only required field is redis address. This must be the same redis address used by your LiveKit server.  
If you want to use templates without supplying your own tokens, `apiKey` and 
`apiSecret` are also required.

```yaml
{
    "redis": {
        "address": redis address, including port (required)
        "username": redis username (optional)
        "password": redis password (optional)
        "db": redis db (optional)
    },
    "apiKey": livekit server api key (required if using templates without supplying tokens)
    "apiSecret": livekit server api secret (required if using templates without supplying tokens)
    # default recording input options (optional)
    "input": { 
        "width": defaults to 1920
        "height": defaults to 1080
        "depth": defaults to 24
        "framerate": defaults to 25
    },
    # default recording output options (optional)
    "output": {
        "width": defaults to 0 (no scaling)
        "height": defaults to 0 (no scaling)
        "audioBitrate": defaults to "128k"
        "audioFrequency": defaults to "44100"
        "videoBitrate": defaults to "2976k"
        "videoBuffer": defaults to "5952k". Using (videoBitrate * 2) is recommended.
    },
    "logLevel": valid levels are debug, info, warn, error, fatal, or panic (optional)
}
```

## Deploying

TBD

# livekit-recording

## What it does

The recorder launches Chrome and navigates to the supplied url, grabs audio from pulse and video from a virtual frame
buffer, and feeds them into ffmpeg. You can write the output as mp4 to a file or upload it to s3, or forward the
output to a rtmp stream. If you don't supply any output options, it will write to `/app/recording.mp4`

This does not need to be used with livekit streaming - you can use this to save or stream any website.

The config should be passed to docker through the `LIVEKIT_RECORDING_CONFIG` env var.

## Recording Options

### Using templates

We have 3 templates available - grid, gallery, and speaker. Just supply your server url, api key, and secret.

```json
{   
    "input": {
        "template": {
            "type": "grid|gallery|speaker",
            "wsUrl": "wss://your-livekit-address.com",
            "roomName": "room-to-record",
            "apiKey": "<key>",
            "apiSecret": "<secret>"
        }
    }
}
```

### Using a custom webpage

You can also save or stream any other webpage - just supply the url.
```json
{   
    "input": {
        "url": "your-recording-domain.com"
    }
}
```

### Using json config file

To use a config file, supply the full file as a string in `LIVEKIT_RECORDING_CONFIG`:
```bash
LIVEKIT_RECORDING_CONFIG="$(cat config.json)"
```
input: Either `url` or `template` required.  
output: Either `file`, `rtmp`, or `s3` required.  
All other fields optional.

All config options:
```yaml
{   
    "input": {
        "url": custom url of recording web page
        "template": {
            "type": grid | gallery | speaker
            "wsUrl": livekit server websocket url
            "token": livekit access token
            "roomName": room name
            "apiKey": livekit server api key
            "apiSecret": livekit server api secret
        }
        "width": defaults to 1920
        "height": defaults to 1080
        "depth": defaults to 24
        "framerate": defaults to 25
    }
    "output": {
        "file": filename
        "rtmp": rtmp url
        "s3": {
            "accessKey": aws access id
            "secret": aws secret
            "bucket": s3 bucket
            "key": filename
        }
        "width": scale output width
        "height": scale output height
        "audioBitrate": defaults to 128k
        "audioFrequency": defaults to 44100
        "videoBitrate": defaults to 2976k
        "videoBuffer": defaults to 5952k
    }
}
```

## Examples

### Save to file

file.json
```json
{
    "input": {
        "url": "https://your-recording-domain.com"
    },
    "output": {
        "file": "recording.mp4"
    }
}
```

```bash
docker run -e LIVEKIT_RECORDING_CONFIG="$(cat file.json)" livekit/livekit-recorder

# copy file to host after completion
docker cp <container_name>:app/recording.mp4 .
```

### Record on custom webpage and upload to S3

s3.json
```json
{
    "input": {
        "url": "https://your-recording-domain.com"
    },
    "output": {
        "S3": {
            "accessKey": "<aws-access-key>",
            "secret": "<aws-secret>",
            "bucket": "bucket-name",
            "key": "recording.mp4"
        }
    }
}
```

```bash
docker run -e LIVEKIT_RECORDING_CONFIG="$(cat s3.json)" -rm livekit/livekit-recorder
```

### Stream to Twitch, scaled to 720p

twitch.json
```json
{
    "input": {
        "template": {
            "type": "speaker",
            "wsUrl": "wss://your-livekit-address.com",
            "apiKey": "<key>",
            "apiSecret": "<secret>"
        },
        "width": 1920,
        "height": 1080
    },
    "output": {
        "rtmp": "rtmp://live.twitch.tv/app/<stream key>",
        "width": 1280,
        "height": 720
    }
}
```

```bash
docker run -e LIVEKIT_RECORDING_CONFIG="$(cat twitch.json)" -rm livekit/livekit-recorder
```

## Building your own templates

When using this option, you must handle token generation/room connection - the recorder will only open the url and start recording.

To stop the recorder, the page must send a `console.log('END_RECORDING')`.  
For example, our templates do the following:
```js  
const onParticipantDisconnected = (room: Room) => {
    /* Special rule for recorder */
    if (room.participants.size === 0) {
      console.log("END_RECORDING")
    }
}
```

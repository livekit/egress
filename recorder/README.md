# LiveKit Recorder

## How it works

The recorder launches Chrome and navigates to the supplied url, grabs audio from pulse and video from a virtual frame
buffer, and feeds them into ffmpeg. You can write the output as mp4 to a file or upload it to s3, or forward the
output to a rtmp stream.

It can be used standalone to make a single recording of any webpage, or it can be managed by our 
[recorder service](https://github.com/livekit/livekit-recorder/tree/main/service).

Once started, the recorder can be safely stopped by sending a `SIGINT` or by logging `END_RECORDING` to the console.

## Recording Options

### Using templates

We have 3 templates available - grid, gallery, and speaker. Just supply your server api key and secret, along with the websocket url.  
Check out our [templates README](https://github.com/livekit/livekit-recorder/tree/main/web) to learn more or create your own. 

```json
{
    "apiKey": "<key>",
    "apiSecret": "<secret>",
    "input": {
        "template": {
            "type": "grid|gallery|speaker",
            "wsUrl": "wss://your-livekit-address.com",
            "roomName": "room-to-record"
        }
    }
}
```
Or, to use your own token instead of having the recorder generate one:
```json
{
    "input": {
        "template": {
            "type": "grid|gallery|speaker",
            "wsUrl": "wss://your-livekit-address.com",
            "token": "<token>"
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

### Config

Supply your config as json in `LIVEKIT_RECORDER_CONFIG`:
```bash
LIVEKIT_RECORDER_CONFIG="$(cat config.json)"
```
input: Either `url` or `template` required. For `template`, either `token`, or `apiKey`, `apiSecret`, and `roomName` required.    
output: Either `file`, `rtmp`, or `s3` required.  
All other fields optional.

All config options:
```yaml
{
    "apiKey": livekit server api key - required if using template + roomName
    "apiSecret": livekit server api secret - required if using template + roomName
    "input": {
        "url": custom url of recording web page
        "template": {
            "type": grid | gallery | speaker
            "wsUrl": livekit server websocket url
            "token": livekit access token
            "roomName": room name
        }
        "width": defaults to 1920 (optional)
        "height": defaults to 1080 (optional)
        "depth": defaults to 24 (optional)
        "framerate": defaults to 25 (optional)
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
        "width": scale output width (optional)
        "height": scale output height (optional)
        "audioBitrate": defaults to 128k (optional)
        "audioFrequency": defaults to 44100 (optional)
        "videoBitrate": defaults to 2976k (optional)
        "videoBuffer": defaults to 5952k (optional)
    }
}
```

## Examples

### Save to file

Note: the file will be saved to `app/<filename.mp4>` on the docker image.  
You can copy it to the host by running `docker cp <container_name>:app/<filename.mp4> .` after completion.

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
docker run -e LIVEKIT_RECORDER_CONFIG="$(cat file.json)" livekit/livekit-recorder

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
docker run -e LIVEKIT_RECORDER_CONFIG="$(cat s3.json)" -rm livekit/livekit-recorder
```

### Stream to Twitch, scaled to 720p

twitch.json
```json
{
    "apiKey": "<key>",
    "apiSecret": "<secret>",
    "input": {
        "template": {
            "type": "speaker",
            "wsUrl": "wss://your-livekit-address.com",
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
docker run -e LIVEKIT_RECORDER_CONFIG="$(cat twitch.json)" -rm livekit/livekit-recorder
```

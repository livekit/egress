# LiveKit Recorder

## How it works

The recorder launches Chrome and navigates to the supplied url, grabs audio from pulse and video from a virtual frame
buffer, and feeds them into ffmpeg. You can write the output as mp4 to a file or upload it to s3, or forward the
output to a rtmp stream.

It can be used standalone to make a single recording of any webpage, or it can be managed by our 
[recorder service](https://github.com/livekit/livekit-recorder/tree/main/service).

## Config

The recorder expects a json config in the `LIVEKIT_RECORDER_CONFIG` environment variable:
```bash
LIVEKIT_RECORDER_CONFIG="$(cat config.json)"
```

Input: Either `url` or `template` required. For `template`, either `token`, or `api_key`, `api_secret`, and `room_name` 
required.  
Output: Either `file`, `rtmp`, or `s3` required.  

See [Input](#input) and [Output](#output) sections below for more details.   

All config options:
```yaml
{
    "api_key": livekit server api key - required if using template + room_name
    "api_secret": livekit server api secret - required if using template + room_name
    "input": {
        "url": custom url of recording web page
        "template": {
            "layout": <grid|speaker>-<light|dark>
            "ws_url": livekit server websocket url
            "token": livekit access token
            "room_name": room name
        }
        "width": defaults to 1920 (optional)
        "height": defaults to 1080 (optional)
        "depth": defaults to 24 (optional)
        "framerate": defaults to 30 (optional)
    }
    "output": {
        "file": filename
        "rtmp": rtmp url
        "s3": {
            "access_key": aws access id
            "secret": aws secret
            "bucket": s3 bucket
            "key": filename
        }
        "width": scale output width (optional)
        "height": scale output height (optional)
        "audio_bitrate": defaults to 128k (optional)
        "audio_frequency": defaults to 44100 (optional)
        "video_bitrate": defaults to 2976k (optional)
        "video_buffer": defaults to 5952k (optional)
    }
}
```

## Input

### Using templates

We currently have 4 templates available - grid or speaker, each available in light or dark. 
Just supply your server api key and secret, along with the websocket url.  
Check out our [templates README](https://github.com/livekit/livekit-recorder/tree/main/web) to learn more or create your own. 

```json
{
    "api_key": "<key>",
    "api_secret": "<secret>",
    "input": {
        "template": {
            "layout": "<grid|speaker>-<light|dark>",
            "ws_url": "wss://your-livekit-address.com",
            "room_name": "room-to-record"
        }
    }
}
```
Or, to use your own token instead of having the recorder generate one:
```json
{
    "input": {
        "template": {
            "layout": "<grid|speaker>-<light|dark>",
            "ws_url": "wss://your-livekit-address.com",
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

## Output

### Save to file

```json
{
    "output": {
        "file": "/app/out/recording.mp4"
    }
}
```
Note: your local mounted directory needs to exist, and the docker directory should match file output (i.e. `/app/out`) 
```bash
mkdir -p ~/livekit/output

docker run --rm -e LIVEKIT_RECORDER_CONFIG="$(cat config.json)" \
    -v ~/livekit/output:/app/out \
    livekit/livekit-recorder
```

### Upload to S3

```json
{
    "output": {
        "S3": {
            "access_key": "<aws-access-key>",
            "secret": "<aws-secret>",
            "bucket": "<bucket-name>",
            "key": "recording.mp4"
        }
    }
}
```

```bash
docker run --rm -e LIVEKIT_RECORDER_CONFIG="$(cat config.json)" livekit/livekit-recorder
```

### RTMP

```json
{
    "output": {
        "rtmp": "<rtmp://stream-url.com>"
    }
}
```

```bash
docker run --rm -e LIVEKIT_RECORDER_CONFIG="$(cat config.json)" livekit/livekit-recorder
```

## Ending a recording

Once started, there are a number of ways to end the recording:
* `docker stop <container>`
* if using our templates, the recorder will stop automatically when the last participant leaves
* if using your own webpage, logging `END_RECORDING` to the console

With any of these methods, the recorder will stop ffmpeg and finish uploading before shutting down.

## Examples

### Basic recording

basic.json:
```json
{
  "api_key": "<server-api-key>",
  "api_secret": "<server-api-secret>",
  "input": {
    "template": {
      "layout": "speaker-dark",
      "ws_url": "<wss://livekit.your-domain.com>",
      "room_name": "<my-room>"
    }
  },
  "output": {
    "file": "/app/out/recording.mp4"
  }
}
```
```bash
mkdir -p ~/livekit/output

docker run --rm -e LIVEKIT_RECORDER_CONFIG="$(cat basic.json)" \
    -v ~/livekit/output:/app/out \
    livekit/livekit-recorder
```

### Record custom url at 720p, 60fps and upload to s3

s3.json:
```json
{
    "input": {
      "url": "https://your-recording-domain.com",
      "width": 1280,
      "height": 720,
      "framerate": 60
    },
    "output": {
        "S3": {
            "access_key": "<aws-access-key>",
            "secret": "<aws-secret>",
            "bucket": "<my-bucket>",
            "key": "recording.mp4"
        }
    }
}
```
```bash
docker run --rm --name my-recorder -e LIVEKIT_RECORDER_CONFIG="$(cat s3.json)" livekit/livekit-recorder
```
```bash
docker stop my-recorder
```

### Stream to Twitch, scaling output from 1080p to 720p

twitch.json:
```json
{
  "input": {
    "template": {
      "layout": "speaker-dark",
      "ws_url": "<wss://livekit.your-domain.com>",
      "token": "<recording-token>"
    },
    "width": 1920,
    "height": 1080
  },
  "output": {
    "rtmp": "rtmp://live.twitch.tv/app/<stream-key>",
    "width": 1280,
    "height": 720
  }
}
```
```bash
docker run --rm -e LIVEKIT_RECORDER_CONFIG="$(cat twitch.json)" livekit/livekit-recorder
```

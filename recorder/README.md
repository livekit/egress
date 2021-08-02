# LiveKit Recorder

## How it works

The recorder launches Chrome and navigates to the supplied url, grabs audio from pulse and video from a virtual frame
buffer, and feeds them into ffmpeg. You can write the output as mp4 to a file or upload it to s3, or forward the
output to a rtmp stream.

It can be used standalone to make a single recording of any webpage, or it can be managed by our 
[recorder service](https://github.com/livekit/livekit-recorder/tree/main/service).

## Recording

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
            "type": "<grid|speaker>-<light|dark>",
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
            "type": "<grid|speaker>-<light|dark>",
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

### Ending a recording

Once started, there are a number of ways to end the recording:
* if not running in docker, `SIGINT` or `SIGTERM`
* `docker stop <container>` if running the docker image
* if using our templates, the recorder will stop automatically when the last participant leaves
* if using your own webpage, logging `END_RECORDING` to the console

With any of these methods, the recorder will stop ffmpeg and finish uploading before shutting down.

### Config

Supply your config as json in `LIVEKIT_RECORDER_CONFIG`:
```bash
LIVEKIT_RECORDER_CONFIG="$(cat config.json)"
```
input: Either `url` or `template` required. For `template`, either `token`, or `api_key`, `api_secret`, and `room_name` required.    
output: Either `file`, `rtmp`, or `s3` required.  
All other fields optional.

All config options:
```yaml
{
    "api_key": livekit server api key - required if using template + room_name
    "api_secret": livekit server api secret - required if using template + room_name
    "input": {
        "url": custom url of recording web page
        "template": {
            "type": <grid|speaker>-<light|dark>
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
            "access_key": "<aws-access-key>",
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
    "api_key": "<key>",
    "api_secret": "<secret>",
    "input": {
        "template": {
            "type": "speaker",
            "ws_url": "wss://your-livekit-address.com",
            "room_name": "my-stream"
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

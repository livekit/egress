# LiveKit Recording Templates

## Using our templates

We currently have 4 templates available - `speaker-light`, `speaker-dark`, `grid-light`, and `grid-dark`.  
When using our templates, you should use a 16:9 input aspect ratio (our recorder defaults to 1080p).

Example usage:
```json
{
  "api_key": "<key>",
  "api_secret": "<secret>",
  "input": {
    "template": {
      "type": "speaker-dark",
      "ws_url": "wss://your-livekit-address.com",
      "room_name": "room-to-record"
    }
  }
}
```


## Building your own templates

When using this option, you must handle token generation/room connection - the recorder will only open the url and start recording.

To have the recorder stop when the room closes, the page should send a `console.log('END_RECORDING')`.  
For example, our templates do the following:
```js  
const onParticipantDisconnected = (room: Room) => {
    /* Special rule for recorder */
    if (room.participants.size === 0) {
      console.log("END_RECORDING")
    }
}
```

If your page doesn't log this message, the recording will need to be stopped by sending a `EndRecordingRequest` to your LiveKit server.

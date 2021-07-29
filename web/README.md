# LiveKit Recording Templates

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

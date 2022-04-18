# Egress Recording Template SDK

This lightweight SDK makes it simple to build your own Room Composite templates.

## Overview

LiveKit uses an instance of headless Chrome to composite the room. This means you
can use the same application code to design an interactive experience that's
livestreamed or recorded.

The general flow of events are as follows:

1. You issue an `Egress.StartRoomCompositeEgress` request to LiveKit
2. LiveKit assigns an available instance of egress to handle it
3. Egress recorder creates necessary connection & authentication details for your template, including
   * url of livekit server
   * an access token that's designed to join the specific room as a recorder
   * desired layout passed to StartRoomCompositeEgress
4. Egress recorder launches Chrome with `<baseurl>?url=<>&template=<>&token=<>`
5. Egress recorder waits for webapp to log `START_RECORDING` to record
6. Egress recorder waits for webapp to log `END_RECORDING` to terminate the stream

Your webapp will need to connect to a LiveKit room using the token provided. This mechanism provides authentication to your application as well as giving you information about the current room that's to be recorded.

This template SDK takes care of communicating with Egress recorder, performing the following:

* parses URL parameters: url, token, layout
* communicates with recorder by logging to console
* handles layout changes requested by UpdateLayout

## Install

```sh
yarn add livekit-egress-sdk
```

## Usage

```typescript
import EgressHelper from 'livekit-egress-sdk'
import { Room } from 'livekit-client'

const room = new Room({
  adaptiveStream: true,
})

EgressHelper.setRoom(room, {
  autoEnd: true,
})
EgressHelper.onLayoutChanged((layout) => {
  // handle layout changes
})

await room.connect(
  EgressHelper.getLiveKitURL(),
  EgressHelper.getAccessToken(),
);
EgressHelper.startRecording();

```

## Example

We provide a few default templates/layouts [here](../egress-composite/). It should serve as a good guide for creating your own templates.

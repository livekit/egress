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
yarn add @livekit/egress-sdk
```

## Usage

```typescript
import EgressHelper from '@livekit/egress-sdk'
import { Room } from 'livekit-client'

const room = new Room({
  adaptiveStream: true,
})

// as soon as room is connected, notify egress helper so it can listen to events
// when autoEnd is set, recording will stop when all participants have left.
EgressHelper.setRoom(room, {
  autoEnd: true,
})

// advanced feature, if you'd like your recording to switch between different
// layouts programmatically using EgressService.UpdateLayout, those changes
// can be handled here
EgressHelper.onLayoutChanged((layout) => {
})

// connect to the room, and render your application UI as usual
// EgressHelper provides URL and token that are passed in by Egress Service
await room.connect(
  EgressHelper.getLiveKitURL(),
  EgressHelper.getAccessToken(),
);

// as soon as your application is set up and ready, call this API to start recording
EgressHelper.startRecording();

```

## Example

We provide a few default templates/layouts [here](../template-default/). It should serve as a good guide for creating your own templates.

## Testing your templates

In order to speed up the development cycle of your recording templates, we provide a convenient utility in [livekit-cli](https://github.com/livekit/livekit-cli). `test-egress-template` will spin up a few virtual publishers, and then simulate them joining your room. It'll also point a browser instance to your local template, with the correct URL parameters filled in.

Here's an example:

```shell
livekit-cli test-egress-template
  --base-url http://localhost:3000 \
  --url <livekit-instance>
  --api-key <key>
  --api-secret <secret>
  --room <your-room> --layout <your-layout> --publishers 3
```

This command will launch a browser pointed at `http://localhost:3000`, while simulating 3 publishers publishing to your livekit instance.

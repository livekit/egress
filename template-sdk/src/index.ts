/**
 * Copyright 2023 LiveKit, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { ParticipantEvent, Room, RoomEvent } from 'livekit-client';

export interface EgressOptions {
  // automatically finish recording when the last participant leaves
  autoEnd?: boolean;
}

const EgressHelper = {
  /**
   * RoomComposite will pass URL to your livekit's server instance.
   * @returns
   */
  getLiveKitURL(): string {
    const url = getURLParam('url');
    if (!url) {
      throw new Error('url is not found in query string');
    }
    return url;
  },

  /**
   *
   * @returns access token to pass to `Room.connect`
   */
  getAccessToken(): string {
    const token = getURLParam('token');
    if (!token) {
      throw new Error('token is not found in query string');
    }
    return token;
  },

  /**
   * the current desired layout. layout can be changed dynamically with [Egress.UpdateLayout](https://github.com/livekit/protocol/blob/main/livekit_egress.proto#L15)
   * @returns
   */
  getLayout(): string {
    if (state.layout) {
      return state.layout;
    }
    const layout = getURLParam('layout');
    return layout ?? '';
  },

  /**
   * Call when successfully connected to the room
   * @param room
   */
  setRoom(room: Room, opts?: EgressOptions) {
    if (currentRoom) {
      currentRoom.off(RoomEvent.ParticipantDisconnected, onParticipantDisconnected);
      currentRoom.off(RoomEvent.Disconnected, EgressHelper.endRecording);
    }

    currentRoom = room;
    currentRoom.localParticipant.on(ParticipantEvent.ParticipantMetadataChanged, onMetadataChanged);
    if (opts?.autoEnd) {
      currentRoom.on(RoomEvent.ParticipantDisconnected, onParticipantDisconnected);
    }
    currentRoom.on(RoomEvent.Disconnected, EgressHelper.endRecording);
    onMetadataChanged();
  },

  /**
   * Starts recording the room that's passed in
   */
  startRecording() {
    console.log('START_RECORDING');
  },

  /**
   * Finishes recording the room, by default, it'll end automatically finish
   * when all other participants have left the room.
   */
  endRecording() {
    currentRoom = undefined;
    console.log('END_RECORDING');
  },

  /**
   * Registers a callback to listen to layout changes.
   * @param f
   */
  onLayoutChanged(f: (layout: string) => void) {
    layoutChangedCallback = f;
  },
};

let currentRoom: Room | undefined;
let layoutChangedCallback: (layout: string) => void | undefined;
let state: TemplateState = {
  layout: '',
};

interface TemplateState {
  layout: string;
}

function onMetadataChanged() {
  // for recorder, metadata is a JSON object containing layout
  const metadata = currentRoom?.localParticipant.metadata;
  if (metadata) {
    const newState: TemplateState = JSON.parse(metadata);
    if (newState && newState.layout !== state.layout) {
      state = newState;
      layoutChangedCallback(state.layout);
    }
  }
}

function onParticipantDisconnected() {
  if (currentRoom) {
    if (currentRoom.participants.size === 0) {
      EgressHelper.endRecording();
    }
  }
}

function getURLParam(name: string): string | null {
  const query = new URLSearchParams(window.location.search);
  return query.get(name);
}

export default EgressHelper;

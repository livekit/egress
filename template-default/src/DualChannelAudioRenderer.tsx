/**
 * Copyright 2026 LiveKit, Inc.
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

import { TrackReference } from '@livekit/components-core';
import { useTracks } from '@livekit/components-react';
import { RemoteAudioTrack, Track } from 'livekit-client';
import { useEffect, useRef } from 'react';

export type DualChannelMixing = 'dual_channel_agent' | 'dual_channel_alternate';

const LEFT = -1;
const RIGHT = 1;

let audioContext: AudioContext | undefined;

function getAudioContext(): AudioContext {
  if (!audioContext) {
    audioContext = new AudioContext();
  }
  return audioContext;
}

interface DualChannelAudioRendererProps {
  mixing: DualChannelMixing;
}

/**
 * Plays all subscribed audio tracks through WebAudio, panning each track hard
 * left or right so the recorded stereo output carries a channel-separated mix:
 * - dual_channel_agent: agent participants left, everyone else right
 * - dual_channel_alternate: alternating channel per track, assigned on first
 *   appearance and kept for the track's lifetime
 */
export default function DualChannelAudioRenderer({ mixing }: DualChannelAudioRendererProps) {
  const trackRefs = useTracks(
    [Track.Source.Microphone, Track.Source.ScreenShareAudio, Track.Source.Unknown],
    { onlySubscribed: true },
  ).filter(
    (ref) => !ref.participant.isLocal && ref.publication.kind === Track.Kind.Audio,
  );

  const assignedPans = useRef(new Map<string, number>());
  const nextPan = useRef(LEFT);

  const panFor = (ref: TrackReference): number => {
    if (mixing === 'dual_channel_agent') {
      return ref.participant.isAgent ? LEFT : RIGHT;
    }
    const sid = ref.publication.trackSid;
    let pan = assignedPans.current.get(sid);
    if (pan === undefined) {
      pan = nextPan.current;
      nextPan.current = pan === LEFT ? RIGHT : LEFT;
      assignedPans.current.set(sid, pan);
    }
    return pan;
  };

  return (
    <>
      {trackRefs.map((ref) => (
        <PannedAudioTrack key={ref.publication.trackSid} trackRef={ref} pan={panFor(ref)} />
      ))}
    </>
  );
}

interface PannedAudioTrackProps {
  trackRef: TrackReference;
  pan: number;
}

function PannedAudioTrack({ trackRef, pan }: PannedAudioTrackProps) {
  const track = trackRef.publication.track;

  useEffect(() => {
    if (!(track instanceof RemoteAudioTrack)) {
      return;
    }

    const ctx = getAudioContext();
    const stream = new MediaStream([track.mediaStreamTrack]);

    // Chrome does not deliver remote WebRTC track audio to WebAudio unless the
    // track is also attached to a media element
    const el = new Audio();
    el.srcObject = stream;
    el.muted = true;

    const source = ctx.createMediaStreamSource(stream);
    const panner = ctx.createStereoPanner();
    panner.pan.value = pan;
    source.connect(panner);
    panner.connect(ctx.destination);
    void ctx.resume();

    return () => {
      source.disconnect();
      panner.disconnect();
      el.srcObject = null;
    };
  }, [track, pan]);

  return null;
}

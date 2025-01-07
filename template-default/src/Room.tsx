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

import {
  GridLayout,
  LiveKitRoom,
  ParticipantTile,
  RoomAudioRenderer,
  useRoomContext,
  useTracks,
} from '@livekit/components-react';
import EgressHelper from '@livekit/egress-sdk';
import { ConnectionState, Track } from 'livekit-client';
import { ReactElement, useEffect, useState } from 'react';
import SingleSpeakerLayout from './SingleSpeakerLayout';
import SpeakerLayout from './SpeakerLayout';

const FRAME_DECODE_TIMEOUT = 5000;

interface RoomPageProps {
  url: string;
  token: string;
  layout: string;
}

export default function RoomPage({ url, token, layout }: RoomPageProps) {
  const [error, setError] = useState<Error>();
  if (!url || !token) {
    return <div className="error">missing required params url and token</div>;
  }

  return (
    <LiveKitRoom serverUrl={url} token={token} onError={setError}>
      {error ? <div className="error">{error.message}</div> : <CompositeTemplate layout={layout} />}
    </LiveKitRoom>
  );
}

interface CompositeTemplateProps {
  layout: string;
}

function CompositeTemplate({ layout: initialLayout }: CompositeTemplateProps) {
  const room = useRoomContext();
  const [layout] = useState(initialLayout);
  const [hasScreenShare, setHasScreenShare] = useState(false);
  const screenshareTracks = useTracks([Track.Source.ScreenShare], {
    onlySubscribed: true,
  });

  EgressHelper.setRoom(room);

  useEffect(() => {
    // determines when to start recording
    // the algorithm used is:
    // * if there are video tracks published, wait for frames to be decoded
    // * if there are no video tracks published, start immediately
    // * if it's been more than 10s, record as long as there are tracks subscribed
    const startTime = Date.now();
    const interval = setInterval(async () => {
      let shouldStartRecording = false;
      let hasVideoTracks = false;
      let hasSubscribedTracks = false;
      let hasDecodedFrames = false;
      for (const p of Array.from(room.remoteParticipants.values())) {
        for (const pub of Array.from(p.trackPublications.values())) {
          if (pub.isSubscribed) {
            hasSubscribedTracks = true;
          }
          if (pub.kind === Track.Kind.Video) {
            hasVideoTracks = true;
            if (pub.videoTrack) {
              const stats = await pub.videoTrack.getRTCStatsReport();
              if (stats) {
                hasDecodedFrames = Array.from(stats).some(
                  (item) => item[1].type === 'inbound-rtp' && item[1].framesDecoded > 0,
                );
              }
            }
          }
        }
      }

      const timeDelta = Date.now() - startTime;
      if (hasDecodedFrames) {
        shouldStartRecording = true;
      } else if (!hasVideoTracks && hasSubscribedTracks && timeDelta > 500) {
        // adding a small timeout to ensure video tracks has a chance to be published
        shouldStartRecording = true;
      } else if (timeDelta > FRAME_DECODE_TIMEOUT && hasSubscribedTracks) {
        shouldStartRecording = true;
      }

      if (shouldStartRecording) {
        EgressHelper.startRecording();
        clearInterval(interval);
      }
    }, 100);
    /* eslint-disable-next-line react-hooks/exhaustive-deps */
  }, []);

  useEffect(() => {
    if (screenshareTracks.length > 0 && screenshareTracks[0].publication) {
      setHasScreenShare(true);
    } else {
      setHasScreenShare(false);
    }
  }, [screenshareTracks]);

  const allTracks = useTracks(
    [Track.Source.Camera, Track.Source.ScreenShare, Track.Source.Unknown],
    {
      onlySubscribed: true,
    },
  );
  const filteredTracks = allTracks.filter(
    (tr) =>
      tr.publication.kind === Track.Kind.Video &&
      tr.participant.identity !== room.localParticipant.identity,
  );

  let interfaceStyle = 'dark';
  if (layout.endsWith('-light')) {
    interfaceStyle = 'light';
  }

  let containerClass = 'roomContainer';
  if (interfaceStyle) {
    containerClass += ` ${interfaceStyle}`;
  }

  // determine layout to use
  let main: ReactElement = <></>;
  let effectiveLayout = layout;
  if (hasScreenShare && layout.startsWith('grid')) {
    effectiveLayout = layout.replace('grid', 'speaker');
  }
  if (room.state !== ConnectionState.Disconnected) {
    if (effectiveLayout.startsWith('speaker')) {
      main = <SpeakerLayout tracks={filteredTracks} />;
    } else if (effectiveLayout.startsWith('single-speaker')) {
      main = <SingleSpeakerLayout tracks={filteredTracks} />;
    } else {
      main = (
        <GridLayout tracks={filteredTracks}>
          <ParticipantTile />
        </GridLayout>
      );
    }
  }

  return (
    <div className={containerClass}>
      {main}
      <RoomAudioRenderer />
    </div>
  );
}

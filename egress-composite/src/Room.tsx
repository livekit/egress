import {
  AudioTrack, Participant, RemoteParticipant, Room,
} from 'livekit-client';
import EgressHelper from 'livekit-egress-sdk';
import { AudioRenderer, useRoom } from 'livekit-react';
import React, { ReactElement, useEffect, useState } from 'react';
import GridLayout from './GridLayout';
import SpeakerLayout from './SpeakerLayout';

interface RoomPageProps {
  url: string;
  token: string;
  layout: string;
}

export default function RoomPage({ url, token, layout: initialLayout }: RoomPageProps) {
  const [layout, setLayout] = useState(initialLayout);
  const roomState = useRoom();
  const { room, participants, audioTracks } = roomState;

  useEffect(() => {
    roomState.connect(url, token, {
      adaptiveStream: true,
    });
  }, [url]);

  useEffect(() => {
    if (room) {
      EgressHelper.setRoom(room, {
        autoEnd: true,
      });
      // Egress layout can change on the fly, we can react to the new layout
      // here.
      EgressHelper.onLayoutChanged((newLayout) => {
        setLayout(newLayout);
      });

      // start recording immediately after connection
      EgressHelper.startRecording();
    }
  }, [room]);

  if (!url || !token) {
    return <div className="error">missing required params url and token</div>;
  }

  // not ready yet, don't render anything
  if (!room) {
    return <div />;
  }

  // filter out local participant
  const remoteParticipants = participants.filter((p) => p instanceof RemoteParticipant);

  return (
    <Stage
      layout={layout}
      room={room}
      participants={remoteParticipants}
      audioTracks={audioTracks}
    />
  );
}

interface StageProps {
  layout: string;
  room: Room;
  participants: Participant[];
  audioTracks: AudioTrack[];
}

function Stage({
  layout, room, participants, audioTracks,
}: StageProps) {
  const [hasScreenShare, setHasScreenShare] = useState(false);

  useEffect(() => {
    let found = false;
    for (const p of participants) {
      if (p.isScreenShareEnabled) {
        found = true;
      }
    }
    setHasScreenShare(found);
  }, [participants]);

  let interfaceStyle = 'light';
  if (layout === 'speaker-dark' || layout === 'grid-dark') {
    interfaceStyle = 'dark';
  }

  let containerClass = 'roomContainer';
  if (interfaceStyle) {
    containerClass += ` ${interfaceStyle}`;
  }

  // determine layout to use
  let main: ReactElement;
  if (layout.startsWith('speaker') || hasScreenShare) {
    main = (
      <SpeakerLayout
        room={room}
        participants={participants}
      />
    );
  } else {
    main = (
      <GridLayout
        room={room}
        participants={participants}
      />
    );
  }

  return (
    <div className={containerClass}>
      {main}
      {audioTracks.map((track) => (
        <AudioRenderer key={track.sid} track={track} isLocal={false} />
      ))}
    </div>
  );
}

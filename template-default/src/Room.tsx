import {
  GridLayout,
  LiveKitRoom,
  ParticipantTile,
  RoomAudioRenderer,
  useRoomContext,
  useTracks,
} from '@livekit/components-react';
import EgressHelper from '@livekit/egress-sdk';
import { ConnectionState, RoomEvent, Track } from 'livekit-client';
import { ReactElement, useEffect, useState } from 'react';
import SingleSpeakerLayout from './SingleSpeakerLayout';
import SpeakerLayout from './SpeakerLayout';

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
  const [layout, setLayout] = useState(initialLayout);
  const [hasScreenShare, setHasScreenShare] = useState(false);
  const screenshareTracks = useTracks([Track.Source.ScreenShare], {
    onlySubscribed: true,
  });

  useEffect(() => {
    if (room) {
      EgressHelper.setRoom(room);

      // Egress layout can change on the fly, we can react to the new layout
      // here.
      EgressHelper.onLayoutChanged((newLayout) => {
        setLayout(newLayout);
      });

      // start recording when there's already a track published
      let hasTrack = false;
      for (const p of Array.from(room.participants.values())) {
        if (p.tracks.size > 0) {
          hasTrack = true;
          break;
        }
      }

      if (hasTrack) {
        EgressHelper.startRecording();
      } else {
        room.once(RoomEvent.TrackSubscribed, () => EgressHelper.startRecording());
      }
    }
  }, [room]);

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

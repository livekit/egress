import {
  Participant, RemoteParticipant, RemoteVideoTrack, VideoQuality,
} from 'livekit-client';
import {
  AudioRenderer, LiveKitRoom, ParticipantView, StageProps,
} from 'livekit-react';
import React, { ReactElement } from 'react';
import {
  onConnected, stopRecording, TemplateProps, useParams,
} from './common';
import styles from './SpeakerPage.module.css';

export default function SpeakerPage({ interfaceStyle }: TemplateProps) {
  const { url, token } = useParams();

  if (!url || !token) {
    return <div className="error">missing required params url and token</div>;
  }

  let containerClass = 'roomContainer';
  if (interfaceStyle) {
    containerClass += ` ${interfaceStyle}`;
  }
  return (
    <div className={containerClass}>
      <LiveKitRoom
        url={url}
        token={token}
        onConnected={onConnected}
        onLeave={stopRecording}
        stageRenderer={renderStage}
        adaptiveVideo
      />
    </div>
  );
}

const renderStage: React.FC<StageProps> = ({ roomState }: StageProps) => {
  const {
    error, room, participants, audioTracks,
  } = roomState;

  if (error) {
    return <div className="error">{error}</div>;
  }

  if (!room) {
    return <div>room closed</div>;
  }

  // find first participant with screen shared
  let screenTrack: RemoteVideoTrack | undefined;
  const remoteParticipants = participants.filter((p) => (p instanceof RemoteParticipant));
  remoteParticipants.forEach((p) => {
    p.videoTracks.forEach((track) => {
      if (track.trackName === 'screen' && track.track) {
        screenTrack = track.track as RemoteVideoTrack;
      }
    });
  });

  if (remoteParticipants.length === 0) {
    return <div />;
  }

  const audioRenderers = audioTracks.map((track) => (
    <AudioRenderer key={track.sid} track={track} isLocal={false} />
  ));

  // full screen a single participant
  if (remoteParticipants.length === 0 && !screenTrack) {
    return (
      <>
        <ParticipantView
          participant={remoteParticipants[0]}
          width="100%"
          height="100%"
        />
        {audioRenderers}
      </>
    );
  }

  let otherParticipants: Participant[];
  let mainView: ReactElement;
  if (screenTrack) {
    otherParticipants = remoteParticipants;
    mainView = (
      // <ScreenShareView track={screenTrack} height="100%" width="100%" />
      <div />
    );
  } else {
    otherParticipants = remoteParticipants.slice(1);
    mainView = (
      <ParticipantView
        key={remoteParticipants[0].identity}
        participant={remoteParticipants[0]}
        aspectWidth={13}
        aspectHeight={9}
        width="100%"
        height="100%"
        quality={VideoQuality.HIGH}
      />
    );
  }

  return (
    <div className={styles.stage}>
      <div className={styles.stageCenter}>{mainView}</div>
      <div className={styles.stageSidebar}>
        {otherParticipants.map((participant) => (
          <ParticipantView
            key={participant.identity}
            participant={participant}
            width="100%"
            aspectWidth={16}
            aspectHeight={9}
            adaptiveVideo
          />
        ))}
      </div>
      {audioRenderers}
    </div>
  );
};

import { ParticipantView, ScreenShareView } from '@livekit/react-components';
import {
  Participant, RemoteParticipant, RemoteVideoTrack, Track,
} from 'livekit-client';
import React, { ReactElement } from 'react';
import { LayoutProps } from './common';
import styles from './SpeakerLayout.module.css';

const SpeakerLayout = ({ participants }: LayoutProps) => {
  // find first participant with screen shared
  let screenTrack: RemoteVideoTrack | undefined;
  const remoteParticipants = participants.filter((p) => (p instanceof RemoteParticipant));
  remoteParticipants.forEach((p) => {
    const pub = p.getTrack(Track.Source.ScreenShare);
    if (pub && pub.isSubscribed) {
      screenTrack = pub.videoTrack as RemoteVideoTrack;
    }
  });

  if (remoteParticipants.length === 0) {
    return <div />;
  }

  // full screen a single participant
  if (remoteParticipants.length === 1 && !screenTrack) {
    return (
      <>
        <ParticipantView
          participant={remoteParticipants[0]}
          width="100%"
          height="100%"
          orientation="landscape"
        />
      </>
    );
  }

  let otherParticipants: Participant[];
  let mainView: ReactElement;
  if (screenTrack) {
    otherParticipants = remoteParticipants;
    mainView = (
      <ScreenShareView track={screenTrack} height="100%" width="100%" />
    );
  } else {
    otherParticipants = remoteParticipants.slice(1);
    mainView = (
      <ParticipantView
        key={remoteParticipants[0].identity}
        participant={remoteParticipants[0]}
        width="100%"
        height="100%"
        orientation="landscape"
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
            height="100%"
            orientation="landscape"
          />
        ))}
      </div>
    </div>
  );
};

export default SpeakerLayout;

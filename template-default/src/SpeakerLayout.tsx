import { ParticipantView, ScreenShareView } from '@livekit/react-components';
import {
  Participant, RemoteVideoTrack, Track,
} from 'livekit-client';
import { ReactElement } from 'react';
import { LayoutProps } from './common';
import styles from './SpeakerLayout.module.css';
import useDominateSpeaker from './useDominateSpeaker';

const SpeakerLayout = ({ participants }: LayoutProps) => {
  const dominantSpeaker = useDominateSpeaker(participants);

  // find first participant with screen shared
  let screenTrack: RemoteVideoTrack | undefined;
  participants.forEach((p) => {
    const pub = p.getTrack(Track.Source.ScreenShare);
    if (pub && pub.isSubscribed) {
      screenTrack = pub.videoTrack as RemoteVideoTrack;
    }
  });

  if (participants.length === 0) {
    return <div />;
  }

  // full screen a single participant
  if (participants.length === 1 && !screenTrack) {
    return (
      <>
        <ParticipantView
          participant={participants[0]}
          width="100%"
          height="100%"
          orientation="landscape"
          speakerClassName=""
        />
      </>
    );
  }

  let otherParticipants: Participant[];
  let mainView: ReactElement;
  if (screenTrack) {
    otherParticipants = participants;
    mainView = (
      <ScreenShareView track={screenTrack} height="100%" width="100%" />
    );
  } else {
    const mainParticipant = dominantSpeaker ?? participants[0];
    otherParticipants = participants.filter((p) => p !== mainParticipant);
    mainView = (
      <ParticipantView
        key={mainParticipant.identity}
        participant={mainParticipant}
        width="100%"
        height="100%"
        orientation="landscape"
        speakerClassName=""
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
            aspectWidth={16}
            aspectHeight={9}
            orientation="landscape"
            speakerClassName=""
          />
        ))}
      </div>
    </div>
  );
};

export default SpeakerLayout;

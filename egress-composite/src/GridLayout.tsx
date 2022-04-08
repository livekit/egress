import { Participant } from 'livekit-client';
import { ParticipantView } from 'livekit-react';
import React, { useEffect } from 'react';
import {
  LayoutProps,
} from './common';
import styles from './GridLayout.module.css';

const GridLayout = ({ participants, room }: LayoutProps) => {
  const [visibleParticipants, setVisibleParticipants] = React.useState<Participant[]>([]);
  const [gridClass, setGridClass] = React.useState(styles.grid1x1);

  // select participants to display on first page, keeping ordering consistent if possible.
  useEffect(() => {
    let numVisible = participants.length;
    if (participants.length === 1) {
      setGridClass(styles.grid1x1);
    } else if (participants.length === 2) {
      setGridClass(styles.grid2x1);
    } else if (participants.length <= 4) {
      setGridClass(styles.grid2x2);
    } else if (participants.length <= 9) {
      setGridClass(styles.grid3x3);
    } else if (participants.length <= 16) {
      setGridClass(styles.grid4x4);
    } else {
      setGridClass(styles.grid5x5);
      numVisible = Math.min(numVisible, 25);
    }

    // remove any participants that are no longer connected
    const newParticipants: Participant[] = [];
    visibleParticipants.forEach((p) => {
      if (
        room?.participants.has(p.sid)
        || room?.localParticipant.sid === p.sid
      ) {
        newParticipants.push(p);
      }
    });

    // ensure active speakers are all visible
    room?.activeSpeakers?.forEach((speaker) => {
      if (
        newParticipants.includes(speaker)
        || (speaker !== room?.localParticipant
          && !room?.participants.has(speaker.sid))
      ) {
        return;
      }
      // find a non-active speaker and switch
      const idx = newParticipants.findIndex((p) => !p.isSpeaking);
      if (idx >= 0) {
        newParticipants[idx] = speaker;
      } else {
        newParticipants.push(speaker);
      }
    });

    // add other non speakers
    for (const p of participants) {
      if (newParticipants.length >= numVisible) {
        break;
      }
      if (newParticipants.includes(p) || p.isSpeaking) {
        continue;
      }
      newParticipants.push(p);
    }

    if (newParticipants.length > numVisible) {
      newParticipants.splice(numVisible, newParticipants.length - numVisible);
    }
    setVisibleParticipants(newParticipants);
  }, [participants]);

  if (visibleParticipants.length === 0) {
    return <div />;
  }

  return (
    <div className={`${styles.stage} ${gridClass}`}>
      {visibleParticipants.map((participant) => (
        <ParticipantView
          key={participant.identity}
          participant={participant}
          orientation="landscape"
          width="100%"
          height="100%"
        />
      ))}
    </div>
  );
};

export default GridLayout;

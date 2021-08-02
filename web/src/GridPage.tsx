import {
  LocalParticipant, Participant, RemoteParticipant,
} from 'livekit-client';
import {
  AudioRenderer, LiveKitRoom, ParticipantView, StageProps,
} from 'livekit-react';
import React, { useEffect } from 'react';
import {
  onConnected, stopRecording, TemplateProps, useParams,
} from './common';
import styles from './GridPage.module.css';

export default function GridPage({ interfaceStyle }: TemplateProps) {
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
  const [visibleParticipants, setVisibleParticipants] = React.useState<Participant[]>([]);
  const [gridClass, setGridClass] = React.useState(styles.grid1x1);

  // select participants to display on first page, keeping ordering consistent if possible.
  useEffect(() => {
    // remove any participants that are no longer connected
    const newParticipants: Participant[] = [];
    visibleParticipants.forEach((p) => {
      if (room?.participants.has(p.sid)) {
        newParticipants.push(p);
      }
    });

    // ensure active speaker is visible
    room?.activeSpeakers.forEach((speaker) => {
      if (newParticipants.includes(speaker) || speaker instanceof LocalParticipant) {
        return;
      }
      newParticipants.unshift(speaker);
    });

    for (let i = 0; i < participants.length; i += 1) {
      const participant = participants[i];
      if (participant instanceof RemoteParticipant && !newParticipants.includes(participant)) {
        newParticipants.push(participants[i]);
      }
      // max of 3x3 grid
      if (newParticipants.length >= 9) {
        return;
      }
    }
    if (newParticipants.length >= 9) {
      setGridClass(styles.grid3x3);
      newParticipants.splice(9, newParticipants.length - 9);
    } else if (newParticipants.length >= 6) {
      setGridClass(styles.grid3x3);
      // one empty row
    } else if (newParticipants.length >= 4) {
      setGridClass(styles.grid2x2);
      newParticipants.splice(4, newParticipants.length - 4);
    } else if (newParticipants.length === 3) {
      setGridClass(styles.grid2x2);
      // one empty spot
    } else if (newParticipants.length === 2) {
      setGridClass(styles.grid2x1);
    } else if (newParticipants.length === 1) {
      setGridClass(styles.grid1x1);
    }
    setVisibleParticipants(newParticipants);
  }, [participants]);

  if (error) {
    return <div className="error">{error}</div>;
  }

  if (!room) {
    return <div>room closed</div>;
  }

  if (visibleParticipants.length === 0) {
    return <div />;
  }

  const audioRenderers = audioTracks.map((track) => (
    <AudioRenderer key={track.sid} track={track} isLocal={false} />
  ));

  return (
    <div className={`${styles.stage} ${gridClass}`}>
      {visibleParticipants.map((participant) => (
        <ParticipantView
          key={participant.identity}
          participant={participant}
          orientation="landscape"
          width="100%"
          height="100%"
          adaptiveVideo
        />
      ))}
      {audioRenderers}
    </div>
  );
};

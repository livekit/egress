import { Room, RoomEvent } from 'livekit-client';
import { useLocation } from 'react-router-dom';

export interface TemplateProps {
  interfaceStyle?: 'dark' | 'light';
}

export interface ConnectionParams {
  url?: string;
  token?: string;
}

export function onConnected(room: Room) {
  if (room.participants.size > 0) {
    startRecording();
  } else {
    room.once(RoomEvent.ParticipantConnected, startRecording);
  }

  room.on(RoomEvent.ParticipantDisconnected, () => onParticipantDisconnected(room));
}

export function onParticipantDisconnected(room: Room) {
  if (room.participants.size === 0) {
    stopRecording();
  }
}

export function stopRecording() {
  console.log('END_RECORDING');
}

export function startRecording() {
  console.log('START_RECORDING');
}

export function useParams(): ConnectionParams {
  const query = new URLSearchParams(useLocation().search);
  return {
    url: query.get('url') ?? undefined,
    token: query.get('token') ?? undefined,
  };
}

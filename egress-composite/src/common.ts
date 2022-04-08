import { Participant, Room } from 'livekit-client';

export interface LayoutProps {
  participants: Participant[];
  room: Room;
}

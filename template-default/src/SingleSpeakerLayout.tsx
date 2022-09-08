import { ParticipantView, ScreenShareView } from '@livekit/react-components';
import { RemoteVideoTrack, Track } from 'livekit-client';
import { LayoutProps } from './common';
import useDominateSpeaker from './useDominateSpeaker';

const SingleSpeakerLayout = ({ participants }: LayoutProps) => {
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

  if (screenTrack) {
    return <ScreenShareView track={screenTrack} height="100%" width="100%" />;
  }
  const mainParticipant = dominantSpeaker ?? participants[0];
  return (
    <ParticipantView
      key={mainParticipant.identity}
      participant={mainParticipant}
      width="100%"
      height="100%"
      orientation="landscape"
      speakerClassName=""
    />
  );
};

export default SingleSpeakerLayout;

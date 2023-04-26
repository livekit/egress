import { TrackReference } from '@livekit/components-core';
import {
  CarouselView,
  FocusLayout,
  ParticipantTile,
  VideoTrack,
  useVisualStableUpdate,
} from '@livekit/components-react';
import { LayoutProps } from './common';

const SpeakerLayout = ({ tracks: references }: LayoutProps) => {
  const sortedTracks = useVisualStableUpdate(references, 1);
  const mainTrack = sortedTracks.shift();
  const remainingTracks = useVisualStableUpdate(sortedTracks, 3);

  if (!mainTrack) {
    return <></>;
  } else if (remainingTracks.length === 0) {
    const trackRef = mainTrack as TrackReference;
    return <VideoTrack {...trackRef} />;
  }

  return (
    <div className="lk-focus-layout">
      <CarouselView tracks={remainingTracks}>
        <ParticipantTile />
      </CarouselView>
      <FocusLayout track={mainTrack as TrackReference} />
    </div>
  );
};

export default SpeakerLayout;

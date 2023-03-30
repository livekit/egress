import { TrackReference } from '@livekit/components-core';
import { CarouselView, FocusLayout, useVisualStableUpdate } from '@livekit/components-react';
import { LayoutProps } from './common';

const SpeakerLayout = ({ tracks: references }: LayoutProps) => {
  const sortedTracks = useVisualStableUpdate(references, 1);
  const mainTrack = sortedTracks.shift();
  const remainingTracks = useVisualStableUpdate(sortedTracks, 3);

  return (
    <div className="lk-focus-layout">
      {/* TODO: remove selected reference from CarouselView */}
      <CarouselView tracks={remainingTracks} />
      {sortedTracks.length > 0 && <FocusLayout track={mainTrack as TrackReference} />}
    </div>
  );
};

export default SpeakerLayout;

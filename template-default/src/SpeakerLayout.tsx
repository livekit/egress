import { TrackReference } from '@livekit/components-core';
import { CarouselView, FocusLayout, useVisualStableUpdate } from '@livekit/components-react';
import { LayoutProps } from './common';

const SpeakerLayout = ({ references }: LayoutProps) => {
  const sortedReferences = useVisualStableUpdate(references, 1);

  return (
    <div className="lk-focus-layout">
      {/* TODO: remove selected reference from CarouselView */}
      <CarouselView />
      {sortedReferences.length > 0 && (
        <FocusLayout trackReference={sortedReferences[0] as TrackReference} />
      )}
    </div>
  );
};

export default SpeakerLayout;

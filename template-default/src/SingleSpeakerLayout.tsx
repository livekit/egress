import { useVisualStableUpdate, VideoTrack } from '@livekit/components-react';
import { LayoutProps } from './common';

const SingleSpeakerLayout = ({ tracks: references }: LayoutProps) => {
  const sortedReferences = useVisualStableUpdate(references, 1);
  if (sortedReferences.length === 0) {
    return null;
  }
  return <VideoTrack {...sortedReferences[0]} />;
};

export default SingleSpeakerLayout;

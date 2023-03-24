import { TrackReferenceSubscribed } from '@livekit/components-core';
import { useVisualStableUpdate, VideoTrack } from '@livekit/components-react';
import { LayoutProps } from './common';

const SingleSpeakerLayout = ({ references }: LayoutProps) => {
  const sortedReferences = useVisualStableUpdate(references, 1);
  let firstTR: TrackReferenceSubscribed | undefined;
  for (const tr of sortedReferences) {
    if ('publication' in tr) {
      firstTR = tr as TrackReferenceSubscribed;
    }
  }
  if (firstTR === undefined) {
    return <div />;
  }

  return <VideoTrack trackReference={firstTR} />;
};

export default SingleSpeakerLayout;

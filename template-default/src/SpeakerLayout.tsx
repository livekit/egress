/**
 * Copyright 2023 LiveKit, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { TrackReference } from '@livekit/components-core';
import {
  CarouselLayout,
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
    return <VideoTrack trackRef={trackRef} />;
  }

  return (
    <div className="lk-focus-layout">
      <CarouselLayout tracks={remainingTracks}>
        <ParticipantTile />
      </CarouselLayout>
      <FocusLayout trackRef={mainTrack as TrackReference} />
    </div>
  );
};

export default SpeakerLayout;

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

import { TrackReference, useVisualStableUpdate, VideoTrack } from '@livekit/components-react';
import { LayoutProps } from './common';

const SingleSpeakerLayout = ({ tracks: references }: LayoutProps) => {
  const sortedReferences = useVisualStableUpdate(references, 1);
  if (sortedReferences.length === 0) {
    return null;
  }
  return <VideoTrack trackRef={sortedReferences[0] as TrackReference} />;
};

export default SingleSpeakerLayout;

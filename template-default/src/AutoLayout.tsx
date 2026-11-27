import { getTrackReferenceId, TrackReference } from '@livekit/components-core';
import { ParticipantTile, useVisualStableUpdate } from '@livekit/components-react';
import { Track } from 'livekit-client';
import {
  boxStyle,
  computeAutoLayout,
  MAX_GRID_TILES,
  MAX_SHARES,
  MAX_STRIP_TILES,
  sortCamerasBySpeaker,
  visibleTracks,
} from './autoLayoutMath';
import { LayoutProps } from './common';

/**
 * "auto" layout: picks one of six arrangements from what is in the room right now
 * and re-arranges live as people publish/unpublish cameras, start/stop screen
 * shares, or start speaking. See computeAutoLayout for the case table.
 *
 * Every tile is rendered as a keyed sibling in one container and only its
 * position changes between cases, so video elements are not remounted and the
 * CSS transition animates the switch.
 */
const AutoLayout = ({ tracks }: LayoutProps) => {
  // Muted (turned-off) cameras and muted shares are left out before ordering and
  // counting, so neither takes the main frame or stage as a placeholder; see
  // visibleTracks.
  const { shares, cams } = visibleTracks(
    tracks.filter((t) => t.source === Track.Source.ScreenShare),
    tracks.filter((t) => t.source !== Track.Source.ScreenShare),
  );

  // hooks must run unconditionally
  // Cameras use their own speaker sort so video without a source label is ranked
  // like a camera instead of always after it; see sortCamerasBySpeaker.
  const camSort = { customSortFunction: sortCamerasBySpeaker };
  // first item = current active speaker (used for the full-frame speaker in case 2)
  const camsBySpeaker = useVisualStableUpdate(cams, 1, camSort) as TrackReference[];
  // stable positions for grid / filmstrip, speakers promoted onto the visible page
  const camsGrid = useVisualStableUpdate(cams, MAX_GRID_TILES, camSort) as TrackReference[];
  // filmstrip under a share is smaller, so speakers must be promoted into its 6 slots
  const camsStrip = useVisualStableUpdate(cams, MAX_STRIP_TILES, camSort) as TrackReference[];
  // shares are split from cameras before ordering, so a share started after the
  // cameras is never stuck behind them
  const sharesStable = useVisualStableUpdate(shares, MAX_SHARES) as TrackReference[];

  let camsOrdered: TrackReference[];
  if (shares.length > 0) {
    camsOrdered = camsStrip;
  } else if (cams.length <= 2) {
    camsOrdered = camsBySpeaker;
  } else {
    camsOrdered = camsGrid;
  }

  const byId = new Map<string, TrackReference>();
  for (const t of [...sharesStable, ...camsOrdered]) {
    byId.set(getTrackReferenceId(t), t);
  }

  const { mode, boxes } = computeAutoLayout(
    sharesStable.map(getTrackReferenceId),
    camsOrdered.map(getTrackReferenceId),
  );

  // render in a stable (sorted) order so React reorders nothing; z-index handles stacking
  const ordered = [...boxes].sort((a, b) => (a.id < b.id ? -1 : 1));

  return (
    <div className={`auto-layout auto-${mode}`}>
      {ordered.map((b) => {
        const trackRef = byId.get(b.id);
        if (!trackRef) return null;
        return (
          <ParticipantTile
            key={b.id}
            trackRef={trackRef}
            className={b.inset ? 'auto-tile auto-inset' : 'auto-tile'}
            style={boxStyle(b)}
          />
        );
      })}
    </div>
  );
};

export default AutoLayout;

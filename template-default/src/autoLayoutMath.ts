// Pure geometry for the "auto" layout. No React / LiveKit imports so it can be
// unit-tested on its own and mirrored later in Go (pkg/pipeline/builder/layout.go).
//
// Boxes are computed on a virtual 1600x900 canvas and converted to percentages,
// so they scale to whatever 16:9 resolution egress records at.

export const CANVAS_W = 1600;
export const CANVAS_H = 900;
const GAP = 12;

/** Max cameras in the filmstrip under a screen share (cases 4 and 5). */
export const MAX_STRIP_TILES = 6;
/** Max cameras in the grid (case 6). Extra participants are heard but not shown. */
export const MAX_GRID_TILES = 16;
/** Max screen shares shown side by side (case 5). */
export const MAX_SHARES = 2;

export type AutoMode = 'pip' | 'grid' | 'stage';

export interface Box {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  z: number;
  /** small floating tile (rounded + border) */
  inset?: boolean;
}

/**
 * Cameras that get a tile. LiveKit's camera toggle mutes the publication rather
 * than unpublishing it, so a camera that was turned off is still subscribed and
 * would otherwise be ordered (by speaking) and counted like a live one: a muted
 * speaker could take the full frame as a placeholder while the only live camera
 * shrinks to the inset. Muted cameras are left out, so muting and unmuting
 * re-compute the arrangement. When nothing at all is live (no unmuted camera and
 * no share), the muted cameras are kept so the frame shows placeholders rather
 * than nothing, like the other layouts.
 */
export function visibleCameras<T extends { publication?: { isMuted?: boolean } }>(
  cams: T[],
  shareCount: number,
): T[] {
  const live = cams.filter((t) => !t.publication?.isMuted);
  return live.length > 0 || shareCount > 0 ? live : cams;
}

export interface AutoLayoutResult {
  mode: AutoMode;
  boxes: Box[];
}

/**
 * @param shareIds screen-share track ids, most relevant first
 * @param camIds   camera track ids, active speaker first
 *
 * Case 1   1 camera, no share            -> camera full frame
 * Case 2   2 cameras, no share           -> active speaker full, other as corner inset
 * Case 3   1 share, 0-1 camera           -> share full, camera as corner inset
 * Case 4   1 share, 2+ cameras           -> share on stage, cameras in bottom filmstrip
 * Case 5   2+ shares (any cameras)       -> shares side by side, cameras in filmstrip
 * Case 6   3+ cameras, no share          -> grid
 */
export function computeAutoLayout(shareIds: string[], camIds: string[]): AutoLayoutResult {
  const shares = shareIds.slice(0, MAX_SHARES);

  if (shares.length === 0) {
    if (camIds.length <= 2) {
      return { mode: 'pip', boxes: pip(camIds[0], camIds[1]) };
    }
    return { mode: 'grid', boxes: grid(camIds.slice(0, MAX_GRID_TILES)) };
  }

  if (shares.length === 1 && camIds.length <= 1) {
    return { mode: 'pip', boxes: pip(shares[0], camIds[0]) };
  }

  return { mode: 'stage', boxes: stage(shares, camIds.slice(0, MAX_STRIP_TILES)) };
}

function pip(main?: string, inset?: string): Box[] {
  const boxes: Box[] = [];
  if (main) {
    boxes.push({ id: main, x: 0, y: 0, w: CANVAS_W, h: CANVAS_H, z: 1 });
  }
  if (inset) {
    const w = Math.round(CANVAS_W * 0.22);
    const h = Math.round((w * 9) / 16);
    boxes.push({
      id: inset,
      x: CANVAS_W - w - Math.round(CANVAS_W * 0.025),
      y: CANVAS_H - h - Math.round(CANVAS_H * 0.04),
      w,
      h,
      z: 2,
      inset: true,
    });
  }
  return boxes;
}

/** Equal 16:9 tiles; picks the column count that gives the biggest tiles, centres every row. */
function grid(ids: string[]): Box[] {
  const n = ids.length;
  if (n === 0) return [];

  let best = { cols: 1, rows: n, w: 0, h: 0 };
  for (let cols = 1; cols <= n; cols++) {
    const rows = Math.ceil(n / cols);
    const maxW = (CANVAS_W - (cols + 1) * GAP) / cols;
    const maxH = (CANVAS_H - (rows + 1) * GAP) / rows;
    const w = Math.min(maxW, (maxH * 16) / 9);
    if (w > best.w) best = { cols, rows, w, h: (w * 9) / 16 };
  }

  const { cols, rows, w, h } = best;
  const totalH = rows * h + (rows - 1) * GAP;
  const top = (CANVAS_H - totalH) / 2;

  return ids.map((id, i) => {
    const row = Math.floor(i / cols);
    const inRow = row === rows - 1 ? n - row * cols : cols;
    const rowW = inRow * w + (inRow - 1) * GAP;
    const left = (CANVAS_W - rowW) / 2;
    const col = i - row * cols;
    return {
      id,
      x: Math.round(left + col * (w + GAP)),
      y: Math.round(top + row * (h + GAP)),
      w: Math.round(w),
      h: Math.round(h),
      z: 1,
    };
  });
}

/** Screen share(s) on a stage across the top, cameras in a centred filmstrip at the bottom. */
function stage(shareIds: string[], camIds: string[]): Box[] {
  const boxes: Box[] = [];

  // filmstrip sizing: 22% of height, shrink if too many to fit the width
  let stripH = camIds.length > 0 ? Math.round(CANVAS_H * 0.22) : 0;
  let stripW = (stripH * 16) / 9;
  const m = camIds.length;
  if (m > 0) {
    const needed = m * stripW + (m - 1) * GAP;
    const avail = CANVAS_W - 2 * GAP;
    if (needed > avail) {
      stripW = (avail - (m - 1) * GAP) / m;
      stripH = (stripW * 9) / 16;
    }
  }

  const stageTop = GAP;
  const stageH = CANVAS_H - stageTop - GAP - (m > 0 ? stripH + GAP : 0);
  const k = shareIds.length;
  const shareW = (CANVAS_W - (k + 1) * GAP) / k;
  shareIds.forEach((id, i) => {
    boxes.push({
      id,
      x: Math.round(GAP + i * (shareW + GAP)),
      y: stageTop,
      w: Math.round(shareW),
      h: Math.round(stageH),
      z: 1,
    });
  });

  if (m > 0) {
    const rowW = m * stripW + (m - 1) * GAP;
    const left = (CANVAS_W - rowW) / 2;
    const y = CANVAS_H - GAP - stripH;
    camIds.forEach((id, i) => {
      boxes.push({
        id,
        x: Math.round(left + i * (stripW + GAP)),
        y: Math.round(y),
        w: Math.round(stripW),
        h: Math.round(stripH),
        z: 1,
      });
    });
  }

  return boxes;
}

/** CSS for a box as percentages of the canvas (works at any 16:9 output size). */
export function boxStyle(b: Box) {
  return {
    position: 'absolute' as const,
    left: `${(b.x / CANVAS_W) * 100}%`,
    top: `${(b.y / CANVAS_H) * 100}%`,
    width: `${(b.w / CANVAS_W) * 100}%`,
    height: `${(b.h / CANVAS_H) * 100}%`,
    zIndex: b.z,
  };
}

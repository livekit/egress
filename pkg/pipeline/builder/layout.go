// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package builder

import (
	"sort"
	"time"

	"github.com/linkdata/deadlock"
)

type TrackSource int

const (
	TrackSourceCamera TrackSource = iota
	TrackSourceScreenShare

	LayoutGrid               = "grid"
	LayoutGridLight          = "grid-light"
	LayoutGridDark           = "grid-dark"
	LayoutSingleSpeaker      = "single-speaker"
	LayoutSingleSpeakerLight = "single-speaker-light"
	LayoutSingleSpeakerDark  = "single-speaker-dark"
	LayoutSpeaker            = "speaker"
	LayoutSpeakerLight       = "speaker-light"
	LayoutSpeakerDark        = "speaker-dark"
)

type PadLayout struct {
	TrackID string
	X, Y    int
	W, H    int
	ZOrder  uint
	Alpha   float64
}

type SpeakerInfo struct {
	Identity   string
	AudioLevel float32
	IsSpeaking bool
}

type trackInfo struct {
	TrackID     string
	Identity    string
	Source      TrackSource
	addOrder    int
	isSpeaking  bool
	audioLevel  float32
	lastSpokeAt int64
}

// gridGap is the Chrome template's --grid-gap (0.5rem at 16px base); applied
// both between cells and as outer padding on .lk-grid-layout / .lk-focus-layout.
const gridGap = 8

const minCarouselTileHeight = 130

// maxCarouselTiles mirrors CarouselLayout.tsx vertical sizing (see
// template-default/node_modules/@livekit/components-react CarouselLayout.tsx).
func maxCarouselTiles(carouselW, innerH int) int {
	tileSpan := carouselW * 6 / 10
	if tileSpan < minCarouselTileHeight {
		tileSpan = minCarouselTileHeight
	}
	n := (innerH + tileSpan/2) / tileSpan
	if n < 1 {
		n = 1
	}
	return n
}

type LayoutManager struct {
	mu            deadlock.Mutex
	layout        string
	origLayout    string
	width, height int
	tracks        []trackInfo
	displayOrder  []string
	addCounter    int
}

func NewLayoutManager(layout string, width, height int) *LayoutManager {
	return &LayoutManager{
		layout:     layout,
		origLayout: layout,
		width:      width,
		height:     height,
	}
}

func (l *LayoutManager) AddTrack(trackID, identity string, source TrackSource) []PadLayout {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.addCounter++
	l.tracks = append(l.tracks, trackInfo{
		TrackID:  trackID,
		Identity: identity,
		Source:   source,
		addOrder: l.addCounter,
	})
	l.displayOrder = append(l.displayOrder, trackID)

	if source == TrackSourceScreenShare {
		l.onScreenShareChanged()
	}

	idealOrder := l.sortTracks()
	l.displayOrder = l.stabilize(idealOrder)

	return l.recalculate()
}

func (l *LayoutManager) RemoveTrack(trackID string) []PadLayout {
	l.mu.Lock()
	defer l.mu.Unlock()

	var removedSource TrackSource
	for i, t := range l.tracks {
		if t.TrackID == trackID {
			removedSource = t.Source
			l.tracks = append(l.tracks[:i], l.tracks[i+1:]...)
			break
		}
	}
	for i, id := range l.displayOrder {
		if id == trackID {
			l.displayOrder = append(l.displayOrder[:i], l.displayOrder[i+1:]...)
			break
		}
	}

	if removedSource == TrackSourceScreenShare {
		l.onScreenShareChanged()
	}

	return l.recalculate()
}

func (l *LayoutManager) onScreenShareChanged() {
	hasScreenShare := false
	for _, t := range l.tracks {
		if t.Source == TrackSourceScreenShare {
			hasScreenShare = true
			break
		}
	}

	if hasScreenShare {
		if isGridLayout(l.origLayout) {
			l.layout = gridToSpeaker(l.origLayout)
		}
	} else {
		l.layout = l.origLayout
	}
}

func isGridLayout(layout string) bool {
	return layout == LayoutGrid || layout == LayoutGridLight || layout == LayoutGridDark
}

func gridToSpeaker(layout string) string {
	switch layout {
	case LayoutGridLight:
		return LayoutSpeakerLight
	case LayoutGridDark:
		return LayoutSpeakerDark
	default:
		return LayoutSpeaker
	}
}

func (l *LayoutManager) Layout() []PadLayout {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.recalculate()
}

func (l *LayoutManager) recalculate() []PadLayout {
	switch l.layout {
	case LayoutSpeaker, LayoutSpeakerLight, LayoutSpeakerDark:
		return l.calculateSpeakerLayout()
	case LayoutSingleSpeaker, LayoutSingleSpeakerLight, LayoutSingleSpeakerDark:
		return l.calculateSingleSpeakerLayout()
	default:
		return l.calculateGridLayout()
	}
}

// gridColumns mirrors the LiveKit components GridLayout breakpoint algorithm.
func (l *LayoutManager) gridColumns(count int) int {
	switch {
	case count <= 1:
		return 1
	case count <= 4:
		if l.width >= 560 {
			return 2
		}
		return 1
	case count <= 9:
		if l.width >= 700 {
			return 3
		}
		return 2
	case count <= 16:
		if l.width >= 960 {
			return 4
		}
		return 3
	default:
		if l.width >= 1100 {
			return 5
		}
		return 4
	}
}

func (l *LayoutManager) calculateGridLayout() []PadLayout {
	count := len(l.tracks)
	if count == 0 {
		return nil
	}

	// Single participant fills the canvas: matches Chrome's single-tile render
	// and keeps the avsync test pattern's top stripe at y=0.
	if count == 1 {
		return []PadLayout{{
			TrackID: l.tracks[0].TrackID,
			X:       0, Y: 0,
			W: l.width, H: l.height,
			ZOrder: 1, Alpha: 1.0,
		}}
	}

	cols := l.gridColumns(count)
	rows := (count + cols - 1) / cols
	innerW := l.width - 2*gridGap
	innerH := l.height - 2*gridGap
	cellW := (innerW - (cols-1)*gridGap) / cols
	cellH := (innerH - (rows-1)*gridGap) / rows

	pads := make([]PadLayout, 0, count)
	for i, t := range l.tracks {
		col := i % cols
		row := i / cols
		pads = append(pads, PadLayout{
			TrackID: t.TrackID,
			X:       gridGap + col*(cellW+gridGap),
			Y:       gridGap + row*(cellH+gridGap),
			W:       cellW,
			H:       cellH,
			ZOrder:  1,
			Alpha:   1.0,
		})
	}
	return pads
}

func (l *LayoutManager) calculateSpeakerLayout() []PadLayout {
	count := len(l.tracks)
	if count == 0 {
		return nil
	}

	ordered := l.orderedTracks()

	if count == 1 {
		return []PadLayout{{
			TrackID: ordered[0].TrackID,
			X:       0, Y: 0,
			W: l.width, H: l.height,
			ZOrder: 1, Alpha: 1.0,
		}}
	}

	// .lk-focus-layout: 1fr carousel + 5fr stage CSS grid, gridGap padding/gap.
	const totalFr = 6
	innerW := l.width - 2*gridGap
	innerH := l.height - 2*gridGap
	availableForCols := innerW - gridGap
	carouselW := availableForCols / totalFr
	stageW := availableForCols - carouselW
	stageX := gridGap + carouselW + gridGap

	// thumbH always divides by maxTiles so tile size stays stable as participants join/leave.
	maxTiles := maxCarouselTiles(carouselW, innerH)
	thumbH := (innerH - gridGap*(maxTiles-1)) / maxTiles
	visibleThumbs := maxTiles
	if count-1 < visibleThumbs {
		visibleThumbs = count - 1
	}

	pads := make([]PadLayout, 0, count)
	pads = append(pads, PadLayout{
		TrackID: ordered[0].TrackID,
		X:       stageX, Y: gridGap,
		W: stageW, H: innerH,
		ZOrder: 1, Alpha: 1.0,
	})

	for i := 1; i <= visibleThumbs; i++ {
		pads = append(pads, PadLayout{
			TrackID: ordered[i].TrackID,
			X:       gridGap,
			Y:       gridGap + (i-1)*(thumbH+gridGap),
			W:       carouselW,
			H:       thumbH,
			ZOrder:  1,
			Alpha:   1.0,
		})
	}

	for i := visibleThumbs + 1; i < count; i++ {
		pads = append(pads, PadLayout{
			TrackID: ordered[i].TrackID,
			X:       0, Y: 0,
			W: 0, H: 0,
			ZOrder: 0, Alpha: 0.0,
		})
	}

	return pads
}

func (l *LayoutManager) calculateSingleSpeakerLayout() []PadLayout {
	if len(l.tracks) == 0 {
		return nil
	}

	ordered := l.orderedTracks()
	pads := make([]PadLayout, 0, len(l.tracks))

	pads = append(pads, PadLayout{
		TrackID: ordered[0].TrackID,
		X:       0, Y: 0,
		W: l.width, H: l.height,
		ZOrder: 1, Alpha: 1.0,
	})

	for i := 1; i < len(ordered); i++ {
		pads = append(pads, PadLayout{
			TrackID: ordered[i].TrackID,
			X:       0, Y: 0,
			W: 0, H: 0,
			ZOrder: 0, Alpha: 0.0,
		})
	}

	return pads
}

func (l *LayoutManager) orderedTracks() []trackInfo {
	byID := make(map[string]trackInfo, len(l.tracks))
	for _, t := range l.tracks {
		byID[t.TrackID] = t
	}

	result := make([]trackInfo, 0, len(l.tracks))
	seen := make(map[string]bool)
	for _, id := range l.displayOrder {
		if t, ok := byID[id]; ok {
			result = append(result, t)
			seen[id] = true
		}
	}
	for _, t := range l.tracks {
		if !seen[t.TrackID] {
			result = append(result, t)
		}
	}
	return result
}

// UpdateSpeakers returns nil if the stabilized display order is unchanged.
func (l *LayoutManager) UpdateSpeakers(speakers []SpeakerInfo) []PadLayout {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixNano()

	speakerMap := make(map[string]SpeakerInfo, len(speakers))
	for _, s := range speakers {
		speakerMap[s.Identity] = s
	}

	for i := range l.tracks {
		if s, ok := speakerMap[l.tracks[i].Identity]; ok {
			l.tracks[i].isSpeaking = s.IsSpeaking
			l.tracks[i].audioLevel = s.AudioLevel
			if s.IsSpeaking {
				l.tracks[i].lastSpokeAt = now
			}
		} else {
			l.tracks[i].isSpeaking = false
			l.tracks[i].audioLevel = 0
		}
	}

	idealOrder := l.sortTracks()
	newDisplayOrder := l.stabilize(idealOrder)

	changed := false
	if len(newDisplayOrder) != len(l.displayOrder) {
		changed = true
	} else {
		for i := range newDisplayOrder {
			if newDisplayOrder[i] != l.displayOrder[i] {
				changed = true
				break
			}
		}
	}

	if !changed {
		return nil
	}

	l.displayOrder = newDisplayOrder
	return l.recalculate()
}

func (l *LayoutManager) sortTracks() []string {
	var screenShares []trackInfo
	var cameras []trackInfo

	for _, t := range l.tracks {
		if t.Source == TrackSourceScreenShare {
			screenShares = append(screenShares, t)
		} else {
			cameras = append(cameras, t)
		}
	}

	sort.SliceStable(screenShares, func(i, j int) bool {
		return screenShares[i].addOrder < screenShares[j].addOrder
	})

	sort.SliceStable(cameras, func(i, j int) bool {
		a, b := cameras[i], cameras[j]
		if a.isSpeaking && b.isSpeaking {
			return a.audioLevel > b.audioLevel
		}
		if a.isSpeaking != b.isSpeaking {
			return a.isSpeaking
		}
		if a.lastSpokeAt != b.lastSpokeAt {
			return a.lastSpokeAt > b.lastSpokeAt
		}
		return a.addOrder < b.addOrder
	})

	result := make([]string, 0, len(l.tracks))
	for _, t := range screenShares {
		result = append(result, t.TrackID)
	}
	for _, t := range cameras {
		result = append(result, t.TrackID)
	}
	return result
}

func (l *LayoutManager) stabilize(idealOrder []string) []string {
	if len(l.displayOrder) == 0 {
		return idealOrder
	}

	maxOnPage := l.maxItemsOnPage()

	current := l.refreshOrder(idealOrder)

	currentSet := make(map[string]bool, len(current))
	for _, id := range current {
		currentSet[id] = true
	}
	for _, id := range idealOrder {
		if !currentSet[id] {
			current = append(current, id)
		}
	}

	idealSet := make(map[string]bool, len(idealOrder))
	for _, id := range idealOrder {
		idealSet[id] = true
	}
	filtered := current[:0]
	for _, id := range current {
		if idealSet[id] {
			filtered = append(filtered, id)
		}
	}
	current = filtered

	if maxOnPage >= len(current) {
		return idealOrder
	}

	visibleIdeal := idealOrder
	if len(visibleIdeal) > maxOnPage {
		visibleIdeal = visibleIdeal[:maxOnPage]
	}

	visibleCurrent := current
	if len(visibleCurrent) > maxOnPage {
		visibleCurrent = visibleCurrent[:maxOnPage]
	}
	visibleSet := make(map[string]bool, len(visibleCurrent))
	for _, id := range visibleCurrent {
		visibleSet[id] = true
	}

	for _, shouldBeVisible := range visibleIdeal {
		if visibleSet[shouldBeVisible] {
			continue
		}
		for i := 0; i < maxOnPage && i < len(current); i++ {
			shouldBeHere := false
			for _, vid := range visibleIdeal {
				if current[i] == vid {
					shouldBeHere = true
					break
				}
			}
			if !shouldBeHere {
				for j := range current {
					if current[j] == shouldBeVisible {
						current[i], current[j] = current[j], current[i]
						break
					}
				}
				break
			}
		}
	}

	return current
}

func (l *LayoutManager) refreshOrder(idealOrder []string) []string {
	idealSet := make(map[string]bool, len(idealOrder))
	for _, id := range idealOrder {
		idealSet[id] = true
	}
	result := make([]string, 0, len(l.displayOrder))
	for _, id := range l.displayOrder {
		if idealSet[id] {
			result = append(result, id)
		}
	}
	return result
}

func (l *LayoutManager) maxItemsOnPage() int {
	switch l.layout {
	case LayoutSingleSpeaker, LayoutSingleSpeakerLight, LayoutSingleSpeakerDark:
		return 1
	case LayoutSpeaker, LayoutSpeakerLight, LayoutSpeakerDark:
		innerW := l.width - 2*gridGap
		innerH := l.height - 2*gridGap
		carouselW := (innerW - gridGap) / 6
		return 1 + maxCarouselTiles(carouselW, innerH)
	default:
		count := len(l.tracks)
		cols := l.gridColumns(count)
		rows := (count + cols - 1) / cols
		return cols * rows
	}
}

// Copyright 2024 LiveKit, Inc.
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
)

// TrackSource identifies the source type of a track.
type TrackSource int

const (
	TrackSourceCamera TrackSource = iota
	TrackSourceScreenShare
)

// PadLayout describes the compositor pad geometry for a single track.
type PadLayout struct {
	TrackID string
	X, Y    int
	W, H    int
	ZOrder  uint
	Alpha   float64
}

// SpeakerInfo carries speaker state from the SDK callback.
type SpeakerInfo struct {
	Identity   string
	AudioLevel float32
	IsSpeaking bool
}

// trackInfo holds internal state for a tracked participant video.
type trackInfo struct {
	TrackID     string
	Identity    string
	Source      TrackSource
	addOrder    int
	isSpeaking  bool
	audioLevel  float32
	lastSpokeAt int64
}

type gridDef struct {
	cols, rows  int
	orientation int
}

const (
	orientationAny       = 0
	orientationLandscape = 1
	orientationPortrait  = 2
)

var gridDefs = []gridDef{
	{1, 1, orientationAny},
	{1, 2, orientationPortrait},
	{2, 1, orientationLandscape},
	{2, 2, orientationAny},
	{3, 3, orientationAny},
	{4, 4, orientationAny},
	{5, 5, orientationAny},
}

// LayoutManager computes compositor pad geometries for a set of participant tracks.
type LayoutManager struct {
	layout       string
	origLayout   string
	width, height int
	tracks       []trackInfo
	displayOrder []string
	addCounter   int
}

// NewLayoutManager creates a new LayoutManager with the given layout name and canvas dimensions.
func NewLayoutManager(layout string, width, height int) *LayoutManager {
	return &LayoutManager{
		layout:     layout,
		origLayout: layout,
		width:      width,
		height:     height,
	}
}

// AddTrack registers a new track and returns the updated list of pad layouts.
func (l *LayoutManager) AddTrack(trackID, identity string, source TrackSource) []PadLayout {
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

// RemoveTrack unregisters a track and returns the updated list of pad layouts.
func (l *LayoutManager) RemoveTrack(trackID string) []PadLayout {
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
	return layout == "grid" || layout == "grid-light" || layout == "grid-dark"
}

func gridToSpeaker(layout string) string {
	switch layout {
	case "grid-light":
		return "speaker-light"
	case "grid-dark":
		return "speaker-dark"
	default:
		return "speaker"
	}
}

func (l *LayoutManager) recalculate() []PadLayout {
	switch l.layout {
	case "speaker", "speaker-light", "speaker-dark":
		return l.calculateSpeakerLayout()
	case "single-speaker", "single-speaker-light", "single-speaker-dark":
		return l.calculateSingleSpeakerLayout()
	default:
		return l.calculateGridLayout()
	}
}

// selectGrid picks the smallest grid definition that fits count tiles,
// respecting the canvas orientation.
func (l *LayoutManager) selectGrid(count int) (cols, rows int) {
	isLandscape := l.width > l.height

	for _, g := range gridDefs {
		if g.orientation == orientationPortrait && isLandscape {
			continue
		}
		if g.orientation == orientationLandscape && !isLandscape {
			continue
		}
		if g.cols*g.rows >= count {
			return g.cols, g.rows
		}
	}
	last := gridDefs[len(gridDefs)-1]
	return last.cols, last.rows
}

func (l *LayoutManager) calculateGridLayout() []PadLayout {
	count := len(l.tracks)
	if count == 0 {
		return nil
	}

	cols, rows := l.selectGrid(count)
	tileW := l.width / cols
	tileH := l.height / rows

	pads := make([]PadLayout, 0, count)
	for i, t := range l.tracks {
		col := i % cols
		row := i / cols
		pads = append(pads, PadLayout{
			TrackID: t.TrackID,
			X:       col * tileW,
			Y:       row * tileH,
			W:       tileW,
			H:       tileH,
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
			X: 0, Y: 0,
			W: l.width, H: l.height,
			ZOrder: 1, Alpha: 1.0,
		}}
	}

	focusW := l.width * 3 / 4
	carouselW := l.width - focusW
	carouselX := focusW

	maxCarousel := 3
	if count-1 < maxCarousel {
		maxCarousel = count - 1
	}
	carouselH := l.height / maxCarousel

	pads := make([]PadLayout, 0, count)

	pads = append(pads, PadLayout{
		TrackID: ordered[0].TrackID,
		X: 0, Y: 0,
		W: focusW, H: l.height,
		ZOrder: 1, Alpha: 1.0,
	})

	for i := 1; i <= maxCarousel; i++ {
		pads = append(pads, PadLayout{
			TrackID: ordered[i].TrackID,
			X:       carouselX,
			Y:       (i - 1) * carouselH,
			W:       carouselW,
			H:       carouselH,
			ZOrder:  1,
			Alpha:   1.0,
		})
	}

	for i := maxCarousel + 1; i < count; i++ {
		pads = append(pads, PadLayout{
			TrackID: ordered[i].TrackID,
			X: 0, Y: 0,
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
		X: 0, Y: 0,
		W: l.width, H: l.height,
		ZOrder: 1, Alpha: 1.0,
	})

	for i := 1; i < len(ordered); i++ {
		pads = append(pads, PadLayout{
			TrackID: ordered[i].TrackID,
			X: 0, Y: 0,
			W: 0, H: 0,
			ZOrder: 0, Alpha: 0.0,
		})
	}

	return pads
}

// orderedTracks returns tracks in displayOrder sequence.
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

// UpdateSpeakers updates speaker state and re-sorts tracks.
// Returns nil if the stabilized display order didn't change.
func (l *LayoutManager) UpdateSpeakers(speakers []SpeakerInfo) []PadLayout {
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

// sortTracks returns track IDs in ideal sorted order.
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

// stabilize applies the visual stable update algorithm.
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
	case "single-speaker", "single-speaker-light", "single-speaker-dark":
		return 1
	case "speaker", "speaker-light", "speaker-dark":
		return 4
	default:
		cols, rows := l.selectGrid(len(l.tracks))
		return cols * rows
	}
}

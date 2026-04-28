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

package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// DimValue represents a single value within a dimension, annotated with
// compatibility tags (e.g. "audio", "video", "track", "composite").
type DimValue struct {
	Dimension string
	Name      string
	Tags      []string
}

// GeneratedTest is a combination of dimension values that are mutually
// compatible. Name is a deterministic slash-joined label built from the
// sorted value names.
type GeneratedTest struct {
	Name   string
	Values []DimValue
}

// tagsCompatible reports whether two tag sets are free of contradictions.
//
// Contradiction rules:
//   - "track" and "composite" contradict each other.
//   - "audio" (without "audio+video") and "video" (without "audio+video")
//     contradict each other. "audio+video" is compatible with both "audio"
//     and "video".
func tagsCompatible(a, b []string) bool {
	hasTag := func(tags []string, tag string) bool {
		return slices.Contains(tags, tag)
	}

	// track vs composite
	if (hasTag(a, "track") && hasTag(b, "composite")) ||
		(hasTag(a, "composite") && hasTag(b, "track")) {
		return false
	}

	// audio vs video (but audio+video is compatible with both)
	aAudio := hasTag(a, "audio")
	aVideo := hasTag(a, "video")
	aAV := hasTag(a, "audio+video")
	bAudio := hasTag(b, "audio")
	bVideo := hasTag(b, "video")
	bAV := hasTag(b, "audio+video")

	// "audio" only (no audio+video) vs "video" only (no audio+video)
	if aAudio && !aAV && bVideo && !bAV {
		return false
	}
	if aVideo && !aAV && bAudio && !bAV {
		return false
	}

	return true
}

// compatible reports whether candidate can be added to the existing set of
// values without violating any constraints. The rules are:
//   - Only one value per dimension.
//   - Tags must not contradict (checked pairwise).
func compatible(existing []DimValue, candidate DimValue) bool {
	for _, v := range existing {
		if v.Dimension == candidate.Dimension {
			return false
		}
		if !tagsCompatible(v.Tags, candidate.Tags) {
			return false
		}
	}
	return true
}

// Cover implements a greedy set-cover algorithm. Given a list of dimensions
// (each a slice of possible DimValue), it returns a minimal set of
// GeneratedTest values such that every DimValue appears in at least one test.
func Cover(dimensions [][]DimValue) []GeneratedTest {
	// Build the full set of uncovered values.
	type key struct{ dim, name string }
	uncovered := make(map[key]DimValue)
	for _, dim := range dimensions {
		for _, v := range dim {
			uncovered[key{v.Dimension, v.Name}] = v
		}
	}

	// Deterministic iteration order: sort by dimension then name.
	sortedKeys := func(m map[key]DimValue) []key {
		keys := make([]key, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].dim != keys[j].dim {
				return keys[i].dim < keys[j].dim
			}
			return keys[i].name < keys[j].name
		})
		return keys
	}

	var tests []GeneratedTest

	for len(uncovered) > 0 {
		// For each uncovered value, try building a combination starting from
		// it, greedily adding other compatible uncovered values.
		var bestCombo []DimValue
		bestCount := 0

		for _, startKey := range sortedKeys(uncovered) {
			combo := []DimValue{uncovered[startKey]}

			// Try to extend with other uncovered values.
			for _, k := range sortedKeys(uncovered) {
				if k == startKey {
					continue
				}
				v := uncovered[k]
				if compatible(combo, v) {
					combo = append(combo, v)
				}
			}

			if len(combo) > bestCount {
				bestCount = len(combo)
				bestCombo = combo
			}
		}

		// Build deterministic test name from sorted value names.
		names := make([]string, len(bestCombo))
		for i, v := range bestCombo {
			names[i] = v.Name
		}
		sort.Strings(names)

		test := GeneratedTest{
			Name:   strings.Join(names, "/"),
			Values: bestCombo,
		}
		tests = append(tests, test)

		// Mark covered.
		for _, v := range bestCombo {
			delete(uncovered, key{v.Dimension, v.Name})
		}
	}

	return tests
}

// CoverageReport returns a human-readable report listing which values each
// test covers and the total count of tests.
func CoverageReport(tests []GeneratedTest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Coverage Report: %d tests\n", len(tests))
	fmt.Fprintln(&b, strings.Repeat("-", 40))
	for i, t := range tests {
		fmt.Fprintf(&b, "\nTest %d: %s\n", i+1, t.Name)
		for _, v := range t.Values {
			fmt.Fprintf(&b, "  [%s] %s", v.Dimension, v.Name)
			if len(v.Tags) > 0 {
				fmt.Fprintf(&b, " (tags: %s)", strings.Join(v.Tags, ", "))
			}
			fmt.Fprintln(&b)
		}
	}
	return b.String()
}

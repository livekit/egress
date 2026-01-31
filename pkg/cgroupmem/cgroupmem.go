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

package cgroupmem

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Stats contains cgroup memory statistics.
type Stats struct {
	Version           string // "v2" or "v1"
	TotalBytes        uint64 // v2: memory.current, v1: memory.usage_in_bytes
	InactiveFileBytes uint64 // v2: inactive_file, v1: total_inactive_file (or inactive_file)
	WorkingSetBytes   uint64 // clamp(total - inactive_file, >=0)
	TotalPath         string // path to total memory file (for debug)
	StatPath          string // path to stat file (for debug)
}

var (
	ErrCgroupNotAvailable = errors.New("cgroup memory not available")
	ErrKeyNotFound        = errors.New("key not found in stat file")
)

// rootFS returns the default filesystem rooted at /.
func rootFS() fs.FS {
	return os.DirFS("/")
}

// toFSPath converts an absolute path to an fs.FS relative path (strips leading /).
func toFSPath(absPath string) string {
	return strings.TrimPrefix(absPath, "/")
}

// joinCgroupPath safely joins mountpoint, cgroup path, and filename.
// Handles the case where cgroupPath starts with "/" which would otherwise
// cause filepath.Join to drop the mountpoint.
func joinCgroupPath(mountpoint, cgroupPath, filename string) string {
	return filepath.Join(mountpoint, strings.TrimPrefix(cgroupPath, "/"), filename)
}

// csvContains checks if a comma-separated list contains the given token (exact match).
func csvContains(csv, token string) bool {
	for _, item := range strings.Split(csv, ",") {
		if strings.TrimSpace(item) == token {
			return true
		}
	}
	return false
}

// Reader caches the detected cgroup version and paths for efficient repeated reads.
// It is safe for concurrent use after initialization.
type Reader struct {
	fsys      fs.FS
	once      sync.Once
	version   string // "v2", "v1", or "" if unavailable
	totalPath string
	statPath  string
	statKey   string // "inactive_file" for v2, "total_inactive_file" or "inactive_file" for v1
}

// NewReader creates a new Reader with the default filesystem (rooted at /).
func NewReader() *Reader {
	return &Reader{fsys: rootFS()}
}

// NewReaderWithFS creates a new Reader with a custom fs.FS (for testing).
func NewReaderWithFS(fsys fs.FS) *Reader {
	return &Reader{fsys: fsys}
}

// Read reads cgroup memory statistics, caching the detected version and paths.
// Safe for concurrent use.
func (r *Reader) Read() (Stats, error) {
	r.once.Do(r.detectVersion)

	if r.version == "" {
		return Stats{}, ErrCgroupNotAvailable
	}

	return r.readCached()
}

// detectVersion probes for cgroup v2 then v1 and caches the working configuration.
func (r *Reader) detectVersion() {
	// Try v2 first
	if stats, err := readV2(r.fsys); err == nil {
		r.version = "v2"
		r.totalPath = stats.TotalPath
		r.statPath = stats.StatPath
		r.statKey = "inactive_file"
		return
	}

	// Try v1
	if stats, err := readV1(r.fsys); err == nil {
		r.version = "v1"
		r.totalPath = stats.TotalPath
		r.statPath = stats.StatPath
		// Determine which key worked for v1
		if _, err := readStatValue(r.fsys, stats.StatPath, "total_inactive_file"); err == nil {
			r.statKey = "total_inactive_file"
		} else {
			r.statKey = "inactive_file"
		}
		return
	}

	r.version = ""
}

// readCached reads using the cached paths (faster than re-detecting each time).
func (r *Reader) readCached() (Stats, error) {
	totalBytes, err := readUint64File(r.fsys, r.totalPath)
	if err != nil {
		return Stats{}, err
	}

	inactiveFile, err := readStatValue(r.fsys, r.statPath, r.statKey)
	if err != nil {
		return Stats{}, err
	}

	workingSet := totalBytes
	if inactiveFile < totalBytes {
		workingSet = totalBytes - inactiveFile
	}

	return Stats{
		Version:           r.version,
		TotalBytes:        totalBytes,
		InactiveFileBytes: inactiveFile,
		WorkingSetBytes:   workingSet,
		TotalPath:         r.totalPath,
		StatPath:          r.statPath,
	}, nil
}

// Read reads cgroup memory statistics for the current process.
// This is a convenience function that creates a new Reader each time.
// For repeated reads, use NewReader() and call Read() on the same instance.
func Read() (Stats, error) {
	return ReadWithFS(rootFS())
}

// ReadWithFS reads cgroup memory statistics using a custom fs.FS.
// This is useful for testing. For repeated reads, use NewReaderWithFS().
func ReadWithFS(fsys fs.FS) (Stats, error) {
	// Try v2 first, then fall back to v1
	stats, err := readV2(fsys)
	if err == nil {
		return stats, nil
	}

	stats, err = readV1(fsys)
	if err == nil {
		return stats, nil
	}

	return Stats{}, ErrCgroupNotAvailable
}

// readV2 reads cgroup v2 memory stats.
func readV2(fsys fs.FS) (Stats, error) {
	cgroupPath, err := getCgroupPathV2(fsys)
	if err != nil {
		cgroupPath = "" // try default fallback
	}

	// Build paths to memory files
	var totalPath, statPath string
	if cgroupPath != "" {
		mountpoint, err := getCgroupMountpointV2(fsys)
		if err != nil {
			mountpoint = "/sys/fs/cgroup"
		}
		totalPath = joinCgroupPath(mountpoint, cgroupPath, "memory.current")
		statPath = joinCgroupPath(mountpoint, cgroupPath, "memory.stat")
	} else {
		// Fallback paths
		totalPath = "/sys/fs/cgroup/memory.current"
		statPath = "/sys/fs/cgroup/memory.stat"
	}

	// Read total memory
	totalBytes, err := readUint64File(fsys, totalPath)
	if err != nil {
		return Stats{}, err
	}

	// Read inactive_file from memory.stat (default to 0 if not found)
	inactiveFile, err := readStatValue(fsys, statPath, "inactive_file")
	if err != nil && !errors.Is(err, ErrKeyNotFound) {
		return Stats{}, err
	}

	workingSet := totalBytes
	if inactiveFile < totalBytes {
		workingSet = totalBytes - inactiveFile
	}

	return Stats{
		Version:           "v2",
		TotalBytes:        totalBytes,
		InactiveFileBytes: inactiveFile,
		WorkingSetBytes:   workingSet,
		TotalPath:         totalPath,
		StatPath:          statPath,
	}, nil
}

// readV1 reads cgroup v1 memory stats.
func readV1(fsys fs.FS) (Stats, error) {
	cgroupPath, err := getCgroupPathV1(fsys)
	if err != nil {
		cgroupPath = "" // try default fallback
	}

	// Build paths to memory files
	var totalPath, statPath string
	if cgroupPath != "" {
		mountpoint, err := getCgroupMountpointV1(fsys)
		if err != nil {
			mountpoint = "/sys/fs/cgroup/memory"
		}
		totalPath = joinCgroupPath(mountpoint, cgroupPath, "memory.usage_in_bytes")
		statPath = joinCgroupPath(mountpoint, cgroupPath, "memory.stat")
	} else {
		// Fallback paths
		totalPath = "/sys/fs/cgroup/memory/memory.usage_in_bytes"
		statPath = "/sys/fs/cgroup/memory/memory.stat"
	}

	// Read total memory
	totalBytes, err := readUint64File(fsys, totalPath)
	if err != nil {
		return Stats{}, err
	}

	// Read inactive_file from memory.stat (try total_inactive_file first, then inactive_file)
	// Default to 0 if neither is found (some environments may not expose this)
	inactiveFile, err := readStatValue(fsys, statPath, "total_inactive_file")
	if err != nil {
		if !errors.Is(err, ErrKeyNotFound) {
			return Stats{}, err
		}
		inactiveFile, err = readStatValue(fsys, statPath, "inactive_file")
		if err != nil && !errors.Is(err, ErrKeyNotFound) {
			return Stats{}, err
		}
	}

	workingSet := totalBytes
	if inactiveFile < totalBytes {
		workingSet = totalBytes - inactiveFile
	}

	return Stats{
		Version:           "v1",
		TotalBytes:        totalBytes,
		InactiveFileBytes: inactiveFile,
		WorkingSetBytes:   workingSet,
		TotalPath:         totalPath,
		StatPath:          statPath,
	}, nil
}

// getCgroupPathV2 parses /proc/self/cgroup to find the cgroup path for v2.
func getCgroupPathV2(fsys fs.FS) (string, error) {
	data, err := fs.ReadFile(fsys, toFSPath("/proc/self/cgroup"))
	if err != nil {
		return "", err
	}

	// v2 format: "0::/path"
	for _, line := range strings.Split(string(data), "\n") {
		if path, found := strings.CutPrefix(line, "0::"); found {
			return strings.TrimSpace(path), nil
		}
	}

	return "", errors.New("cgroup v2 path not found")
}

// getCgroupPathV1 parses /proc/self/cgroup to find the memory cgroup path for v1.
func getCgroupPathV1(fsys fs.FS) (string, error) {
	data, err := fs.ReadFile(fsys, toFSPath("/proc/self/cgroup"))
	if err != nil {
		return "", err
	}

	// v1 format: "N:memory:/path" or "N:memory,cpu,etc:/path"
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		controllers := parts[1]
		if csvContains(controllers, "memory") {
			return strings.TrimSpace(parts[2]), nil
		}
	}

	return "", errors.New("cgroup v1 memory path not found")
}

// getCgroupMountpointV2 parses /proc/self/mountinfo to find the cgroup2 mountpoint.
func getCgroupMountpointV2(fsys fs.FS) (string, error) {
	f, err := fsys.Open(toFSPath("/proc/self/mountinfo"))
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// mountinfo format: "ID PARENT_ID MAJOR:MINOR ROOT MOUNTPOINT OPTIONS - FSTYPE SOURCE SUPOPTS"
		// We need MOUNTPOINT (field 4, 0-indexed) and FSTYPE (after "-")
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// Find the separator "-"
		sepIdx := -1
		for i, f := range fields {
			if f == "-" {
				sepIdx = i
				break
			}
		}
		if sepIdx < 0 || sepIdx+1 >= len(fields) {
			continue
		}

		fstype := fields[sepIdx+1]
		if fstype == "cgroup2" {
			return fields[4], nil
		}
	}

	return "", errors.New("cgroup2 mountpoint not found")
}

// getCgroupMountpointV1 parses /proc/self/mountinfo to find the cgroup v1 memory mountpoint.
func getCgroupMountpointV1(fsys fs.FS) (string, error) {
	f, err := fsys.Open(toFSPath("/proc/self/mountinfo"))
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// Find the separator "-"
		sepIdx := -1
		for i, f := range fields {
			if f == "-" {
				sepIdx = i
				break
			}
		}
		if sepIdx < 0 || sepIdx+1 >= len(fields) {
			continue
		}

		fstype := fields[sepIdx+1]
		if fstype != "cgroup" {
			continue
		}

		// Check super options for memory controller
		if sepIdx+3 < len(fields) {
			superOpts := fields[sepIdx+3]
			if csvContains(superOpts, "memory") {
				return fields[4], nil
			}
		}

		// Also check if mountpoint ends with /memory (e.g., /sys/fs/cgroup/memory)
		mountpoint := fields[4]
		if strings.HasSuffix(mountpoint, "/memory") {
			return mountpoint, nil
		}
	}

	return "", errors.New("cgroup v1 memory mountpoint not found")
}

// readUint64File reads a file containing a single uint64 value.
func readUint64File(fsys fs.FS, path string) (uint64, error) {
	data, err := fs.ReadFile(fsys, toFSPath(path))
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return val, nil
}

// readStatValue reads a specific key from a memory.stat file.
// The file format is "key value" per line.
func readStatValue(fsys fs.FS, path, key string) (uint64, error) {
	f, err := fsys.Open(toFSPath(path))
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == key {
			val, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse %s key %s: %w", path, key, err)
			}
			return val, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan %s: %w", path, err)
	}

	return 0, ErrKeyNotFound
}

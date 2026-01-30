// Copyright 2023 LiveKit, Inc.
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
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestReadV2(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("0::/user.slice/user-1000.slice/session-1.scope\n"),
		},
		"proc/self/mountinfo": &fstest.MapFile{
			Data: []byte("25 24 0:22 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw,nsdelegate\n"),
		},
		"sys/fs/cgroup/user.slice/user-1000.slice/session-1.scope/memory.current": &fstest.MapFile{
			Data: []byte("1073741824\n"),
		},
		"sys/fs/cgroup/user.slice/user-1000.slice/session-1.scope/memory.stat": &fstest.MapFile{
			Data: []byte("anon 536870912\nfile 268435456\nkernel 134217728\ninactive_file 134217728\nactive_file 134217728\n"),
		},
	}

	stats, err := ReadWithFS(fsys)
	require.NoError(t, err)
	require.Equal(t, "v2", stats.Version)
	require.Equal(t, uint64(1073741824), stats.TotalBytes)       // 1 GiB
	require.Equal(t, uint64(134217728), stats.InactiveFileBytes) // 128 MiB
	require.Equal(t, uint64(939524096), stats.WorkingSetBytes)   // 1 GiB - 128 MiB
}

func TestReadV2Fallback(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte(""),
		},
		"sys/fs/cgroup/memory.current": &fstest.MapFile{
			Data: []byte("2147483648\n"),
		},
		"sys/fs/cgroup/memory.stat": &fstest.MapFile{
			Data: []byte("inactive_file 268435456\n"),
		},
	}

	stats, err := ReadWithFS(fsys)
	require.NoError(t, err)
	require.Equal(t, "v2", stats.Version)
	require.Equal(t, uint64(2147483648), stats.TotalBytes)
	require.Equal(t, uint64(268435456), stats.InactiveFileBytes)
	require.Equal(t, uint64(1879048192), stats.WorkingSetBytes)
}

func TestReadV1(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("12:pids:/docker/abc123\n11:memory:/docker/abc123\n10:cpu,cpuacct:/docker/abc123\n"),
		},
		"proc/self/mountinfo": &fstest.MapFile{
			Data: []byte("30 25 0:26 / /sys/fs/cgroup/memory rw,nosuid,nodev,noexec,relatime shared:11 - cgroup cgroup rw,memory\n"),
		},
		"sys/fs/cgroup/memory/docker/abc123/memory.usage_in_bytes": &fstest.MapFile{
			Data: []byte("3221225472\n"),
		},
		"sys/fs/cgroup/memory/docker/abc123/memory.stat": &fstest.MapFile{
			Data: []byte("cache 1073741824\nrss 2147483648\ntotal_inactive_file 536870912\ntotal_active_file 536870912\n"),
		},
	}

	stats, err := ReadWithFS(fsys)
	require.NoError(t, err)
	require.Equal(t, "v1", stats.Version)
	require.Equal(t, uint64(3221225472), stats.TotalBytes)       // 3 GiB
	require.Equal(t, uint64(536870912), stats.InactiveFileBytes) // 512 MiB
	require.Equal(t, uint64(2684354560), stats.WorkingSetBytes)  // 3 GiB - 512 MiB
}

func TestReadV1FallbackToInactiveFile(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("11:memory:/docker/abc123\n"),
		},
		"proc/self/mountinfo": &fstest.MapFile{
			Data: []byte("30 25 0:26 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory\n"),
		},
		"sys/fs/cgroup/memory/docker/abc123/memory.usage_in_bytes": &fstest.MapFile{
			Data: []byte("1073741824\n"),
		},
		"sys/fs/cgroup/memory/docker/abc123/memory.stat": &fstest.MapFile{
			Data: []byte("cache 536870912\nrss 536870912\ninactive_file 268435456\n"),
		},
	}

	stats, err := ReadWithFS(fsys)
	require.NoError(t, err)
	require.Equal(t, "v1", stats.Version)
	require.Equal(t, uint64(268435456), stats.InactiveFileBytes)
}

func TestReadNotAvailable(t *testing.T) {
	fsys := fstest.MapFS{}

	_, err := ReadWithFS(fsys)
	require.ErrorIs(t, err, ErrCgroupNotAvailable)
}

func TestWorkingSetClamp(t *testing.T) {
	// Scenario where inactive_file > total (shouldn't happen but defensive)
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("0::/\n"),
		},
		"sys/fs/cgroup/memory.current": &fstest.MapFile{
			Data: []byte("100\n"),
		},
		"sys/fs/cgroup/memory.stat": &fstest.MapFile{
			Data: []byte("inactive_file 200\n"),
		},
	}

	stats, err := ReadWithFS(fsys)
	require.NoError(t, err)
	require.Equal(t, uint64(100), stats.TotalBytes)
	require.Equal(t, uint64(200), stats.InactiveFileBytes)
	require.Equal(t, uint64(100), stats.WorkingSetBytes) // Clamped to total, not negative
}

func TestParseProcSelfCgroupV2(t *testing.T) {
	testCases := []struct {
		name     string
		content  string
		expected string
		hasError bool
	}{
		{
			name:     "standard v2",
			content:  "0::/user.slice/user-1000.slice/session-1.scope\n",
			expected: "/user.slice/user-1000.slice/session-1.scope",
		},
		{
			name:     "root cgroup",
			content:  "0::/\n",
			expected: "/",
		},
		{
			name:     "docker container",
			content:  "0::/docker/abc123def456\n",
			expected: "/docker/abc123def456",
		},
		{
			name:     "no v2 entry",
			content:  "1:memory:/docker/abc\n2:cpu:/docker/abc\n",
			hasError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"proc/self/cgroup": &fstest.MapFile{Data: []byte(tc.content)},
			}

			path, err := getCgroupPathV2(fsys)
			if tc.hasError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, path)
			}
		})
	}
}

func TestParseProcSelfCgroupV1(t *testing.T) {
	testCases := []struct {
		name     string
		content  string
		expected string
		hasError bool
	}{
		{
			name:     "memory controller only",
			content:  "5:memory:/docker/abc123\n",
			expected: "/docker/abc123",
		},
		{
			name:     "memory with other controllers",
			content:  "12:pids:/docker/abc123\n11:memory:/docker/abc123\n10:cpu,cpuacct:/docker/abc123\n",
			expected: "/docker/abc123",
		},
		{
			name:     "memory in combo",
			content:  "3:memory,cpu:/container\n",
			expected: "/container",
		},
		{
			name:     "no memory controller",
			content:  "1:cpu:/docker/abc\n2:pids:/docker/abc\n",
			hasError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"proc/self/cgroup": &fstest.MapFile{Data: []byte(tc.content)},
			}

			path, err := getCgroupPathV1(fsys)
			if tc.hasError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, path)
			}
		})
	}
}

func TestParseMountInfo(t *testing.T) {
	mountinfo := "25 0 0:22 / / rw,relatime shared:1 - ext4 /dev/sda1 rw\n" +
		"30 25 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw,nsdelegate\n" +
		"31 25 0:27 / /sys/fs/cgroup/memory rw,nosuid,nodev,noexec,relatime shared:10 - cgroup cgroup rw,memory\n"

	t.Run("v2 mountpoint", func(t *testing.T) {
		fsys := fstest.MapFS{
			"proc/self/mountinfo": &fstest.MapFile{Data: []byte(mountinfo)},
		}

		mp, err := getCgroupMountpointV2(fsys)
		require.NoError(t, err)
		require.Equal(t, "/sys/fs/cgroup", mp)
	})

	t.Run("v1 memory mountpoint", func(t *testing.T) {
		fsys := fstest.MapFS{
			"proc/self/mountinfo": &fstest.MapFile{Data: []byte(mountinfo)},
		}

		mp, err := getCgroupMountpointV1(fsys)
		require.NoError(t, err)
		require.Equal(t, "/sys/fs/cgroup/memory", mp)
	})
}

func TestParseMemoryStat(t *testing.T) {
	statContent := "anon 536870912\nfile 268435456\nkernel 134217728\ninactive_file 134217728\nactive_file 134217728\nslab 67108864\n"

	fsys := fstest.MapFS{
		"test/memory.stat": &fstest.MapFile{Data: []byte(statContent)},
	}

	value, err := readStatValue(fsys, "/test/memory.stat", "inactive_file")
	require.NoError(t, err)
	require.Equal(t, uint64(134217728), value)

	value, err = readStatValue(fsys, "/test/memory.stat", "anon")
	require.NoError(t, err)
	require.Equal(t, uint64(536870912), value)

	_, err = readStatValue(fsys, "/test/memory.stat", "nonexistent")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestReadUint64File(t *testing.T) {
	fsys := fstest.MapFS{
		"test/value": &fstest.MapFile{Data: []byte("12345678901234\n")},
		"test/bad":   &fstest.MapFile{Data: []byte("not a number\n")},
		"test/empty": &fstest.MapFile{Data: []byte("")},
	}

	val, err := readUint64File(fsys, "/test/value")
	require.NoError(t, err)
	require.Equal(t, uint64(12345678901234), val)

	_, err = readUint64File(fsys, "/test/bad")
	require.Error(t, err)

	_, err = readUint64File(fsys, "/test/missing")
	require.Error(t, err)
}

func TestKubernetesLikeCgroupPaths(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("0::/kubepods/besteffort/pod123/container456\n"),
		},
		"proc/self/mountinfo": &fstest.MapFile{
			Data: []byte("30 25 0:26 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw,nsdelegate\n"),
		},
		"sys/fs/cgroup/kubepods/besteffort/pod123/container456/memory.current": &fstest.MapFile{
			Data: []byte("4294967296\n"),
		},
		"sys/fs/cgroup/kubepods/besteffort/pod123/container456/memory.stat": &fstest.MapFile{
			Data: []byte("inactive_file 1073741824\n"),
		},
	}

	stats, err := ReadWithFS(fsys)
	require.NoError(t, err)
	require.Equal(t, "v2", stats.Version)
	require.Equal(t, uint64(4294967296), stats.TotalBytes)        // 4 GiB
	require.Equal(t, uint64(1073741824), stats.InactiveFileBytes) // 1 GiB
	require.Equal(t, uint64(3221225472), stats.WorkingSetBytes)   // 3 GiB
	require.True(t, strings.Contains(stats.TotalPath, "kubepods"))
}

func TestCachedReader(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("0::/test\n"),
		},
		"proc/self/mountinfo": &fstest.MapFile{
			Data: []byte("30 25 0:26 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw,nsdelegate\n"),
		},
		"sys/fs/cgroup/test/memory.current": &fstest.MapFile{
			Data: []byte("1073741824\n"),
		},
		"sys/fs/cgroup/test/memory.stat": &fstest.MapFile{
			Data: []byte("inactive_file 134217728\n"),
		},
	}

	// Create cached reader
	r := NewReaderWithFS(fsys)

	// First read - should detect version
	stats, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, "v2", stats.Version)
	require.Equal(t, uint64(1073741824), stats.TotalBytes)

	// Update the memory values
	fsys["sys/fs/cgroup/test/memory.current"] = &fstest.MapFile{Data: []byte("2147483648\n")}
	fsys["sys/fs/cgroup/test/memory.stat"] = &fstest.MapFile{Data: []byte("inactive_file 268435456\n")}

	// Second read - should use cached paths and get new values
	stats, err = r.Read()
	require.NoError(t, err)
	require.Equal(t, "v2", stats.Version)
	require.Equal(t, uint64(2147483648), stats.TotalBytes)
	require.Equal(t, uint64(268435456), stats.InactiveFileBytes)
}

func TestCachedReaderFallsBackToV1(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("11:memory:/docker/abc\n"),
		},
		"proc/self/mountinfo": &fstest.MapFile{
			Data: []byte("30 25 0:26 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory\n"),
		},
		"sys/fs/cgroup/memory/docker/abc/memory.usage_in_bytes": &fstest.MapFile{
			Data: []byte("1073741824\n"),
		},
		"sys/fs/cgroup/memory/docker/abc/memory.stat": &fstest.MapFile{
			Data: []byte("total_inactive_file 134217728\n"),
		},
	}

	r := NewReaderWithFS(fsys)

	stats, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, "v1", stats.Version)

	// Second read should still work
	fsys["sys/fs/cgroup/memory/docker/abc/memory.usage_in_bytes"] = &fstest.MapFile{Data: []byte("2147483648\n")}
	stats, err = r.Read()
	require.NoError(t, err)
	require.Equal(t, "v1", stats.Version)
	require.Equal(t, uint64(2147483648), stats.TotalBytes)
}

func TestCachedReaderUnavailable(t *testing.T) {
	fsys := fstest.MapFS{}

	r := NewReaderWithFS(fsys)

	_, err := r.Read()
	require.ErrorIs(t, err, ErrCgroupNotAvailable)

	// Subsequent reads should also fail without re-probing
	_, err = r.Read()
	require.ErrorIs(t, err, ErrCgroupNotAvailable)
}

func TestMissingInactiveFile(t *testing.T) {
	// Test that missing inactive_file defaults to 0 (WorkingSet = Total)
	fsys := fstest.MapFS{
		"proc/self/cgroup": &fstest.MapFile{
			Data: []byte("0::/test\n"),
		},
		"proc/self/mountinfo": &fstest.MapFile{
			Data: []byte("30 25 0:26 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw,nsdelegate\n"),
		},
		"sys/fs/cgroup/test/memory.current": &fstest.MapFile{
			Data: []byte("1073741824\n"),
		},
		"sys/fs/cgroup/test/memory.stat": &fstest.MapFile{
			Data: []byte("anon 536870912\nfile 268435456\n"), // no inactive_file
		},
	}

	stats, err := ReadWithFS(fsys)
	require.NoError(t, err)
	require.Equal(t, "v2", stats.Version)
	require.Equal(t, uint64(1073741824), stats.TotalBytes)
	require.Equal(t, uint64(0), stats.InactiveFileBytes)           // missing = 0
	require.Equal(t, uint64(1073741824), stats.WorkingSetBytes)    // Total - 0 = Total
}

func TestJoinCgroupPath(t *testing.T) {
	// Test that leading / in cgroupPath is handled correctly
	testCases := []struct {
		mount, cgroup, file, expected string
	}{
		{"/sys/fs/cgroup", "/kubepods/pod123", "memory.current", "/sys/fs/cgroup/kubepods/pod123/memory.current"},
		{"/sys/fs/cgroup", "kubepods/pod123", "memory.current", "/sys/fs/cgroup/kubepods/pod123/memory.current"},
		{"/sys/fs/cgroup", "/", "memory.current", "/sys/fs/cgroup/memory.current"},
		{"/sys/fs/cgroup/memory", "/docker/abc", "memory.stat", "/sys/fs/cgroup/memory/docker/abc/memory.stat"},
	}

	for _, tc := range testCases {
		result := joinCgroupPath(tc.mount, tc.cgroup, tc.file)
		require.Equal(t, tc.expected, result, "mount=%s cgroup=%s file=%s", tc.mount, tc.cgroup, tc.file)
	}
}

func TestCsvContains(t *testing.T) {
	require.True(t, csvContains("memory", "memory"))
	require.True(t, csvContains("cpu,memory", "memory"))
	require.True(t, csvContains("memory,cpu", "memory"))
	require.True(t, csvContains("cpu,memory,blkio", "memory"))
	require.True(t, csvContains("cpu, memory", "memory"))  // with spaces
	require.False(t, csvContains("cpu,cpuacct", "memory"))
	require.False(t, csvContains("memoryleak", "memory")) // exact match only
	require.False(t, csvContains("", "memory"))
}

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

//go:build integration

package test

import (
	"fmt"
	"testing"
	"time"

	toxiproxy "github.com/Shopify/toxiproxy/v2/client"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

const (
	toxiproxyAPI          = "localhost:8474"
	wedgeProxyName        = "rtmp_wedge"
	wedgeProxyListenAddr  = "localhost:11935"
	wedgeProxyUpstream    = "localhost:1935"
	wedgeHealthyDuration  = 5 * time.Second
	wedgeFailureDeadline  = 90 * time.Second
)

// ensureWedgeProxy ensures a toxiproxy proxy is in place that routes traffic
// from wedgeProxyListenAddr to mediamtx (wedgeProxyUpstream). It removes any
// stale toxics so each test starts from a clean state.
func ensureWedgeProxy(t *testing.T) (*toxiproxy.Client, *toxiproxy.Proxy) {
	t.Helper()
	client := toxiproxy.NewClient(toxiproxyAPI)

	// Reuse the proxy if it already exists from a previous test in the same run.
	proxies, err := client.Proxies()
	require.NoError(t, err, "toxiproxy must be reachable; ensure entrypoint started toxiproxy-server")

	if p, ok := proxies[wedgeProxyName]; ok {
		toxics, err := p.Toxics()
		require.NoError(t, err)
		for _, tox := range toxics {
			require.NoError(t, p.RemoveToxic(tox.Name))
		}
		require.NoError(t, p.Enable())
		return client, p
	}

	p, err := client.CreateProxy(wedgeProxyName, wedgeProxyListenAddr, wedgeProxyUpstream)
	require.NoError(t, err)
	return client, p
}

// testRtmpSilentWedge exercises the active liveness monitor:
//
//  1. Start an egress streaming through toxiproxy to mediamtx — bytes flow normally.
//  2. After a healthy stream is established, apply a `timeout=0` toxic so the proxy
//     stops forwarding bytes upstream while keeping the TCP socket alive. From
//     rtmp2sink's perspective this looks like a network-level wedge —
//     OutBytesTotal stops growing once the TCP send buffer fills, no GST errors.
//  3. Assert the egress marks the stream FAILED within livenessIdleTimeout + slack.
//     Without the liveness monitor, the stream stays ACTIVE indefinitely.
func (r *Runner) testRtmpSilentWedge(t *testing.T, test *testCase) {
	_, proxy := ensureWedgeProxy(t)
	defer func() {
		// Best-effort cleanup so subsequent tests don't inherit a half-broken proxy.
		toxics, _ := proxy.Toxics()
		for _, tox := range toxics {
			_ = proxy.RemoveToxic(tox.Name)
		}
	}()

	streamKey := utils.NewGuid("")
	wedgeURL := fmt.Sprintf("rtmp://%s/live/%s", wedgeProxyListenAddr, streamKey)
	test.streamUrls = []string{wedgeURL}

	req := r.build(test)
	egressID := r.startEgress(t, req)

	// Let the stream become healthy so OutBytesTotal grows past
	// livenessProgressThreshold in the monitor and monitorObservedProgress
	// becomes sticky.
	time.Sleep(wedgeHealthyDuration)

	wedgeRedacted, _ := utils.RedactStreamKey(wedgeURL)
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		wedgeRedacted: livekit.StreamInfo_ACTIVE,
	})

	// timeout=0 stops bytes from flowing through the proxy. From the sink's
	// perspective the connection wedges: writes stall, the existing TCP socket
	// eventually times out, and reconnect attempts can't complete the handshake.
	// Depending on which signal the egress observes first, the stream is failed
	// either by the active liveness monitor (if rtmp2sink goes silent) or by
	// the error-driven 30s give-up window (if rtmp2sink keeps emitting errors).
	// Either way the egress must stop reporting ACTIVE.
	_, err := proxy.AddToxic("wedge", "timeout", "upstream", 1.0, toxiproxy.Attributes{
		"timeout": 0,
	})
	require.NoError(t, err, "failed to apply wedge toxic")

	// Poll the latest egress snapshot until the egress stops reporting ACTIVE.
	// r.getUpdate returns the current snapshot held by the test IO server; the
	// inner sleep gives the egress time to publish state changes between polls.
	deadline := time.Now().Add(wedgeFailureDeadline)
	info := r.getUpdate(t, egressID)
	streamFailed := hasFailedStream(info, wedgeRedacted)
	for info.Status == livekit.EgressStatus_EGRESS_ACTIVE || info.Status == livekit.EgressStatus_EGRESS_STARTING {
		if time.Now().After(deadline) {
			t.Fatalf("egress did not leave EGRESS_ACTIVE within %s after wedge applied", wedgeFailureDeadline)
		}
		time.Sleep(100 * time.Millisecond)
		info = r.getUpdate(t, egressID)
		if hasFailedStream(info, wedgeRedacted) {
			streamFailed = true
		}
	}

	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status,
		"egress did not transition to FAILED after wedge applied")
	require.True(t, streamFailed || hasFailedStream(info, wedgeRedacted),
		"stream %s was never reported FAILED in any update", wedgeRedacted)
	require.NotEmpty(t, info.Error)
}

func hasFailedStream(info *livekit.EgressInfo, redactedURL string) bool {
	for _, s := range info.StreamResults {
		if s.Url == redactedURL && s.Status == livekit.StreamInfo_FAILED {
			return true
		}
	}
	return false
}

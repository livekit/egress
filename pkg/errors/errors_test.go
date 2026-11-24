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

package errors

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// NetworkFailure names how a request that got no response failed.
func TestNetworkFailure(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	get := func(t *testing.T, client *http.Client, url string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	t.Run("TLS version refused", func(t *testing.T) {
		server := httptest.NewUnstartedServer(ok)
		server.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
		server.StartTLS()
		defer server.Close()

		err := get(t, server.Client(), server.URL)
		require.Error(t, err)
		require.Equal(t, "TLS handshake failed", NetworkFailure(err))
	})

	t.Run("untrusted certificate", func(t *testing.T) {
		server := httptest.NewTLSServer(ok)
		defer server.Close()

		err := get(t, &http.Client{Transport: &http.Transport{}}, server.URL)
		require.Error(t, err)
		require.Equal(t, "TLS handshake failed", NetworkFailure(err))
	})

	t.Run("connection refused", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		url := "http://" + listener.Addr().String()
		require.NoError(t, listener.Close())

		err = get(t, &http.Client{Transport: &http.Transport{}}, url)
		require.Error(t, err)
		require.Equal(t, "connection failed", NetworkFailure(err))
	})

	t.Run("no answer in time", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		defer server.Close()
		defer close(release)

		client := &http.Client{Transport: &http.Transport{}, Timeout: 100 * time.Millisecond}
		err := get(t, client, server.URL)
		require.Error(t, err)
		require.Equal(t, "timed out", NetworkFailure(err))
	})

	t.Run("not a network error", func(t *testing.T) {
		require.Empty(t, NetworkFailure(errors.New("denied")))
	})
}

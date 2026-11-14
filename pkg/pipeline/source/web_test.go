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

package source

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInsecureChromeFlags(t *testing.T) {
	for _, insecure := range []bool{true, false} {
		flags := insecureChromeFlags(insecure)
		for _, name := range []string{
			"disable-web-security",
			"allow-running-insecure-content",
			"ignore-certificate-errors",
		} {
			require.Equal(t, insecure, flags[name], name)
		}
	}
}

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

package uploader

import (
	"github.com/livekit/storage"

	"github.com/livekit/egress/pkg/config"
)

func newAzureUploader(c *config.StorageConfig) (*store, error) {
	st, err := storage.NewAzure(c.Azure)
	if err != nil {
		return nil, err
	}

	return &store{
		Storage: st,
		conf:    c,
		name:    "Azure",
	}, nil

}

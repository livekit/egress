// Copyright 2025 LiveKit, Inc.
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

package logging

import (
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
	"time"
)

type TrackStats struct {
	Timestamp       string
	PacketsReceived uint64
	PaddingReceived uint64
	LastReceived    string
	PacketsDropped  uint64
	PacketsPushed   uint64
	SamplesPushed   uint64
	LastPushed      string
	Drift           time.Duration
	MaxDrift        time.Duration
}

type StreamStats struct {
	Timestamp     string
	Keyframes     uint64
	OutBytesTotal uint64
	OutBytesAcked uint64
	InBytesTotal  uint64
	InBytesAcked  uint64
}

// CSVLogger is used for logging data in CSV format. It does not validate columns or data
type CSVLogger[T any] struct {
	f *os.File
}

func NewCSVLogger[T any](filename string) (*CSVLogger[T], error) {
	if !strings.HasSuffix(filename, ".csv") {
		filename = filename + ".csv"
	}
	filename = path.Join(os.TempDir(), filename)
	f, err := os.Create(filename)
	if err != nil {
		return nil, err
	}

	columns := make([]string, 0)
	t := reflect.TypeFor[T]()
	for i := range t.NumField() {
		columns = append(columns, t.Field(i).Name)
	}
	_, _ = f.WriteString(fmt.Sprintf("%s\n", strings.Join(columns, ",")))

	return &CSVLogger[T]{
		f: f,
	}, nil
}

func (l *CSVLogger[T]) Write(value *T) {
	v := reflect.ValueOf(value).Elem()
	t := v.Type()

	row := make([]string, t.NumField())
	for i := range t.NumField() {
		row[i] = fmt.Sprintf("%v", v.Field(i).Interface())
	}

	_, _ = l.f.WriteString(strings.Join(row, ",") + "\n")
}

func (l *CSVLogger[T]) Close() {
	_ = l.f.Close()
}

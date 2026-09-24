// Copyright 2026 Google LLC
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

package engine

import "bytes"

const (
	// MaxOutputBytes caps each of stdout and stderr captured from a script.
	MaxOutputBytes = 64 * 1024
	// OutputTruncatedMarker is appended to output that exceeded MaxOutputBytes.
	OutputTruncatedMarker = "\n[output truncated]"
)

// limitWriter buffers up to MaxOutputBytes and silently discards the rest,
// always reporting a full write so the child never blocks on a full pipe.
type limitWriter struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if room := MaxOutputBytes - w.buf.Len(); room < len(p) {
		w.truncated = true
		p = p[:max(room, 0)]
	}
	w.buf.Write(p)
	return n, nil
}

// String returns the captured output, with a marker if any was discarded.
func (w *limitWriter) String() string {
	if w.truncated {
		return w.buf.String() + OutputTruncatedMarker
	}
	return w.buf.String()
}

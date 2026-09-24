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

// Package feed holds the presenter action feed and the SSE fan-out to
// attendees and presenter pages.
package feed

import (
	"fmt"
	"sync"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// Log is the action feed: the most recent entries, each updated in place
// as its turn progresses.
type Log struct {
	mu      sync.RWMutex
	max     int
	entries []types.ActionLogEntry
}

// NewLog returns an empty Log that retains up to max entries.
func NewLog(max int) *Log {
	return &Log{max: max, entries: make([]types.ActionLogEntry, 0)}
}

// Record appends entry, or merges it into the entry with the same ID, and
// returns the stored result. A missing timestamp or ID is filled in.
func (l *Log) Record(entry types.ActionLogEntry) types.ActionLogEntry {
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("action-%d", time.Now().UnixNano())
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, existing := range l.entries {
		if existing.ID == entry.ID {
			merged := existing
			if entry.AttendeeID != "" {
				merged.AttendeeID = entry.AttendeeID
			}
			if entry.Timestamp != 0 {
				merged.Timestamp = entry.Timestamp
			}
			if entry.Prompt != "" {
				merged.Prompt = entry.Prompt
			}
			if entry.ModelResponse != "" {
				merged.ModelResponse = entry.ModelResponse
			}
			if entry.Code != "" {
				merged.Code = entry.Code
			}
			if entry.Language != "" {
				merged.Language = entry.Language
			}
			if entry.RiskLevel != "" {
				merged.RiskLevel = entry.RiskLevel
			}
			if entry.ApprovalID != "" {
				merged.ApprovalID = entry.ApprovalID
			}
			if entry.Decision != "" {
				merged.Decision = entry.Decision
			}
			if entry.Execution != nil {
				merged.Execution = entry.Execution
			}
			if entry.WorkerPod != "" {
				merged.WorkerPod = entry.WorkerPod
			}
			// Each entry records the activity the actor moved to, idle included.
			merged.Activity = entry.Activity
			if entry.Source != "" {
				merged.Source = entry.Source
			}
			l.entries[i] = merged
			return merged
		}
	}
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.max {
		l.entries = l.entries[len(l.entries)-l.max:]
	}
	return entry
}

// History returns a copy of the current entries, oldest first.
func (l *Log) History() []types.ActionLogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	res := make([]types.ActionLogEntry, len(l.entries))
	copy(res, l.entries)
	return res
}

// Reset drops every entry.
func (l *Log) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = make([]types.ActionLogEntry, 0)
}

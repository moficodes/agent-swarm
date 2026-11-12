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

package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

func (g *Gateway) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	attendeeID := r.URL.Query().Get("attendee_id")
	if attendeeID == "" {
		http.Error(w, "attendee_id query parameter is required", http.StatusBadRequest)
		return
	}
	// EventSource cannot set headers, so the token may come in the query.
	if tok := r.URL.Query().Get("token"); tok != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	sub, err := g.authenticateRequest(r)
	if err != nil || sub != attendeeID {
		http.Error(w, "a token for this attendee is required", http.StatusUnauthorized)
		return
	}
	if isReservedHumanSlotID(attendeeID, g.maxActors, g.HumanSlotStride()) || g.IsLeased(attendeeID) {
		g.LeaseAttendee(attendeeID)
	}

	g.broker.ServeAttendee(w, r, attendeeID, func() any { return g.currentView(attendeeID) })
}

func (g *Gateway) handleActionStream(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/stream" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	g.broker.ServeActions(w, r)
}

func (g *Gateway) handleFeedHistory(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/feed/history" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	history := g.GetFeedHistory()

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(history)
}

// RecordActionEntry appends or updates an entry in the action feed (retaining up to 100 items)
// and broadcasts it to action subscribers.
func (g *Gateway) RecordActionEntry(entry types.ActionLogEntry) {
	entry = g.actions.Record(entry)
	if g.broker != nil {
		g.broker.PublishAction(entry)
	}
}

// GetFeedHistory returns a copy of the current action log entries.
func (g *Gateway) GetFeedHistory() []types.ActionLogEntry {
	return g.actions.History()
}

// SubscriberCount returns the current number of active SSE subscribers for an attendee.
func (g *Gateway) SubscriberCount(attendeeID string) int {
	if g.broker == nil {
		return 0
	}
	return g.broker.SubscriberCount(attendeeID)
}

// ActionSubscriberCount returns the current number of active action stream subscribers.
func (g *Gateway) ActionSubscriberCount() int {
	if g.broker == nil {
		return 0
	}
	return g.broker.ActionSubscriberCount()
}

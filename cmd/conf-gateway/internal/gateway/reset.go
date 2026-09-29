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
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

func (g *Gateway) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/reset" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	g.mu.Lock()
	sc := g.simController
	g.mu.Unlock()

	if sc != nil {
		sc.Stop()
	}

	var active, crashed []string
	for name, v := range g.board.Views() {
		switch v.State {
		case types.StateRunning, types.StateResuming:
			active = append(active, name)
		case types.StateCrashed:
			crashed = append(crashed, name)
		}
	}
	slices.Sort(active)
	slices.Sort(crashed)

	// A crashed actor refuses every request, including the history clear
	// below, so roll it back to its last good snapshot first. What it loses
	// is its chat, which the reset clears anyway.
	revertErrors := g.forEachActor(r.Context(), crashed, "revert", func(ctx context.Context, name string) error {
		return g.control.Revert(ctx, name)
	})

	g.mu.Lock()
	// Actors that held a conversation get their history cleared; the clear
	// suspends them again once it is done, so the reset leaves them alone.
	cleared := make(map[string]bool, len(g.chatHistories))
	for name := range g.chatHistories {
		cleared[name] = true
		g.requestHistoryClearLocked(name)
	}
	g.proxy.ResetPending()
	g.chatHistories = make(map[string][]types.ChatMessage)
	g.lastResults = make(map[string]*types.ExecutionResult)
	g.currentActionID = make(map[string]string)
	g.actions.Reset()
	g.addEventLocked("Stage reset: chats cleared, running actors suspended")
	g.mu.Unlock()

	g.board.Reset()
	if g.usage != nil {
		g.usage.ResetUsage()
	}
	g.broker.Broadcast("reset", struct{}{})

	var toSuspend []string
	for _, name := range active {
		if !cleared[name] {
			toSuspend = append(toSuspend, name)
		}
	}
	suspendErrors := g.forEachActor(r.Context(), toSuspend, "suspend", func(ctx context.Context, name string) error {
		return g.control.Suspend(ctx, name)
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":          "ok",
		"actors_reset":    len(toSuspend) + len(cleared),
		"suspend_errors":  suspendErrors,
		"actors_reverted": len(crashed) - int(revertErrors),
		"revert_errors":   revertErrors,
	})
}

// forEachActor runs op for every named actor, eight at a time, within 30s,
// and returns how many failed. Each failure is logged and shown as a stage
// event. It does nothing without actor control.
func (g *Gateway) forEachActor(ctx context.Context, names []string, verb string, op func(context.Context, string) error) int64 {
	if g.control == nil || len(names) == 0 {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var failed atomic.Int64
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := op(ctx, name); err != nil {
				log.Printf("[gateway] reset: %s %s: %v", verb, name, err)
				failed.Add(1)
				g.mu.Lock()
				g.addEventLocked(fmt.Sprintf("Warning: reset %s actor %s failed: %v", verb, name, err))
				g.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed.Load()
}

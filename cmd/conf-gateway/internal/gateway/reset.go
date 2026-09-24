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

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
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

	var active []string
	for name, v := range g.board.Views() {
		if v.State == types.StateRunning || v.State == types.StateResuming {
			active = append(active, name)
		}
	}
	slices.Sort(active)

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

	var failed atomic.Int64
	if g.control != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, name := range toSuspend {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				if err := g.control.Suspend(ctx, name); err != nil {
					log.Printf("[gateway] reset: suspend %s: %v", name, err)
					failed.Add(1)
					g.mu.Lock()
					g.addEventLocked(fmt.Sprintf("Warning: reset suspend actor %s failed: %v", name, err))
					g.mu.Unlock()
				}
			}()
		}
		wg.Wait()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"actors_reset":   len(toSuspend) + len(cleared),
		"suspend_errors": failed.Load(),
	})
}

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

	crashed, active := g.ActiveActorsForReset()
	sr := g.SimulationRunner()
	useDataGenReset := sr != nil && sr.HasDataGenerator() && g.control != nil

	var revertErrors int64
	if !useDataGenReset {
		// A crashed actor refuses every request, including the history clear
		// below, so roll it back to its last good snapshot first. What it loses
		// is its chat, which the reset clears anyway.
		revertErrors = g.forEachActor(r.Context(), crashed, "revert", func(ctx context.Context, name string) error {
			return g.control.Revert(ctx, name)
		})
	}

	g.mu.Lock()
	for name, t := range g.suspendTimers {
		if t.Stop() {
			g.wg.Done()
		}
		delete(g.suspendTimers, name)
	}
	// Actors that held a conversation get their history cleared; the clear
	// suspends them again once it is done, so the reset leaves them alone.
	// In large-swarm mode (>64 chat histories or external data-generator reset),
	// defer the in-actor /clear until the actor's next turn so reset does not
	// wake thousands of already-suspended actors at once.
	cleared := make(map[string]bool, len(g.chatHistories))
	deferHistoryClear := useDataGenReset || len(g.chatHistories) > 64
	for name := range g.chatHistories {
		if deferHistoryClear {
			c := g.historyClears[name]
			if c == nil {
				c = &historyClear{}
				g.historyClears[name] = c
			}
			c.want++
		} else {
			cleared[name] = true
			g.requestHistoryClearLocked(name)
		}
	}
	g.proxy.ResetPending()
	g.chatHistories = make(map[string][]types.ChatMessage)
	g.lastResults = make(map[string]*types.ExecutionResult)
	g.currentActionID = make(map[string]string)
	g.sampledActions = make(map[string]bool)
	g.leasedUntil = make(map[string]time.Time)
	g.actions.Reset()
	g.addEventLocked("Stage reset: chats cleared, running actors suspended")
	g.mu.Unlock()

	g.board.Reset()
	g.chatLimiter.reset()
	g.sessionLimiter.reset()
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

	if useDataGenReset {
		if started, err := sr.StartReset(crashed, toSuspend); err != nil {
			log.Printf("[gateway] external data-generator reset unavailable (%v); falling back to in-process reset", err)
			revertErrors = g.forEachActor(r.Context(), crashed, "revert", func(ctx context.Context, name string) error {
				return g.control.Revert(ctx, name)
			})
		} else if started {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":          "ok",
				"mode":            "data-generator",
				"actors_reset":    len(toSuspend) + len(cleared),
				"suspend_errors":  0,
				"actors_reverted": len(crashed),
				"revert_errors":   0,
			})
			return
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

// ActiveActorsForReset returns sorted lists of crashed and active
// (RUNNING/RESUMING/SUSPENDING) actors currently known on the board.
func (g *Gateway) ActiveActorsForReset() (crashed []string, active []string) {
	for name, v := range g.board.Views() {
		switch v.State {
		case types.StateRunning, types.StateResuming, types.StateSuspending:
			active = append(active, name)
		case types.StateCrashed:
			crashed = append(crashed, name)
		}
	}
	slices.Sort(crashed)
	slices.Sort(active)
	return crashed, active
}

// SuspendActorForReset suspends one actor during a data-generator reset pass.
func (g *Gateway) SuspendActorForReset(ctx context.Context, attendeeID string) error {
	if g.control == nil {
		return nil
	}
	if g.suspendSem != nil {
		select {
		case g.suspendSem <- struct{}{}:
			defer func() { <-g.suspendSem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	g.mu.Lock()
	g.suspendsInFlight[attendeeID]++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.suspendsInFlight[attendeeID]--
		if g.suspendsInFlight[attendeeID] <= 0 {
			delete(g.suspendsInFlight, attendeeID)
		}
		g.mu.Unlock()
	}()

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return g.control.Suspend(callCtx, attendeeID)
}

// RevertActorForReset reverts one crashed actor during a data-generator reset pass.
func (g *Gateway) RevertActorForReset(ctx context.Context, attendeeID string) error {
	if g.control == nil {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return g.control.Revert(callCtx, attendeeID)
}

// OnResetComplete records a stage event when a data-generator reset pass finishes.
func (g *Gateway) OnResetComplete(suspended int, reverted int, errors int) {
	g.mu.Lock()
	g.addEventLocked(fmt.Sprintf("Stage reset complete via data-generator: %d suspended, %d reverted (%d errors)", suspended, reverted, errors))
	g.mu.Unlock()
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
	sem := make(chan struct{}, 64)
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

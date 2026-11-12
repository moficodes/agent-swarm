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
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// beginTurn marks a turn in flight and cancels any pending suspend, so an
// actor is never suspended while it is serving a turn.
func (g *Gateway) beginTurn(attendee string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turnsInFlight[attendee]++
	g.turnSeq[attendee]++
	delete(g.runningIdleSince, attendee)
	if t, ok := g.suspendTimers[attendee]; ok {
		if t.Stop() {
			g.wg.Done()
		}
		delete(g.suspendTimers, attendee)
	}
}

// endTurn marks a turn finished and, once no turn is in flight, schedules
// the actor's suspend.
func (g *Gateway) endTurn(attendee string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turnsInFlight[attendee]--
	if g.turnsInFlight[attendee] > 0 {
		return
	}
	delete(g.turnsInFlight, attendee)
	g.suspendAfterTurnLocked(attendee)
}

// suspendAfterTurnLocked suspends the attendee's actor right away when
// suspendAfter is zero, or after suspendAfter of idleness. g.mu must be held.
func (g *Gateway) suspendAfterTurnLocked(attendee string) {
	if g.control == nil || g.closed {
		return
	}
	seq := g.turnSeq[attendee]
	if g.suspendAfter <= 0 {
		g.suspendWG.Add(1)
		go func() {
			defer g.suspendWG.Done()
			g.suspendIfIdle(attendee, seq)
		}()
		return
	}
	if t, ok := g.suspendTimers[attendee]; ok && t.Stop() {
		g.wg.Done()
	}
	g.wg.Add(1)
	var t *time.Timer
	t = time.AfterFunc(g.suspendAfter, func() {
		defer g.wg.Done()
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return
		}
		if g.suspendTimers[attendee] == t {
			delete(g.suspendTimers, attendee)
		}
		g.suspendWG.Add(1)
		g.mu.Unlock()
		defer g.suspendWG.Done()
		g.suspendIfIdle(attendee, seq)
	})
	g.suspendTimers[attendee] = t
}

// suspendIfIdle suspends the attendee's actor unless a turn has started
// since the suspend was scheduled at turn seq. A turn that starts after this
// check can still race the suspend; closing that gap needs ateapi to drain
// in-flight requests before it checkpoints.
func (g *Gateway) suspendIfIdle(attendee string, seq uint64) {
	if g.beforeSuspendCheck != nil {
		g.beforeSuspendCheck(attendee)
	}
	g.mu.Lock()
	busy := g.turnsInFlight[attendee] > 0 || g.turnSeq[attendee] != seq
	g.mu.Unlock()
	if busy {
		return
	}
	g.suspendActor(attendee)
}

func (g *Gateway) suspendActor(attendee string) {
	if g.suspendSem != nil {
		select {
		case g.suspendSem <- struct{}{}:
			defer func() { <-g.suspendSem }()
		default:
			select {
			case g.suspendSem <- struct{}{}:
				defer func() { <-g.suspendSem }()
			case <-g.suspendCtx.Done():
				return
			}
		}
	}

	g.mu.Lock()
	g.suspendsInFlight[attendee]++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.suspendsInFlight[attendee]--
		if g.suspendsInFlight[attendee] <= 0 {
			delete(g.suspendsInFlight, attendee)
		}
		g.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(g.suspendCtx, 30*time.Second)
	defer cancel()
	if g.board != nil {
		g.board.BeginHyperSuspend(attendee)
	}
	err := g.control.Suspend(ctx, attendee)
	if g.board != nil {
		g.board.FinishHyperSuspend(attendee, err == nil)
	}
	if err != nil {
		if g.suspendCtx.Err() != nil {
			return // the gateway's suspend drain window ended
		}
		if strings.Contains(err.Error(), "another operation is in progress") || strings.Contains(err.Error(), "Aborted") {
			g.scheduleSuspendRetry(attendee)
			return
		}
		log.Printf("[gateway] auto-suspend %s: %v", attendee, err)
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Warning: suspend actor %s failed: %v", attendee, err))
		g.mu.Unlock()
	}
}

// HoldSeat extends a warm seated actor's post-turn auto-suspend timer so a
// multi-turn session keeps its live sandbox on the worker pod between turns
// without holding an open HTTP stream through atenet-router.
func (g *Gateway) HoldSeat(attendee string, d time.Duration) {
	if d <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.control == nil || g.closed || g.turnsInFlight[attendee] > 0 {
		return
	}
	if t, ok := g.suspendTimers[attendee]; ok && t.Stop() {
		g.wg.Done()
	}
	seq := g.turnSeq[attendee]
	g.wg.Add(1)
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		defer g.wg.Done()
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return
		}
		if g.suspendTimers[attendee] == t {
			delete(g.suspendTimers, attendee)
		}
		g.suspendWG.Add(1)
		g.mu.Unlock()
		defer g.suspendWG.Done()
		g.suspendIfIdle(attendee, seq)
	})
	g.suspendTimers[attendee] = t
}

func (g *Gateway) drainUnleasedHeldSeats(isProtected func(string) bool) {
	if g.control == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	baseDelay := max(g.suspendAfter, 500*time.Millisecond)
	buckets := 32
	stepMs := 400
	if len(g.suspendTimers) > 8192 {
		// Pace hyper-mode Stop Simulation drains to ~20 SuspendActor RPCs/sec so
		// checkpoint uploads stay comfortably below GCP's project-wide 100 QPS STS
		// token-exchange limit.
		buckets = max(256, len(g.suspendTimers)/5)
		stepMs = 250
	}
	step := 0
	for attendee, oldTimer := range g.suspendTimers {
		if isProtected != nil && isProtected(attendee) {
			continue
		}
		if g.turnsInFlight[attendee] > 0 {
			continue
		}
		if oldTimer.Stop() {
			g.wg.Done()
		}
		seq := g.turnSeq[attendee]
		stagger := baseDelay + time.Duration((step%buckets)*stepMs)*time.Millisecond
		step++
		g.wg.Add(1)
		var nt *time.Timer
		nt = time.AfterFunc(stagger, func() {
			defer g.wg.Done()
			g.mu.Lock()
			if g.closed {
				g.mu.Unlock()
				return
			}
			if g.suspendTimers[attendee] == nt {
				delete(g.suspendTimers, attendee)
			}
			g.suspendWG.Add(1)
			g.mu.Unlock()
			defer g.suspendWG.Done()
			g.suspendIfIdle(attendee, seq)
		})
		g.suspendTimers[attendee] = nt
	}
}

func (g *Gateway) scheduleSuspendRetry(attendee string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.control == nil || g.closed || g.turnsInFlight[attendee] > 0 {
		return
	}
	if _, hasTimer := g.suspendTimers[attendee]; hasTimer {
		return
	}
	seq := g.turnSeq[attendee]
	retryDelay := max(g.suspendAfter, 2*time.Second)
	g.wg.Add(1)
	var t *time.Timer
	t = time.AfterFunc(retryDelay, func() {
		defer g.wg.Done()
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return
		}
		if g.suspendTimers[attendee] == t {
			delete(g.suspendTimers, attendee)
		}
		g.suspendWG.Add(1)
		g.mu.Unlock()
		defer g.suspendWG.Done()
		g.suspendIfIdle(attendee, seq)
	})
	g.suspendTimers[attendee] = t
}

// reconcileStuckSuspendingWithViews re-drives Substrate's idempotent SuspendActor
// workflow for any actor reported in StateSuspending for at least
// stuckSuspendingAfter while this gateway has no turn or suspend in flight for
// it (for example after an interrupted caller left ateapi mid-suspend).
func (g *Gateway) reconcileStuckSuspendingWithViews(now time.Time, views map[string]types.ActorView) {
	if g.control == nil || g.stuckSuspendingAfter <= 0 {
		return
	}
	var toRecover []string

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	for name := range g.suspendingSince {
		if v, ok := views[name]; !ok || v.State != types.StateSuspending {
			delete(g.suspendingSince, name)
		}
	}
	for name, v := range views {
		if v.State != types.StateSuspending {
			continue
		}
		since, seen := g.suspendingSince[name]
		if !seen {
			g.suspendingSince[name] = now
			continue
		}
		if now.Sub(since) < g.stuckSuspendingAfter {
			continue
		}
		if g.turnsInFlight[name] > 0 || g.suspendsInFlight[name] > 0 {
			continue
		}
		g.suspendingSince[name] = now
		g.suspendWG.Add(1)
		toRecover = append(toRecover, name)
	}
	g.mu.Unlock()

	for _, name := range toRecover {
		go func() {
			defer g.suspendWG.Done()
			log.Printf("[gateway] recovering actor %s stuck in SUSPENDING", name)
			g.suspendActor(name)
		}()
	}
}

func (g *Gateway) isBusyLocked(attendee string) bool {
	if g.turnsInFlight[attendee] > 0 || g.suspendsInFlight[attendee] > 0 || g.suspendTimers[attendee] != nil {
		return true
	}
	if c := g.historyClears[attendee]; c != nil && c.done < c.want {
		return true
	}
	return false
}

// reconcileStuckRunningWithViews suspends any actor reported in StateRunning
// for at least max(stuckRunningAfter, suspendAfter) while this gateway has no
// turn, auto-suspend timer, history clear, or suspend in flight for it (for
// example after a client or previous gateway pod disconnected mid-ResumeActor
// and ate-api-server finished waking the actor after the caller was gone).
func (g *Gateway) reconcileStuckRunningWithViews(now time.Time, views map[string]types.ActorView) {
	if g.control == nil || g.stuckRunningAfter <= 0 {
		return
	}
	type idleTarget struct {
		name string
		seq  uint64
	}
	var candidates []string
	g.mu.RLock()
	if g.closed {
		g.mu.RUnlock()
		return
	}
	if sc, ok := g.simController.(*SimulationRunner); ok && sc.IsAdoptingWarmSeats(now) {
		g.mu.RUnlock()
		return
	}
	for name, v := range views {
		if v.State == types.StateRunning && !g.isBusyLocked(name) {
			candidates = append(candidates, name)
		}
	}
	hasTracked := len(g.runningIdleSince) > 0
	g.mu.RUnlock()
	if len(candidates) == 0 && !hasTracked {
		return
	}

	var toSuspend []idleTarget
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	for name := range g.runningIdleSince {
		if v, ok := views[name]; !ok || v.State != types.StateRunning || g.isBusyLocked(name) {
			delete(g.runningIdleSince, name)
		}
	}
	threshold := max(g.stuckRunningAfter, g.suspendAfter)
	for _, name := range candidates {
		v, ok := views[name]
		if !ok || v.State != types.StateRunning || g.isBusyLocked(name) {
			continue
		}
		since, seen := g.runningIdleSince[name]
		if !seen {
			g.runningIdleSince[name] = now
			continue
		}
		if now.Sub(since) < threshold {
			continue
		}
		if g.maxActors > 262144 && len(toSuspend) >= 256 {
			break
		}
		g.runningIdleSince[name] = now
		g.suspendWG.Add(1)
		toSuspend = append(toSuspend, idleTarget{name: name, seq: g.turnSeq[name]})
	}
	g.mu.Unlock()

	for _, t := range toSuspend {
		go func() {
			defer g.suspendWG.Done()
			log.Printf("[gateway] suspending orphaned RUNNING actor %s", t.name)
			g.suspendIfIdle(t.name, t.seq)
		}()
	}
}

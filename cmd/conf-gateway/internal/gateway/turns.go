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
	"math/rand/v2"
	"strings"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// beginTurn marks a turn in flight and cancels any pending suspend, so an
// actor is never suspended while it is serving a turn.
func (g *Gateway) beginTurn(attendee string) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turnsInFlight[attendee]++
	g.nextTurnSeq++
	seq := g.nextTurnSeq
	if g.maxActors > actorstate.HyperFleetThreshold && len(g.turnSeq) >= 65536 {
		for k := range g.turnSeq {
			if g.turnsInFlight[k] == 0 && g.suspendTimers[k] == nil && g.suspendsInFlight[k] == 0 {
				delete(g.turnSeq, k)
			}
		}
	}
	g.turnSeq[attendee] = seq
	delete(g.runningIdleSince, attendee)
	if t, ok := g.suspendTimers[attendee]; ok {
		if t.Stop() {
			g.wg.Done()
		}
		delete(g.suspendTimers, attendee)
	}
	return seq
}

// endTurn marks a turn finished and, once no turn is in flight, schedules
// the actor's suspend.
func (g *Gateway) endTurn(attendee string) {
	g.endTurnWithOpts(attendee, false, false)
}

// endTurnWithOpts marks a turn finished and schedules the actor's suspend.
func (g *Gateway) endTurnWithOpts(attendee string, useMock, _ bool) {
	isHyperMock := useMock && g.maxActors > actorstate.HyperFleetThreshold && !isReservedHumanSlotID(attendee, g.maxActors, g.HumanSlotStride())
	isUnseatedSuspended := false
	if isHyperMock && g.board != nil {
		if v, ok := g.board.View(attendee); ok && v.State == types.StateSuspended && v.WorkerPod == "" {
			isUnseatedSuspended = true
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.turnsInFlight[attendee]--
	if g.turnsInFlight[attendee] > 0 {
		return
	}
	delete(g.turnsInFlight, attendee)

	exp, hasLease := g.leasedUntil[attendee]
	isLeased := hasLease && time.Now().Before(exp)
	if isHyperMock && !isLeased && isUnseatedSuspended {
		return
	}
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
	delay := g.suspendAfter
	if g.maxActors > actorstate.HyperFleetThreshold && delay > 500*time.Millisecond {
		exp, hasLease := g.leasedUntil[attendee]
		isLeased := hasLease && time.Now().Before(exp)
		if !isLeased && !isReservedHumanSlotID(attendee, g.maxActors, g.HumanSlotStride()) {
			delay = 500 * time.Millisecond
		}
	}
	var t *time.Timer
	t = time.AfterFunc(delay, func() {
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

func (g *Gateway) paceAutomatedSuspend(ctx context.Context, attendee string) bool {
	if g.maxActors <= actorstate.HyperFleetThreshold {
		return true
	}
	if g.IsLeased(attendee) || isReservedHumanSlotID(attendee, g.maxActors, g.HumanSlotStride()) {
		return true
	}
	now := time.Now().UnixNano()
	next := g.suspendNextUnixNano.Load()
	if next <= now {
		return true
	}
	const stepNs = int64(250 * time.Microsecond)
	var wait time.Duration
	for {
		prev := g.suspendNextUnixNano.Load()
		if prev <= now {
			return true
		}
		target := prev + stepNs
		if g.suspendNextUnixNano.CompareAndSwap(prev, target) {
			wait = time.Duration(min(prev-now, int64(5*time.Second)))
			break
		}
	}
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (g *Gateway) noteSuspendRateLimit() {
	const backoffNs = int64(250 * time.Millisecond)
	now := time.Now().UnixNano()
	for {
		prev := g.suspendNextUnixNano.Load()
		target := max(now, prev) + int64(15*time.Millisecond)
		if target > now+backoffNs {
			target = now + backoffNs
		}
		if g.suspendNextUnixNano.CompareAndSwap(prev, target) {
			return
		}
	}
}

func (g *Gateway) suspendActor(attendee string) {
	g.mu.Lock()
	if g.turnsInFlight[attendee] > 0 {
		g.mu.Unlock()
		return
	}
	g.suspendsInFlight[attendee]++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.suspendsInFlight[attendee]--
		if g.suspendsInFlight[attendee] <= 0 {
			delete(g.suspendsInFlight, attendee)
			if g.turnsInFlight[attendee] == 0 && g.suspendTimers[attendee] == nil {
				delete(g.turnSeq, attendee)
			}
		}
		g.mu.Unlock()
	}()

	isHyperAutomated := g.maxActors > actorstate.HyperFleetThreshold && !g.IsLeased(attendee) && !isReservedHumanSlotID(attendee, g.maxActors, g.HumanSlotStride())
	if isHyperAutomated && g.board != nil {
		if p := g.workerPods.Load(); p != nil && p.Available && p.Current >= 1024 && g.board.ActiveSeatCount() < 208000 {
			if sr := g.SimulationRunner(); sr != nil {
				if simRunning, _ := sr.Status(); simRunning {
					if v, ok := g.board.View(attendee); ok && v.Activity != types.ActivityAwaitingApproval {
						g.scheduleSuspendRetry(attendee)
						return
					}
				}
			}
		}
	}

	if !g.paceAutomatedSuspend(g.suspendCtx, attendee) {
		return
	}
	if g.suspendSem != nil {
		select {
		case g.suspendSem <- struct{}{}:
			defer func() { <-g.suspendSem }()
		default:
			if isHyperAutomated {
				g.scheduleSuspendRetry(attendee)
				return
			}
			select {
			case g.suspendSem <- struct{}{}:
				defer func() { <-g.suspendSem }()
			case <-g.suspendCtx.Done():
				return
			}
		}
	}

	g.mu.RLock()
	busy := g.turnsInFlight[attendee] > 0
	g.mu.RUnlock()
	if busy {
		return
	}

	if g.board != nil && g.maxActors > actorstate.HyperFleetThreshold {
		if v, ok := g.board.View(attendee); ok {
			if v.State == types.StateSuspended && v.WorkerPod == "" {
				return
			}
			if v.State == types.StateResuming && v.WorkerPod == "" {
				g.scheduleSuspendRetry(attendee)
				return
			}
		}
	}

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
		errMsg := err.Error()
		if strings.Contains(errMsg, "ACTOR_STATE_SUSPENDED") {
			if g.board != nil {
				g.board.FinishHyperSuspend(attendee, true)
			}
			return
		}
		if strings.Contains(errMsg, "ACTOR_STATE_SUSPENDING") {
			return
		}
		if strings.Contains(errMsg, "ACTOR_STATE_CRASHED") {
			revErr := g.control.Revert(ctx, attendee)
			if revErr == nil || strings.Contains(revErr.Error(), "ACTOR_STATE_SUSPENDED") {
				if g.board != nil {
					g.board.FinishHyperSuspend(attendee, true)
				}
				return
			}
		}
		if strings.Contains(errMsg, "status 429") ||
			strings.Contains(errMsg, "ResourceExhausted") ||
			strings.Contains(errMsg, "preparing storage credentials") {
			g.noteSuspendRateLimit()
			g.scheduleSuspendRetry(attendee)
			return
		}
		if strings.Contains(errMsg, "another operation is in progress") ||
			strings.Contains(errMsg, "Aborted") ||
			strings.Contains(errMsg, "ACTOR_STATE_RESUMING") ||
			strings.Contains(errMsg, "ACTOR_STATE_REVERTING") ||
			strings.Contains(errMsg, "DeadlineExceeded") {
			g.scheduleSuspendRetry(attendee)
			return
		}
		log.Printf("[gateway] auto-suspend %s: %v", attendee, err)
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Warning: suspend actor %s failed: %v", attendee, err))
		g.mu.Unlock()
	}
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
	if g.maxActors > actorstate.HyperFleetThreshold && g.suspendAfter >= 500*time.Millisecond {
		retryDelay += time.Duration(rand.IntN(3000)) * time.Millisecond
	}
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
	for name, v := range views {
		if v.State == types.StateRunning && !g.isBusyLocked(name) {
			if g.maxActors > actorstate.HyperFleetThreshold && (v.WorkerPod == "" || (v.Activity != types.ActivityIdle && v.Activity != "" && v.Activity != types.ActivityAwaitingApproval)) {
				continue
			}
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
	if g.maxActors > actorstate.HyperFleetThreshold && threshold > 5*time.Second {
		threshold = 5 * time.Second
	}
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
		if g.maxActors > 262144 && len(toSuspend) >= 4096 {
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
			if g.maxActors <= actorstate.HyperFleetThreshold {
				log.Printf("[gateway] suspending orphaned RUNNING actor %s", t.name)
			}
			g.suspendIfIdle(t.name, t.seq)
		}()
	}
}

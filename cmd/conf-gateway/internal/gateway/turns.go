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
	"time"
)

// beginTurn marks a turn in flight and cancels any pending suspend, so an
// actor is never suspended while it is serving a turn.
func (g *Gateway) beginTurn(attendee string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turnsInFlight[attendee]++
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
	g.wg.Add(1)
	if g.suspendAfter <= 0 {
		go func() {
			defer g.wg.Done()
			g.suspendActor(attendee)
		}()
		return
	}
	if t, ok := g.suspendTimers[attendee]; ok && t.Stop() {
		g.wg.Done()
	}
	var t *time.Timer
	t = time.AfterFunc(g.suspendAfter, func() {
		defer g.wg.Done()
		g.mu.Lock()
		if g.suspendTimers[attendee] == t {
			delete(g.suspendTimers, attendee)
		}
		g.mu.Unlock()
		g.suspendActor(attendee)
	})
	g.suspendTimers[attendee] = t
}

func (g *Gateway) suspendActor(attendee string) {
	ctx, cancel := context.WithTimeout(g.ctx, 30*time.Second)
	defer cancel()
	if err := g.control.Suspend(ctx, attendee); err != nil {
		if g.ctx.Err() != nil {
			return // the gateway is closing
		}
		log.Printf("[gateway] auto-suspend %s: %v", attendee, err)
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Warning: suspend actor %s failed: %v", attendee, err))
		g.mu.Unlock()
	}
}

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

// historyClearTimeout bounds one background clear, long enough for the
// router to park the request while a suspended actor resumes.
const historyClearTimeout = 90 * time.Second

// historyClear tracks the history clears owed to one attendee's actor.
type historyClear struct {
	want, done uint64        // clear generations requested and settled
	running    chan struct{} // closed when the clear in flight ends
}

// requestHistoryClearLocked starts clearing the attendee's actor history in
// the background. g.mu must be held.
func (g *Gateway) requestHistoryClearLocked(attendee string) {
	if g.closed {
		return
	}
	c := g.historyClears[attendee]
	if c == nil {
		c = &historyClear{}
		g.historyClears[attendee] = c
	}
	c.want++
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		ctx, cancel := context.WithTimeout(g.ctx, historyClearTimeout)
		defer cancel()
		g.settleHistoryClear(ctx, attendee)
	}()
}

// settleHistoryClear returns once no clear is owed to the attendee's actor,
// running one if needed. Turns call it first so they never overlap a clear.
// A failed clear is reported once and not retried. The clear counts as a
// turn, so the actor it resumes is suspended again afterward.
func (g *Gateway) settleHistoryClear(ctx context.Context, attendee string) {
	for {
		g.mu.Lock()
		c := g.historyClears[attendee]
		if c == nil || c.done >= c.want {
			g.mu.Unlock()
			return
		}
		if ch := c.running; ch != nil {
			g.mu.Unlock()
			if g.waitingForHistoryClear != nil {
				g.waitingForHistoryClear(attendee)
			}
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return
			}
		}
		target := c.want
		ch := make(chan struct{})
		c.running = ch
		g.mu.Unlock()

		g.beginTurn(attendee)
		err := g.proxy.ClearHistory(ctx, attendee)
		g.endTurn(attendee)

		g.mu.Lock()
		c.running = nil
		close(ch)
		c.done = target
		if err != nil && g.ctx.Err() == nil {
			log.Printf("[gateway] clear history %s: %v", attendee, err)
			g.addEventLocked(fmt.Sprintf("Warning: clear history for actor %s failed: %v", attendee, err))
		}
		g.mu.Unlock()
	}
}

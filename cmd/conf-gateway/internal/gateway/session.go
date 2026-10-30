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
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// Claims represents the decoded JWT payload.
type Claims = auth.Claims

func (g *Gateway) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	presenter := g.isPresenter(r)
	if !presenter && !g.sessionLimiter.allow(clientIP(r)) {
		http.Error(w, fmt.Sprintf("session rate limit exceeded (max %d requests per minute)", g.sessionRateLimitRPM), http.StatusTooManyRequests)
		return
	}

	maxActors := 50
	if g.maxActors > 0 {
		maxActors = g.maxActors
	}

	requestedID := strings.TrimSpace(r.URL.Query().Get("attendee_id"))
	var attendeeID string
	if requestedID != "" && presenter && auth.ValidAttendeeID(requestedID, maxActors) {
		attendeeID = requestedID
	} else {

		startOffset := 0
		sc := g.SimulationController()

		if sc != nil {
			running, count := sc.Status()
			if running && count > 0 && count < maxActors {
				startOffset = count
			}
		}

		availableRange := maxActors - startOffset
		if availableRange <= 0 {
			availableRange = maxActors
			startOffset = 0
		}

		attendeeID = g.selectAttendeeSlot(startOffset, availableRange)
		g.forgetAttendee(attendeeID)
	}

	token := g.MintToken(attendeeID)

	if _, exists := g.board.View(attendeeID); !exists {
		g.setActivity(attendeeID, types.ActivityIdle)
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Provisioned session for %s", attendeeID))
		g.mu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"attendee_id": attendeeID,
		"token":       token,
	})
}

// selectAttendeeSlot picks the next attendee slot in [startOffset+1 .. startOffset+availableRange],
// skipping slots that are currently executing a turn or waiting on a human approval
// so active attendees are not evicted when the counter wraps around.
func (g *Gateway) selectAttendeeSlot(startOffset, availableRange int) string {
	var firstCandidate string
	for attempt := 0; attempt < availableRange; attempt++ {
		seq := atomic.AddUint64(&g.attendeeSeq, 1)
		index := startOffset + int((seq-1)%uint64(availableRange)) + 1
		candidate := fmt.Sprintf("attendee-%02d", index)
		if attempt == 0 {
			firstCandidate = candidate
		}
		if !g.isSlotActive(candidate) {
			return candidate
		}
	}
	return firstCandidate
}

func (g *Gateway) isSlotActive(attendee string) bool {
	g.mu.RLock()
	inFlight := g.turnsInFlight[attendee]
	g.mu.RUnlock()
	if inFlight > 0 {
		return true
	}
	return g.proxy != nil && g.proxy.PendingApproval(attendee) != nil
}

// forgetAttendee drops everything kept for the attendee's previous visitor,
// in the gateway and in the actor's memory, before the slot is handed out.
func (g *Gateway) forgetAttendee(attendee string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.chatHistories, attendee)
	delete(g.lastResults, attendee)
	delete(g.currentActionID, attendee)
	g.proxy.ClearPending(attendee)
	g.requestHistoryClearLocked(attendee)
}

func (g *Gateway) authenticateRequest(r *http.Request) (string, error) {
	return g.signer.Authenticate(r)
}

// MintToken creates a signed HS256 JWT for the specified attendee ID.
func (g *Gateway) MintToken(attendeeID string) string {
	return g.signer.Mint(attendeeID)
}

// VerifyToken validates an HS256 JWT string and returns the decoded claims.
func (g *Gateway) VerifyToken(tokenStr string) (*Claims, error) {
	return g.signer.Verify(tokenStr)
}

// isPresenter reports whether the request carries the presenter key, either in
// the cookie set by a presenter page or in the PresenterHeaderName header.
func (g *Gateway) isPresenter(r *http.Request) bool {
	return auth.PresenterOK(r, g.presenterKey)
}

func (g *Gateway) requirePresenter(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.isPresenter(r) {
			http.Error(w, "presenter key required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

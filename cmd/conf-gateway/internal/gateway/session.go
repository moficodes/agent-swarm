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
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const attendeeLeaseTTL = 30 * time.Minute

func isReservedHumanSlotID(attendeeID string, maxActors, stride int) bool {
	if maxActors <= actorstate.CompactFleetThreshold || stride <= 1 {
		return false
	}
	numStr, ok := strings.CutPrefix(attendeeID, "attendee-")
	if !ok {
		return false
	}
	idx, err := strconv.Atoi(numStr)
	if err != nil {
		return false
	}
	return idx >= stride && idx <= maxActors && idx%stride == 0
}

// HumanSlotStride returns the periodic reserved human attendee slot stride
// (e.g. 16 on megaswarm, 4096 on hyperswarm, or 0 on small swarms).
func (g *Gateway) HumanSlotStride() int {
	if g.humanSlotStride > 0 {
		return g.humanSlotStride
	}
	if g.maxActors > actorstate.HyperFleetThreshold {
		return 4096
	}
	if g.maxActors > actorstate.CompactFleetThreshold {
		return 16
	}
	return 0
}

// LeasedIDs returns a snapshot slice of currently leased attendee IDs.
func (g *Gateway) LeasedIDs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.leasedUntil) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]string, 0, len(g.leasedUntil))
	for id, until := range g.leasedUntil {
		if now.Before(until) {
			out = append(out, id)
		}
	}
	return out
}

// LeaseAttendee marks an attendee slot as leased by a live QR-code user.
func (g *Gateway) LeaseAttendee(attendeeID string) {
	if attendeeID == "" {
		return
	}
	g.mu.Lock()
	g.leasedUntil[attendeeID] = time.Now().Add(attendeeLeaseTTL)
	g.mu.Unlock()
}

// IsLeased reports whether an attendee slot currently holds an active human lease.
func (g *Gateway) IsLeased(attendeeID string) bool {
	g.mu.RLock()
	until, ok := g.leasedUntil[attendeeID]
	g.mu.RUnlock()
	return ok && time.Now().Before(until)
}

// leasedChecker returns a lock-free predicate reporting which attendee slots
// currently hold an active human lease, safe to pass into Board and Proxy leaf
// methods without acquiring g.mu under their locks.
func (g *Gateway) leasedChecker() func(string) bool {
	g.mu.RLock()
	if len(g.leasedUntil) == 0 {
		g.mu.RUnlock()
		return nil
	}
	now := time.Now()
	snap := make(map[string]bool, len(g.leasedUntil))
	for id, until := range g.leasedUntil {
		if now.Before(until) {
			snap[id] = true
		}
	}
	g.mu.RUnlock()
	if len(snap) == 0 {
		return nil
	}
	return func(id string) bool {
		return snap[id]
	}
}

// simCleanupProtectedIndices builds the sparse 1-based index set of leased and
// SSE-subscribed slots so hyper-mode ClearUnleasedActivityIndices never has to
// format strings or acquire mutexes 16.78 million times.
func (g *Gateway) simCleanupProtectedIndices() map[int]bool {
	out := make(map[int]bool)
	for _, id := range g.LeasedIDs() {
		if idx, ok := actorstate.ParseAttendeeIndex(id, g.maxActors); ok {
			out[idx] = true
		}
	}
	if g.broker != nil {
		for _, id := range g.broker.SubscribedAttendeeIDs() {
			if idx, ok := actorstate.ParseAttendeeIndex(id, g.maxActors); ok {
				out[idx] = true
			}
		}
	}
	return out
}

// simCleanupProtectedChecker returns a lock-free predicate reporting which
// attendee slots must be preserved when ClearSimulatedActivity resets
// simulated activity and pending approvals (leased human slots, reserved
// periodic human slots, and slots with active SSE subscribers).
func (g *Gateway) simCleanupProtectedChecker() func(string) bool {
	maxActors := g.maxActors
	stride := g.HumanSlotStride()
	isLeased := g.leasedChecker()
	if isLeased == nil && maxActors <= actorstate.CompactFleetThreshold && g.broker == nil {
		return nil
	}
	return func(id string) bool {
		if isLeased != nil && isLeased(id) {
			return true
		}
		if isReservedHumanSlotID(id, maxActors, stride) {
			return true
		}
		return g.SubscriberCount(id) > 0
	}
}

// IsReservedForHuman reports whether the background simulation should skip this slot.
func (g *Gateway) IsReservedForHuman(attendeeID string, slotIndex int) bool {
	stride := g.HumanSlotStride()
	if g.maxActors > actorstate.CompactFleetThreshold && stride > 1 && slotIndex%stride == 0 {
		return true
	}
	return g.IsLeased(attendeeID)
}

func (g *Gateway) selectReservedAttendeeSlot(maxActors int) string {
	stride := g.HumanSlotStride()
	if stride <= 1 {
		return g.selectAttendeeSlot(0, maxActors)
	}
	numBuckets := maxActors / stride
	if numBuckets < 1 {
		return g.selectAttendeeSlot(0, maxActors)
	}
	var firstCandidate string
	var firstAvailable string
	for attempt := 0; attempt < numBuckets; attempt++ {
		seq := atomic.AddUint64(&g.attendeeSeq, 1)
		slotIndex := (int((seq-1)%uint64(numBuckets)) + 1) * stride
		candidate := fmt.Sprintf("attendee-%02d", slotIndex)
		if attempt == 0 {
			firstCandidate = candidate
		}
		if !g.isSlotActive(candidate) && !g.IsLeased(candidate) {
			if firstAvailable == "" {
				firstAvailable = candidate
			}
			if g.board == nil {
				return candidate
			}
			if maxActors > actorstate.HyperFleetThreshold && g.control != nil && attempt < 32 {
				if getter, hasGet := g.control.(actorstate.SingleActorGetter); hasGet {
					parentCtx := g.ctx
					if parentCtx == nil {
						parentCtx = context.Background()
					}
					ctx, cancel := context.WithTimeout(parentCtx, 2*time.Second)
					s, err := getter.Get(ctx, candidate)
					cancel()
					if err == nil {
						uri := s.SnapshotURI
						if s.InProgressSnapshotURI != "" {
							uri = s.InProgressSnapshotURI
						}
						if s.State == types.StateSuspended && (uri == "" || strings.Contains(uri, "/tags/")) {
							return candidate
						}
						continue
					}
				}
			}
			if v, ok := g.board.View(candidate); !ok || v.NeverStarted || strings.Contains(v.SnapshotURI, "/tags/") {
				return candidate
			}
		}
	}
	if firstAvailable != "" {
		return firstAvailable
	}
	return firstCandidate
}

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
		if maxActors > actorstate.CompactFleetThreshold {
			g.LeaseAttendee(attendeeID)
		}
	} else if maxActors > actorstate.CompactFleetThreshold {
		attendeeID = g.selectReservedAttendeeSlot(maxActors)
		g.LeaseAttendee(attendeeID)
		g.forgetAttendee(attendeeID)
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
	if g.SubscriberCount(attendee) > 0 {
		return true
	}
	return g.proxy != nil && g.proxy.PendingApproval(attendee) != nil
}

// forgetAttendee drops everything kept for the attendee's previous visitor,
// in the gateway and in the actor's memory, before the slot is handed out.
func (g *Gateway) forgetAttendee(attendee string) {
	v, hasView := g.board.View(attendee)
	g.mu.Lock()
	defer g.mu.Unlock()
	_, hadHistory := g.chatHistories[attendee]
	delete(g.chatHistories, attendee)
	delete(g.lastResults, attendee)
	delete(g.currentActionID, attendee)
	g.proxy.ClearPending(attendee)
	if g.maxActors > actorstate.CompactFleetThreshold && !hadHistory && (!hasView || v.NeverStarted || strings.Contains(v.SnapshotURI, "/tags/")) {
		return
	}
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

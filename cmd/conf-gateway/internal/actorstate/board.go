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

package actorstate

import (
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// CompactFleetThreshold is the actor count above which TelemetrySnapshot emits
// a 1-byte-per-actor FleetCompact string and prunes idle suspended actors from
// the JSON Actors map.
const CompactFleetThreshold = 256

// HyperFleetThreshold is the actor count above which Board switches to
// compact 1-byte-per-actor state storage (keeping full ActorView structs only
// for active/seated actors) and active-worker polling instead of storing and
// paging millions of idle ActorView structs.
const HyperFleetThreshold = 65536

const (
	compactCheckpointed byte = 1 << 0
	compactActShift          = 1
	compactActMask      byte = 0x03 << compactActShift
	compactMockSource   byte = 1 << 3
	compactUnavailable  byte = 1 << 4
	compactSeated       byte = 1 << 5
)

func encodeCompactActivity(a types.Activity) byte {
	switch a {
	case types.ActivityThinking:
		return 1 << compactActShift
	case types.ActivityAwaitingApproval:
		return 2 << compactActShift
	case types.ActivityExecuting:
		return 3 << compactActShift
	default:
		return 0
	}
}

func decodeCompactActivity(cb byte) types.Activity {
	switch (cb & compactActMask) >> compactActShift {
	case 1:
		return types.ActivityThinking
	case 2:
		return types.ActivityAwaitingApproval
	case 3:
		return types.ActivityExecuting
	default:
		return types.ActivityIdle
	}
}

func compactByteToBadgeCode(cb byte) byte {
	if (cb & compactUnavailable) != 0 {
		return 8
	}
	switch (cb & compactActMask) >> compactActShift {
	case 3:
		return 7 // Executing
	case 2:
		return 6 // AwaitingApproval
	case 1:
		return 5 // Thinking
	default:
		if (cb & compactCheckpointed) != 0 {
			return 1 // Checkpointed SUSPENDED
		}
		return 0 // NeverStarted SUSPENDED
	}
}

// ParseAttendeeIndex parses "attendee-<N>" into 1-based index N in [1..maxActors]
// without heap allocations.
func ParseAttendeeIndex(name string, maxActors int) (int, bool) {
	const prefix = "attendee-"
	if len(name) <= len(prefix) || !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	n := 0
	for i := len(prefix); i < len(name); i++ {
		ch := name[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
		if n > maxActors {
			return 0, false
		}
	}
	if n < 1 {
		return 0, false
	}
	return n, true
}

func isSeatedView(v types.ActorView) bool {
	return v.WorkerPod != "" ||
		v.State == types.StateRunning ||
		v.State == types.StateResuming ||
		v.State == types.StateSuspending
}

// Board holds the latest view of every actor and publishes each change.
type Board struct {
	publish func(types.ActorView)

	mu              sync.Mutex
	views           map[string]types.ActorView
	activeSeatCount int
	hyperCount      int
	compact         []byte
	hyperResuming   map[string]types.Activity
	hyperSuspending map[string]bool
	unavailableMsg  string
	hasUnavailable  bool

	// pubMu serializes publish calls in commit order. It is acquired while
	// mu is still held, so the order in which updates commit their views is
	// the order in which they publish them: two concurrent updates cannot
	// have their publishes interleave out of commit order.
	pubMu sync.Mutex
}

// NewBoard returns an empty Board that calls publish for each changed view.
// publish must not call any Board method: publish runs holding the lock
// that orders publishes, and a concurrent update may hold the board lock
// while it waits for that one.
func NewBoard(publish func(types.ActorView)) *Board {
	return NewBoardWithCapacity(publish, 0)
}

// NewBoardWithCapacity returns a Board sized for maxActors. When maxActors
// exceeds HyperFleetThreshold, idle actors are stored in a compact 1-byte
// array (16 MiB for 16,777,216 actors) rather than a 16.78M-entry string map.
func NewBoardWithCapacity(publish func(types.ActorView), maxActors int) *Board {
	b := &Board{
		publish: publish,
		views:   map[string]types.ActorView{},
	}
	if maxActors > HyperFleetThreshold {
		b.hyperCount = maxActors
		b.compact = make([]byte, maxActors)
		b.hyperResuming = make(map[string]types.Activity)
		b.hyperSuspending = make(map[string]bool)
	}
	return b
}

// IsHyperMode reports whether this Board uses compact byte-array storage.
func (b *Board) IsHyperMode() bool {
	return b.hyperCount > 0
}

func (b *Board) hyperViewLocked(name string, idx int) types.ActorView {
	if v, ok := b.views[name]; ok {
		return v
	}
	cb := b.compact[idx-1]
	state := types.StateSuspended
	detail := ""
	neverStarted := (cb & compactCheckpointed) == 0
	if (cb & compactUnavailable) != 0 {
		state = types.StateUnavailable
		detail = b.unavailableMsg
		neverStarted = false
	}
	var src types.ReplySource
	if (cb & compactMockSource) != 0 {
		src = types.SourceMock
	}
	return types.ActorView{
		ActorID:      name,
		State:        state,
		StateDetail:  detail,
		NeverStarted: neverStarted,
		Activity:     decodeCompactActivity(cb),
		Source:       src,
	}
}

func (b *Board) syncCompactFromViewLocked(idx int, v types.ActorView) {
	if idx < 1 || idx > len(b.compact) {
		return
	}
	cb := b.compact[idx-1]
	if !v.NeverStarted && v.State != types.StateUnknown && v.State != types.StateUnavailable {
		cb |= compactCheckpointed
	}
	cb = (cb &^ compactActMask) | encodeCompactActivity(v.Activity)
	if v.Source == types.SourceMock {
		cb |= compactMockSource
	} else if v.Source == "" {
		cb &^= compactMockSource
	}
	if v.State == types.StateUnavailable {
		cb |= compactUnavailable
		b.hasUnavailable = true
	} else {
		cb &^= compactUnavailable
	}
	if isSeatedView(v) {
		cb |= compactSeated
	} else {
		cb &^= compactSeated
	}
	b.compact[idx-1] = cb
}

func (b *Board) shouldRetainInHyperMap(v types.ActorView) bool {
	return v.WorkerPod != "" ||
		v.State == types.StateRunning ||
		v.State == types.StateResuming ||
		v.State == types.StateSuspending ||
		v.State == types.StateCrashed ||
		v.VolumeShare != "" ||
		(v.Source != "" && v.Source != types.SourceMock)
}

// update applies fn to each named view and publishes the ones that changed.
func (b *Board) update(names []string, fn func(*types.ActorView)) {
	var changed []types.ActorView
	b.mu.Lock()
	for _, name := range names {
		if b.hyperCount > 0 {
			if idx, ok := ParseAttendeeIndex(name, b.hyperCount); ok {
				old := b.hyperViewLocked(name, idx)
				_, wasInMap := b.views[name]
				wasSeated := wasInMap && isSeatedView(old)
				v := old
				fn(&v)
				if v != old {
					b.syncCompactFromViewLocked(idx, v)
					nowSeated := isSeatedView(v)
					if b.shouldRetainInHyperMap(v) {
						b.views[name] = v
						if !wasSeated && nowSeated {
							b.activeSeatCount++
						} else if wasSeated && !nowSeated {
							b.activeSeatCount--
						}
					} else if wasInMap {
						delete(b.views, name)
						if wasSeated {
							b.activeSeatCount--
						}
					}
					changed = append(changed, v)
				}
				continue
			}
		}

		old, ok := b.views[name]
		wasSeated := ok && isSeatedView(old)
		if !ok {
			old = types.ActorView{ActorID: name, State: types.StateUnknown}
		}
		v := old
		fn(&v)
		if !ok || v != old {
			b.views[name] = v
			nowSeated := isSeatedView(v)
			if !wasSeated && nowSeated {
				b.activeSeatCount++
			} else if wasSeated && !nowSeated {
				b.activeSeatCount--
			}
			changed = append(changed, v)
		}
	}
	b.pubMu.Lock()
	b.mu.Unlock()
	for _, v := range changed {
		b.publish(v)
	}
	b.pubMu.Unlock()
}

func (b *Board) names() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Collect(maps.Keys(b.views))
}

// SetStatuses applies one ateapi listing. Known actors missing from it
// become UNKNOWN.
func (b *Board) SetStatuses(statuses []Status) {
	if b.hyperCount > 0 {
		b.SetActiveStatuses(statuses)
		return
	}

	byName := make(map[string]Status, len(statuses))
	for _, s := range statuses {
		byName[s.Name] = s
	}

	var changed []types.ActorView
	b.mu.Lock()
	apply := func(name string, s Status, ok bool) {
		old, existed := b.views[name]
		if !existed {
			old = types.ActorView{ActorID: name, State: types.StateUnknown}
		}
		v := old
		if !ok {
			v.State, v.StateDetail, v.WorkerPod, v.SnapshotURI, v.NeverStarted = types.StateUnknown, "not found in ateapi", "", "", false
			v.VolumeType, v.VolumeShare, v.VolumeURI = "", "", ""
		} else {
			v.State, v.StateDetail, v.WorkerPod = s.State, "", s.WorkerPod
			v.SnapshotURI = s.SnapshotURI
			if s.InProgressSnapshotURI != "" {
				v.SnapshotURI = s.InProgressSnapshotURI
			}
			v.VolumeType, v.VolumeShare, v.VolumeURI = s.VolumeType, s.VolumeShare, s.VolumeURI
			v.NeverStarted = s.State == types.StateSuspended &&
				(s.SnapshotURI == "" || strings.Contains(s.SnapshotURI, "/tags/")) &&
				(s.InProgressSnapshotURI == "" || strings.Contains(s.InProgressSnapshotURI, "/tags/"))
		}
		if !existed || v != old {
			b.views[name] = v
			changed = append(changed, v)
		}
	}
	for name, s := range byName {
		apply(name, s, true)
	}
	for name := range b.views {
		if _, ok := byName[name]; !ok {
			apply(name, Status{}, false)
		}
	}
	activeSeats := 0
	for _, v := range b.views {
		if isSeatedView(v) {
			activeSeats++
		}
	}
	b.activeSeatCount = activeSeats
	slices.SortFunc(changed, func(a, b types.ActorView) int {
		return strings.Compare(a.ActorID, b.ActorID)
	})
	b.pubMu.Lock()
	b.mu.Unlock()
	for _, v := range changed {
		b.publish(v)
	}
	b.pubMu.Unlock()
}

// SetActiveStatuses updates a hyper-mode Board from the list of currently
// seated actors reported by ListWorkers + ListWorkerActorAssignments. Actors
// previously seated that are no longer in statuses transition cleanly back to
// checkpointed SUSPENDED in the compact array and are evicted from b.views.
func (b *Board) SetActiveStatuses(statuses []Status) {
	if b.hyperCount == 0 {
		b.SetStatuses(statuses)
		return
	}

	byName := make(map[string]Status, len(statuses))
	for _, s := range statuses {
		byName[s.Name] = s
	}

	var changed []types.ActorView
	b.mu.Lock()
	if b.hasUnavailable || b.unavailableMsg != "" {
		b.unavailableMsg = ""
		b.hasUnavailable = false
		for i := range b.compact {
			b.compact[i] &^= compactUnavailable
		}
	}

	for name, s := range byName {
		idx, validIdx := ParseAttendeeIndex(name, b.hyperCount)
		var old types.ActorView
		existed := false
		if cur, ok := b.views[name]; ok {
			old = cur
			existed = true
		} else if validIdx {
			old = b.hyperViewLocked(name, idx)
			existed = true
		} else {
			old = types.ActorView{ActorID: name, State: types.StateUnknown}
		}
		v := old
		if b.hyperSuspending[name] {
			v.State, v.StateDetail, v.WorkerPod = types.StateSuspending, "", s.WorkerPod
		} else {
			v.State, v.StateDetail, v.WorkerPod = s.State, "", s.WorkerPod
			if pendAct, wasResuming := b.hyperResuming[name]; wasResuming {
				delete(b.hyperResuming, name)
				if (v.Activity == types.ActivityIdle || v.Activity == "") && pendAct != types.ActivityIdle {
					v.Activity = pendAct
				}
			}
		}
		if s.SnapshotURI != "" {
			v.SnapshotURI = s.SnapshotURI
		}
		if s.InProgressSnapshotURI != "" {
			v.SnapshotURI = s.InProgressSnapshotURI
		}
		if s.VolumeType != "" {
			v.VolumeType, v.VolumeShare, v.VolumeURI = s.VolumeType, s.VolumeShare, s.VolumeURI
		}
		v.NeverStarted = false
		if validIdx {
			b.compact[idx-1] |= compactCheckpointed
			b.syncCompactFromViewLocked(idx, v)
		}
		if !existed || v != old {
			b.views[name] = v
			changed = append(changed, v)
		} else {
			b.views[name] = v
		}
	}

	for name, old := range b.views {
		if _, stillActive := byName[name]; stillActive {
			continue
		}
		if _, resuming := b.hyperResuming[name]; resuming {
			continue
		}
		if old.State == types.StateSuspended && old.WorkerPod == "" && !b.hyperSuspending[name] {
			continue
		}
		wasSeatedOrSuspending := old.WorkerPod != "" || b.hyperSuspending[name] || !old.NeverStarted
		delete(b.hyperSuspending, name)
		v := old
		v.State = types.StateSuspended
		v.StateDetail = ""
		v.WorkerPod = ""
		if wasSeatedOrSuspending {
			v.NeverStarted = false
			if v.Activity == types.ActivityExecuting {
				v.Activity = types.ActivityIdle
			} else if v.Activity == types.ActivityThinking {
				v.Activity = types.ActivityAwaitingApproval
			}
		} else {
			v.Activity = types.ActivityIdle
		}
		if idx, validIdx := ParseAttendeeIndex(name, b.hyperCount); validIdx {
			if !v.NeverStarted {
				b.compact[idx-1] |= compactCheckpointed
			}
			b.syncCompactFromViewLocked(idx, v)
		}
		if b.shouldRetainInHyperMap(v) {
			b.views[name] = v
		} else {
			delete(b.views, name)
		}
		if v != old {
			changed = append(changed, v)
		}
	}

	activeSeats := 0
	for _, v := range b.views {
		if isSeatedView(v) {
			activeSeats++
		}
	}
	b.activeSeatCount = activeSeats
	b.pubMu.Lock()
	b.mu.Unlock()
	for _, v := range changed {
		b.publish(v)
	}
	b.pubMu.Unlock()
}

// SetUnavailable marks every known actor's state as unreadable.
func (b *Board) SetUnavailable(reason string) {
	if b.hyperCount > 0 {
		b.mu.Lock()
		b.unavailableMsg = reason
		b.hasUnavailable = true
		for i := range b.compact {
			b.compact[i] |= compactUnavailable
		}
		var changed []types.ActorView
		for name, old := range b.views {
			v := old
			v.State, v.StateDetail, v.WorkerPod, v.NeverStarted = types.StateUnavailable, reason, "", false
			if v != old {
				b.views[name] = v
				changed = append(changed, v)
			}
		}
		b.activeSeatCount = 0
		b.pubMu.Lock()
		b.mu.Unlock()
		for _, v := range changed {
			b.publish(v)
		}
		b.pubMu.Unlock()
		return
	}

	b.update(b.names(), func(v *types.ActorView) {
		v.State, v.StateDetail, v.WorkerPod, v.NeverStarted = types.StateUnavailable, reason, "", false
	})
}

// SetActivity records what the actor is doing for its attendee.
func (b *Board) SetActivity(actor string, a types.Activity) {
	b.update([]string{actor}, func(v *types.ActorView) {
		if b.hyperCount > 0 {
			if (v.State == types.StateResuming || v.State == types.StateRunning) && a == types.ActivityIdle && v.WorkerPod == "" {
				delete(b.hyperResuming, actor)
				v.State = types.StateSuspended
			} else if v.State == types.StateResuming && v.WorkerPod != "" {
				v.State = types.StateRunning
			}
			if v.WorkerPod != "" || a == types.ActivityIdle || v.State == types.StateSuspended {
				delete(b.hyperResuming, actor)
			} else if _, wasResuming := b.hyperResuming[actor]; wasResuming {
				b.hyperResuming[actor] = a
			}
		}
		v.Activity = a
	})
}

// SetSource records where the actor's last reply came from.
func (b *Board) SetSource(actor string, s types.ReplySource) {
	b.update([]string{actor}, func(v *types.ActorView) { v.Source = s })
}

// BeginHyperResume marks an unseated hyper-mode actor as StateResuming until
// ListActiveSeats observes its worker pod assignment, then promotes it to targetActivity.
// If the actor is already seated and running on a worker pod, it updates Activity directly.
func (b *Board) BeginHyperResume(actor string, targetActivity types.Activity) {
	if b.hyperCount == 0 {
		b.SetActivity(actor, targetActivity)
		return
	}
	b.update([]string{actor}, func(v *types.ActorView) {
		if v.WorkerPod != "" && v.State == types.StateRunning && !b.hyperSuspending[actor] {
			delete(b.hyperResuming, actor)
			v.Activity = targetActivity
			return
		}
		delete(b.hyperSuspending, actor)
		b.hyperResuming[actor] = targetActivity
		v.State = types.StateResuming
		v.StateDetail = ""
		if targetActivity == types.ActivityExecuting {
			v.Activity = types.ActivityExecuting
		} else {
			v.Activity = types.ActivityIdle
		}
	})
}

// PromoteHyperResume transitions an actor still in hyperResuming into StateRunning
// with its pending targetActivity if the container began executing before a poll tick.
func (b *Board) PromoteHyperResume(actor string) {
	if b.hyperCount == 0 {
		return
	}
	b.update([]string{actor}, func(v *types.ActorView) {
		pendAct, ok := b.hyperResuming[actor]
		if !ok {
			return
		}
		delete(b.hyperResuming, actor)
		if v.WorkerPod != "" && v.State == types.StateResuming {
			v.State = types.StateRunning
		}
		if v.Activity == types.ActivityIdle && pendAct != types.ActivityIdle {
			v.Activity = pendAct
		}
	})
}

// BeginHyperSuspend marks a seated hyper-mode actor as StateSuspending while
// its SuspendActor checkpoint RPC is in flight.
func (b *Board) BeginHyperSuspend(actor string) {
	if b.hyperCount == 0 {
		return
	}
	b.update([]string{actor}, func(v *types.ActorView) {
		delete(b.hyperResuming, actor)
		if v.State == types.StateSuspended && v.WorkerPod == "" {
			return
		}
		b.hyperSuspending[actor] = true
		v.State = types.StateSuspending
		v.StateDetail = ""
	})
}

// FinishHyperSuspend completes an in-flight hyper-mode SuspendActor transition.
func (b *Board) FinishHyperSuspend(actor string, succeeded bool) {
	if b.hyperCount == 0 {
		return
	}
	b.update([]string{actor}, func(v *types.ActorView) {
		delete(b.hyperSuspending, actor)
		if succeeded {
			if v.Activity == types.ActivityExecuting {
				v.Activity = types.ActivityIdle
			} else if v.Activity == types.ActivityThinking {
				v.Activity = types.ActivityAwaitingApproval
			}
			v.State = types.StateSuspended
			v.StateDetail = ""
			v.WorkerPod = ""
			v.NeverStarted = false
			return
		}
		if v.State == types.StateSuspending {
			if v.WorkerPod != "" {
				v.State = types.StateRunning
			} else {
				if v.Activity == types.ActivityExecuting || v.Activity == types.ActivityThinking {
					v.Activity = types.ActivityIdle
				}
				v.State = types.StateSuspended
			}
		}
	})
}

// Reset clears gateway-side activity and reply source on every actor.
func (b *Board) Reset() {
	if b.hyperCount > 0 {
		b.mu.Lock()
		clear(b.hyperResuming)
		clear(b.hyperSuspending)
		for i := range b.compact {
			b.compact[i] &^= (compactActMask | compactMockSource)
		}
		names := slices.Collect(maps.Keys(b.views))
		b.mu.Unlock()
		if len(names) > 0 {
			b.update(names, func(v *types.ActorView) {
				if v.State == types.StateResuming && v.WorkerPod == "" {
					v.State = types.StateSuspended
				}
				v.Activity, v.Source = types.ActivityIdle, ""
			})
		}
		return
	}
	b.update(b.names(), func(v *types.ActorView) { v.Activity, v.Source = types.ActivityIdle, "" })
}

// ClearUnleasedActivity resets gateway-side activity to ActivityIdle on every
// actor that is not currently leased by a live human attendee.
func (b *Board) ClearUnleasedActivity(isLeased func(string) bool) {
	b.ClearUnleasedActivityWithStride(isLeased, 0)
}

// ClearUnleasedActivityWithStride resets gateway-side activity to ActivityIdle
// on every actor not protected by isLeased or humanStride.
func (b *Board) ClearUnleasedActivityWithStride(isLeased func(string) bool, humanStride int) {
	b.ClearUnleasedActivityIndices(nil, isLeased, humanStride)
}

// ClearUnleasedActivityIndices resets gateway-side activity to ActivityIdle on
// every actor not in protectedIndices or humanStride, avoiding per-actor string
// formatting and mutex callbacks across 16.78M hyper-mode entries.
func (b *Board) ClearUnleasedActivityIndices(protectedIndices map[int]bool, isLeased func(string) bool, humanStride int) {
	if b.hyperCount > 0 {
		b.mu.Lock()
		for i := range b.compact {
			if (b.compact[i] & compactActMask) == 0 {
				continue
			}
			idx := i + 1
			if humanStride > 1 && idx%humanStride == 0 {
				continue
			}
			if len(protectedIndices) > 0 && protectedIndices[idx] {
				continue
			}
			if protectedIndices == nil && isLeased != nil && isLeased(fmt.Sprintf("attendee-%02d", idx)) {
				continue
			}
			b.compact[i] &^= compactActMask
		}
		var targetNames []string
		for name, v := range b.views {
			_, isResuming := b.hyperResuming[name]
			if (v.Activity == types.ActivityIdle || v.Activity == "") && !isResuming {
				continue
			}
			if isLeased != nil && isLeased(name) {
				continue
			}
			delete(b.hyperResuming, name)
			targetNames = append(targetNames, name)
		}
		b.mu.Unlock()
		if len(targetNames) > 0 {
			b.update(targetNames, func(v *types.ActorView) {
				if v.State == types.StateResuming && v.WorkerPod == "" {
					v.State = types.StateSuspended
				}
				v.Activity = types.ActivityIdle
			})
		}
		return
	}

	var targetNames []string
	b.mu.Lock()
	for name, v := range b.views {
		if v.Activity == types.ActivityIdle || v.Activity == "" {
			continue
		}
		if isLeased != nil && isLeased(name) {
			continue
		}
		targetNames = append(targetNames, name)
	}
	b.mu.Unlock()
	if len(targetNames) > 0 {
		b.update(targetNames, func(v *types.ActorView) {
			v.Activity = types.ActivityIdle
		})
	}
}

// View returns one actor's view.
func (b *Board) View(actor string) (types.ActorView, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.views[actor]; ok {
		return v, true
	}
	if b.hyperCount > 0 {
		if idx, ok := ParseAttendeeIndex(actor, b.hyperCount); ok {
			return b.hyperViewLocked(actor, idx), true
		}
	}
	return types.ActorView{}, false
}

// Views returns a copy of every tracked view (in hyper mode, only active/retained views).
func (b *Board) Views() map[string]types.ActorView {
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.views)
}

// ActiveSeatCount returns the number of actors currently occupying or
// transitioning on a worker pod seat according to the board's latest view.
func (b *Board) ActiveSeatCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.activeSeatCount
}

// EncodeBadgeCode maps an ActorView and lease status to a 5-bit compact code:
// bits 0..3 = badge state (0..9), bit 4 (16) = leased by a live attendee.
func EncodeBadgeCode(v types.ActorView, leased bool) byte {
	var code byte
	switch v.State {
	case types.StateCrashed, types.StateUnavailable:
		code = 8
	case types.StateSuspending:
		code = 3
	default:
		switch v.Activity {
		case types.ActivityExecuting:
			code = 7
		case types.ActivityAwaitingApproval:
			code = 6
		case types.ActivityThinking:
			code = 5
		default:
			switch v.State {
			case types.StateSuspended:
				if v.NeverStarted {
					code = 0
				} else {
					code = 1
				}
			case types.StateResuming:
				code = 2
			case types.StateRunning:
				code = 4
			default:
				code = 9
			}
		}
	}
	if leased {
		code |= 16
	}
	return code
}

// TelemetrySnapshot returns the actor map and compact fleet encoding for /api/stage/telemetry.
func (b *Board) TelemetrySnapshot(maxActors int, isLeased func(string) bool) (actors map[string]types.ActorView, compact string, runningCount, suspendedCount int) {
	return b.TelemetrySnapshotWithLeases(maxActors, isLeased, nil)
}

// TelemetrySnapshotWithLeases is TelemetrySnapshot with an optional explicit
// list of leased attendee IDs so hyper-mode snapshots avoid calling isLeased
// 16.78 million times.
func (b *Board) TelemetrySnapshotWithLeases(maxActors int, isLeased func(string) bool, leasedIDs []string) (actors map[string]types.ActorView, compact string, runningCount, suspendedCount int) {
	actors, compact, runningCount, suspendedCount, _, _, _ = b.TelemetrySnapshotOptions(maxActors, isLeased, leasedIDs, true)
	return actors, compact, runningCount, suspendedCount
}

// TelemetrySnapshotOptions returns the actor map, optional compact string, and
// fleet summary counters. When includeCompact is false in hyper mode, it omits
// allocating the 16.78M-byte ASCII string while still computing exact counters.
func (b *Board) TelemetrySnapshotOptions(maxActors int, isLeased func(string) bool, leasedIDs []string, includeCompact bool) (actors map[string]types.ActorView, compact string, runningCount, suspendedCount, activeCohortCount, snapshottedCount, leasedCount int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.hyperCount > 0 && maxActors > HyperFleetThreshold {
		n := min(maxActors, len(b.compact))
		var buf []byte
		if includeCompact {
			buf = make([]byte, maxActors)
		}
		for i := 0; i < n; i++ {
			code := compactByteToBadgeCode(b.compact[i])
			if buf != nil {
				buf[i] = '@' + code
			}
			if code == 1 {
				snapshottedCount++
			} else if code >= 2 && code <= 8 {
				activeCohortCount++
			}
		}
		if buf != nil {
			for i := n; i < maxActors; i++ {
				buf[i] = '@' + 9
			}
		}

		nonSuspendedInViews := 0
		actors = make(map[string]types.ActorView, len(b.views)+len(leasedIDs)+1)
		for name, v := range b.views {
			leased := isLeased != nil && isLeased(name)
			if leased {
				leasedCount++
			}
			v.Leased = leased
			if v.State == types.StateRunning {
				runningCount++
			}
			if v.State != types.StateSuspended {
				nonSuspendedInViews++
			}
			if idx, ok := ParseAttendeeIndex(name, maxActors); ok {
				var oldCode byte
				if idx-1 < n {
					oldCode = compactByteToBadgeCode(b.compact[idx-1])
				}
				newRaw := EncodeBadgeCode(v, leased)
				newCode := newRaw & 15
				if oldCode != newCode {
					if oldCode == 1 {
						snapshottedCount--
					} else if oldCode >= 2 && oldCode <= 8 {
						activeCohortCount--
					}
					if newCode == 1 {
						snapshottedCount++
					} else if newCode >= 2 && newCode <= 8 {
						activeCohortCount++
					}
				}
				if buf != nil {
					buf[idx-1] = '@' + newRaw
				}
			}
			if v.WorkerPod != "" || leased || v.State == types.StateCrashed {
				actors[name] = v
			}
		}
		for _, id := range leasedIDs {
			if idx, ok := ParseAttendeeIndex(id, maxActors); ok {
				if _, inMap := b.views[id]; !inMap {
					leasedCount++
					v := b.hyperViewLocked(id, idx)
					v.Leased = true
					if buf != nil {
						buf[idx-1] = '@' + EncodeBadgeCode(v, true)
					}
					actors[id] = v
				}
			}
		}
		if (b.unavailableMsg == "") && maxActors >= nonSuspendedInViews {
			suspendedCount = maxActors - nonSuspendedInViews
		}
		if buf != nil {
			compact = string(buf)
		}
		return actors, compact, runningCount, suspendedCount, activeCohortCount, snapshottedCount, leasedCount
	}

	for _, v := range b.views {
		switch v.State {
		case types.StateRunning:
			runningCount++
		case types.StateSuspended:
			suspendedCount++
		}
	}

	if maxActors <= CompactFleetThreshold {
		actors = make(map[string]types.ActorView, len(b.views))
		for k, v := range b.views {
			if isLeased != nil && isLeased(k) {
				v.Leased = true
				leasedCount++
			}
			code := EncodeBadgeCode(v, v.Leased) & 15
			if code == 1 {
				snapshottedCount++
			} else if code >= 2 && code <= 8 {
				activeCohortCount++
			}
			actors[k] = v
		}
		return actors, "", runningCount, suspendedCount, activeCohortCount, snapshottedCount, leasedCount
	}

	buf := make([]byte, maxActors)
	actors = make(map[string]types.ActorView, 256)
	for i := 1; i <= maxActors; i++ {
		id := fmt.Sprintf("attendee-%02d", i)
		v, ok := b.views[id]
		if !ok {
			v = types.ActorView{ActorID: id, State: types.StateUnknown}
		}
		leased := isLeased != nil && isLeased(id)
		if leased {
			leasedCount++
		}
		v.Leased = leased
		raw := EncodeBadgeCode(v, leased)
		code := raw & 15
		if code == 1 {
			snapshottedCount++
		} else if code >= 2 && code <= 8 {
			activeCohortCount++
		}
		buf[i-1] = '@' + raw

		isActive := v.WorkerPod != "" ||
			v.State == types.StateRunning ||
			v.State == types.StateResuming ||
			v.State == types.StateSuspending ||
			v.State == types.StateCrashed ||
			leased
		if ok && (isActive || i == 1) {
			actors[id] = v
		}
	}
	if includeCompact {
		compact = string(buf)
	}
	return actors, compact, runningCount, suspendedCount, activeCohortCount, snapshottedCount, leasedCount
}

// FleetBinaryHeaderBytes is the fixed 24-byte header prefix of /api/stage/fleet.bin:
// [0..3]  magic "SWFL"
// [4..7]  version uint32 LE (= 1)
// [8..11] actorCount uint32 LE
// [12..15] activeCohortCount uint32 LE
// [16..19] snapshottedCount uint32 LE
// [20..23] leasedCount uint32 LE
// followed by actorCount raw bytes (bits 0..3 = badge code 0..9, bit 4 = leased, bit 5 = seated on pod).
const FleetBinaryHeaderBytes = 24

// FleetBinarySnapshot encodes the entire fleet into a compact binary frame for
// zero-JSON-allocation WebGL2 / ImageData rendering on /stage.
func (b *Board) FleetBinarySnapshot(maxActors int, isLeased func(string) bool, leasedIDs []string) []byte {
	if maxActors < 0 {
		maxActors = 0
	}
	out := make([]byte, FleetBinaryHeaderBytes+maxActors)
	copy(out[0:4], "SWFL")
	binary.LittleEndian.PutUint32(out[4:8], 1)
	binary.LittleEndian.PutUint32(out[8:12], uint32(maxActors)) //nolint:gosec

	payload := out[FleetBinaryHeaderBytes:]
	var activeCount, snapshottedCount, leasedCount uint32

	b.mu.Lock()
	if b.hyperCount > 0 && maxActors > HyperFleetThreshold {
		n := min(maxActors, len(b.compact))
		for i := 0; i < n; i++ {
			code := compactByteToBadgeCode(b.compact[i])
			payload[i] = code
			if code == 1 {
				snapshottedCount++
			} else if code >= 2 && code <= 8 {
				activeCount++
			}
		}
		for i := n; i < maxActors; i++ {
			payload[i] = 9
		}
		for name, v := range b.views {
			idx, ok := ParseAttendeeIndex(name, maxActors)
			if !ok {
				continue
			}
			leased := isLeased != nil && isLeased(name)
			if leased {
				leasedCount++
			}
			oldCode := payload[idx-1] & 15
			raw := EncodeBadgeCode(v, leased)
			newCode := raw & 15
			if oldCode != newCode {
				if oldCode == 1 && snapshottedCount > 0 {
					snapshottedCount--
				} else if oldCode >= 2 && oldCode <= 8 && activeCount > 0 {
					activeCount--
				}
				if newCode == 1 {
					snapshottedCount++
				} else if newCode >= 2 && newCode <= 8 {
					activeCount++
				}
			}
			if v.WorkerPod != "" || isSeatedView(v) {
				raw |= 32
			}
			payload[idx-1] = raw
		}
		for _, id := range leasedIDs {
			if idx, ok := ParseAttendeeIndex(id, maxActors); ok {
				if _, inMap := b.views[id]; !inMap {
					leasedCount++
					payload[idx-1] |= 16
				}
			}
		}
	} else {
		for i := 1; i <= maxActors; i++ {
			id := fmt.Sprintf("attendee-%02d", i)
			v, ok := b.views[id]
			if !ok {
				v = types.ActorView{ActorID: id, State: types.StateUnknown}
			}
			leased := isLeased != nil && isLeased(id)
			if leased {
				leasedCount++
			}
			raw := EncodeBadgeCode(v, leased)
			code := raw & 15
			if code == 1 {
				snapshottedCount++
			} else if code >= 2 && code <= 8 {
				activeCount++
			}
			if v.WorkerPod != "" || isSeatedView(v) {
				raw |= 32
			}
			payload[i-1] = raw
		}
	}
	b.mu.Unlock()

	binary.LittleEndian.PutUint32(out[12:16], activeCount)
	binary.LittleEndian.PutUint32(out[16:20], snapshottedCount)
	binary.LittleEndian.PutUint32(out[20:24], leasedCount)
	return out
}

// HyperWorkerTelemetrySnapshot builds compact per-pod 1-based actor number
// slices and summary counters for /api/stage/telemetry?compact=0 in hyper mode
// when >8,192 seats are occupied, avoiding a 262k-entry ActorView map copy and
// 262k string sort on every poll tick. If any seated actor has a non-numeric ID,
// ok is false and callers fall back to TelemetrySnapshotOptions.
func (b *Board) HyperWorkerTelemetrySnapshot(maxActors, seatsPerWorker int, isLeased func(string) bool, leasedIDs []string) (
	podActorNums map[string][]int,
	compactActors map[string]types.ActorView,
	runningCount, suspendedCount, activeCohortCount, snapshottedCount, leasedCount int,
	ok bool,
) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.hyperCount == 0 || maxActors <= HyperFleetThreshold {
		return nil, nil, 0, 0, 0, 0, 0, false
	}

	n := min(maxActors, len(b.compact))
	for i := 0; i < n; i++ {
		code := compactByteToBadgeCode(b.compact[i])
		if code == 1 {
			snapshottedCount++
		} else if code >= 2 && code <= 8 {
			activeCohortCount++
		}
	}

	capPerPod := max(8, seatsPerWorker)
	packedByPod := make(map[string][]uint32, 4096)
	compactActors = make(map[string]types.ActorView, len(leasedIDs)+8)
	nonSuspendedInViews := 0

	seatPrio := func(s types.SubstrateState) uint32 {
		switch s {
		case types.StateRunning:
			return 0
		case types.StateResuming:
			return 1
		case types.StateSuspending:
			return 2
		default:
			return 3
		}
	}

	for name, v := range b.views {
		leased := isLeased != nil && isLeased(name)
		if leased {
			leasedCount++
		}
		v.Leased = leased
		if v.State == types.StateRunning {
			runningCount++
		}
		if v.State != types.StateSuspended {
			nonSuspendedInViews++
		}

		idx, validIdx := ParseAttendeeIndex(name, maxActors)
		if validIdx {
			var oldCode byte
			if idx-1 < n {
				oldCode = compactByteToBadgeCode(b.compact[idx-1])
			}
			newCode := EncodeBadgeCode(v, leased) & 15
			if oldCode != newCode {
				if oldCode == 1 {
					snapshottedCount--
				} else if oldCode >= 2 && oldCode <= 8 {
					activeCohortCount--
				}
				if newCode == 1 {
					snapshottedCount++
				} else if newCode >= 2 && newCode <= 8 {
					activeCohortCount++
				}
			}
		} else if v.WorkerPod != "" {
			return nil, nil, 0, 0, 0, 0, 0, false
		}

		if v.WorkerPod != "" && validIdx {
			sl := packedByPod[v.WorkerPod]
			if sl == nil {
				sl = make([]uint32, 0, capPerPod)
			}
			packedByPod[v.WorkerPod] = append(sl, (seatPrio(v.State)<<25)|uint32(idx))
		}
		if leased || v.State == types.StateCrashed || name == "attendee-01" {
			compactActors[name] = v
		}
	}

	for _, id := range leasedIDs {
		if idx, validIdx := ParseAttendeeIndex(id, maxActors); validIdx {
			if _, inMap := b.views[id]; !inMap {
				leasedCount++
				v := b.hyperViewLocked(id, idx)
				v.Leased = true
				compactActors[id] = v
			}
		}
	}
	if _, hasFirst := compactActors["attendee-01"]; !hasFirst {
		compactActors["attendee-01"] = b.hyperViewLocked("attendee-01", 1)
	}
	if b.unavailableMsg == "" && maxActors >= nonSuspendedInViews {
		suspendedCount = maxActors - nonSuspendedInViews
	}

	podActorNums = make(map[string][]int, len(packedByPod))
	for pod, packed := range packedByPod {
		slices.Sort(packed)
		if seatsPerWorker > 0 && len(packed) > seatsPerWorker {
			packed = packed[:seatsPerWorker]
		}
		nums := make([]int, len(packed))
		for i, pv := range packed {
			nums[i] = int(pv & 0x1FFFFFF)
		}
		podActorNums[pod] = nums
	}

	return podActorNums, compactActors, runningCount, suspendedCount, activeCohortCount, snapshottedCount, leasedCount, true
}

// IsCheckpointedIndex reports whether the 1-based actor index in hyper mode
// already has an actor-specific checkpoint (as opposed to an untouched
// golden-snapshot NeverStarted actor), allowing SimulationRunner to pace
// STS-backed checkpoint restores well under project STS token-exchange quotas.
func (b *Board) IsCheckpointedIndex(idx int) bool {
	if idx <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hyperCount == 0 || idx > len(b.compact) {
		return false
	}
	return (b.compact[idx-1] & compactCheckpointed) != 0
}

// IsSeatedIndex reports whether the 1-based actor index is currently occupying
// or transitioning on a worker pod seat.
func (b *Board) IsSeatedIndex(idx int) bool {
	if idx <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hyperCount == 0 || idx > len(b.compact) {
		v, ok := b.views[fmt.Sprintf("attendee-%02d", idx)]
		return ok && isSeatedView(v)
	}
	return (b.compact[idx-1] & compactSeated) != 0
}

// MarkCheckpointedBatch marks a batch of 1-based actor indices as having a
// completed actor-owned checkpoint in hyper mode.
func (b *Board) MarkCheckpointedBatch(indices []int) {
	if len(indices) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hyperCount == 0 {
		return
	}
	for _, idx := range indices {
		if idx >= 1 && idx <= len(b.compact) {
			b.compact[idx-1] |= compactCheckpointed
		}
	}
}

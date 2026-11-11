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
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// Board holds the latest view of every actor and publishes each change.
type Board struct {
	publish func(types.ActorView)

	mu    sync.Mutex
	views map[string]types.ActorView

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
	return &Board{publish: publish, views: map[string]types.ActorView{}}
}

// update applies fn to each named view and publishes the ones that changed.
func (b *Board) update(names []string, fn func(*types.ActorView)) {
	var changed []types.ActorView
	b.mu.Lock()
	for _, name := range names {
		old, ok := b.views[name]
		if !ok {
			old = types.ActorView{ActorID: name, State: types.StateUnknown}
		}
		v := old
		fn(&v)
		if !ok || v != old {
			b.views[name] = v
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
			v.NeverStarted = s.State == types.StateSuspended && s.SnapshotURI == "" && s.InProgressSnapshotURI == ""
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

// SetUnavailable marks every known actor's state as unreadable.
func (b *Board) SetUnavailable(reason string) {
	b.update(b.names(), func(v *types.ActorView) {
		v.State, v.StateDetail, v.WorkerPod, v.NeverStarted = types.StateUnavailable, reason, "", false
	})
}

// SetActivity records what the actor is doing for its attendee.
func (b *Board) SetActivity(actor string, a types.Activity) {
	b.update([]string{actor}, func(v *types.ActorView) { v.Activity = a })
}

// SetSource records where the actor's last reply came from.
func (b *Board) SetSource(actor string, s types.ReplySource) {
	b.update([]string{actor}, func(v *types.ActorView) { v.Source = s })
}

// SeedSimulatedActivity records both activity and reply source in a single board update.
func (b *Board) SeedSimulatedActivity(actor string, a types.Activity, s types.ReplySource) {
	b.update([]string{actor}, func(v *types.ActorView) {
		v.Activity = a
		v.Source = s
	})
}

// Reset clears gateway-side activity and reply source on every actor.
func (b *Board) Reset() {
	b.update(b.names(), func(v *types.ActorView) { v.Activity, v.Source = types.ActivityIdle, "" })
}

// ClearUnleasedActivity resets gateway-side activity to ActivityIdle on every
// actor that is not currently leased by a live human attendee.
func (b *Board) ClearUnleasedActivity(isLeased func(string) bool) {
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
	v, ok := b.views[actor]
	return v, ok
}

// Views returns a copy of every view.
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
	count := 0
	for _, v := range b.views {
		if v.WorkerPod != "" || v.State == types.StateRunning || v.State == types.StateResuming || v.State == types.StateSuspending {
			count++
		}
	}
	return count
}

// CompactFleetThreshold is the actor count above which TelemetrySnapshot emits
// a 1-byte-per-actor FleetCompact string and prunes idle suspended actors from
// the JSON Actors map.
const CompactFleetThreshold = 256

// EncodeBadgeCode maps an ActorView and lease status to a 5-bit compact code:
// bits 0..3 = badge state (0..9), bit 4 (16) = leased by a live attendee.
func EncodeBadgeCode(v types.ActorView, leased bool) byte {
	var code byte
	switch v.State {
	case types.StateCrashed, types.StateUnavailable:
		code = 8
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
			case types.StateSuspending:
				code = 3
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

// TelemetrySnapshot returns the actor map and compact fleet encoding for /api/telemetry.
func (b *Board) TelemetrySnapshot(maxActors int, isLeased func(string) bool) (actors map[string]types.ActorView, compact string, runningCount, suspendedCount int) {
	b.mu.Lock()
	defer b.mu.Unlock()

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
			}
			actors[k] = v
		}
		return actors, "", runningCount, suspendedCount
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
		v.Leased = leased
		buf[i-1] = '@' + EncodeBadgeCode(v, leased)

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
	return actors, string(buf), runningCount, suspendedCount
}

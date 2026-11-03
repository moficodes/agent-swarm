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
	"maps"
	"slices"
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
	names := b.names()
	for _, s := range statuses {
		if _, seen := byName[s.Name]; !seen {
			names = append(names, s.Name)
		}
		byName[s.Name] = s
	}
	b.update(slices.Compact(slices.Sorted(slices.Values(names))), func(v *types.ActorView) {
		s, ok := byName[v.ActorID]
		if !ok {
			v.State, v.StateDetail, v.WorkerPod, v.SnapshotURI, v.NeverStarted = types.StateUnknown, "not found in ateapi", "", "", false
			v.VolumeType, v.VolumeShare, v.VolumeURI = "", "", ""
			return
		}
		v.State, v.StateDetail, v.WorkerPod = s.State, "", s.WorkerPod
		v.SnapshotURI = s.SnapshotURI
		if s.InProgressSnapshotURI != "" {
			v.SnapshotURI = s.InProgressSnapshotURI
		}
		v.VolumeType, v.VolumeShare, v.VolumeURI = s.VolumeType, s.VolumeShare, s.VolumeURI
		v.NeverStarted = s.State == types.StateSuspended && s.SnapshotURI == "" && s.InProgressSnapshotURI == ""
	})
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

// Reset clears gateway-side activity and reply source on every actor.
func (b *Board) Reset() {
	b.update(b.names(), func(v *types.ActorView) { v.Activity, v.Source = types.ActivityIdle, "" })
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

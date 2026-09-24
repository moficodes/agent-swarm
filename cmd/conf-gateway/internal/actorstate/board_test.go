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

package actorstate_test

import (
	"sync"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type published struct {
	mu    sync.Mutex
	views []types.ActorView
}

func (p *published) add(v types.ActorView) { p.mu.Lock(); p.views = append(p.views, v); p.mu.Unlock() }
func (p *published) take() []types.ActorView {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.views
	p.views = nil
	return v
}

func TestBoard_NeverStartedAndSnapshot(t *testing.T) {
	var pub published
	b := actorstate.NewBoard(pub.add)
	b.SetStatuses([]actorstate.Status{
		{Name: "a", State: types.StateSuspended},
		{Name: "b", State: types.StateSuspended, SnapshotURI: "gs://s/b"},
		{Name: "c", State: types.StateSuspending, SnapshotURI: "gs://s/c/1", InProgressSnapshotURI: "gs://s/c/2"},
	})
	a, _ := b.View("a")
	bv, _ := b.View("b")
	c, _ := b.View("c")
	if !a.NeverStarted || bv.NeverStarted || c.NeverStarted {
		t.Errorf("NeverStarted a=%v b=%v c=%v", a.NeverStarted, bv.NeverStarted, c.NeverStarted)
	}
	if c.SnapshotURI != "gs://s/c/2" {
		t.Errorf("c.SnapshotURI = %q, want the in-progress URI", c.SnapshotURI)
	}
	if n := len(pub.take()); n != 3 {
		t.Errorf("published %d views, want 3", n)
	}
}

func TestBoard_PublishesOnlyChanges(t *testing.T) {
	var pub published
	b := actorstate.NewBoard(pub.add)
	st := []actorstate.Status{{Name: "a", State: types.StateRunning, WorkerPod: "p1"}}
	b.SetStatuses(st)
	pub.take()

	b.SetStatuses(st)
	if got := pub.take(); len(got) != 0 {
		t.Errorf("republished unchanged view: %+v", got)
	}
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateSuspending}})
	if got := pub.take(); len(got) != 1 || got[0].State != types.StateSuspending || got[0].WorkerPod != "" {
		t.Errorf("published %+v", got)
	}
}

func TestBoard_ActivitySurvivesPolls(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetActivity("a", types.ActivityThinking)
	b.SetSource("a", types.SourceGemini)
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateRunning}})
	v, _ := b.View("a")
	if v.Activity != types.ActivityThinking || v.Source != types.SourceGemini || v.State != types.StateRunning {
		t.Errorf("view = %+v", v)
	}
}

func TestBoard_MissingActorBecomesUnknown(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateRunning}})
	b.SetStatuses(nil)
	if v, _ := b.View("a"); v.State != types.StateUnknown || v.StateDetail == "" {
		t.Errorf("view = %+v", v)
	}
}

func TestBoard_Unavailable(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateSuspended}})
	b.SetUnavailable("kubectl-ate: exit status 1")
	v, _ := b.View("a")
	if v.State != types.StateUnavailable || v.NeverStarted || v.StateDetail != "kubectl-ate: exit status 1" {
		t.Errorf("view = %+v", v)
	}
}

func TestBoard_ResetClearsActivity(t *testing.T) {
	var pub published
	b := actorstate.NewBoard(pub.add)
	b.SetActivity("a", types.ActivityAwaitingApproval)
	pub.take()
	b.Reset()
	v, _ := b.View("a")
	if v.Activity != types.ActivityIdle {
		t.Errorf("Activity = %q after Reset", v.Activity)
	}
	if len(pub.take()) != 1 {
		t.Error("Reset did not publish the change")
	}
}

func TestBoard_ViewsIsACopy(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetActivity("a", types.ActivityThinking)
	views := b.Views()
	views["a"] = types.ActorView{}
	if v, _ := b.View("a"); v.Activity != types.ActivityThinking {
		t.Error("mutating Views() changed the board")
	}
}

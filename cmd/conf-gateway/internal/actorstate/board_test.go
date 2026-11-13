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
	"encoding/binary"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/pkg/types"
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
	b.SetStatuses([]actorstate.Status{{
		Name:        "a",
		State:       types.StateRunning,
		VolumeType:  "filestore.csi.storage.gke.io",
		VolumeShare: "192.0.2.10:/s123",
		VolumeURI:   "volumepool://proj/us-west1/pool/volumes/a",
	}})
	if v, _ := b.View("a"); v.VolumeShare != "192.0.2.10:/s123" || v.VolumeType != "filestore.csi.storage.gke.io" {
		t.Fatalf("view before clear = %+v", v)
	}
	b.SetStatuses(nil)
	if v, _ := b.View("a"); v.State != types.StateUnknown || v.StateDetail == "" || v.VolumeShare != "" || v.VolumeType != "" || v.VolumeURI != "" {
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

// TestBoard_PublishOrderMatchesCommitOrder is a regression test for a race
// where two concurrent updates to the same actor could publish out of
// commit order: the first update commits, is preempted before publishing,
// the second update commits and publishes, and only then does the first
// update publish its now-stale view. It forces that interleaving by
// blocking the first publish call on a channel until the second update has
// committed (but not necessarily published), then releasing it.
func TestBoard_PublishOrderMatchesCommitOrder(t *testing.T) {
	const timeout = 5 * time.Second

	started := make(chan struct{})
	proceed := make(chan struct{})

	var mu sync.Mutex
	var order []types.ActorView
	first := true

	b := actorstate.NewBoard(func(v types.ActorView) {
		mu.Lock()
		isFirst := first
		first = false
		mu.Unlock()
		if isFirst {
			close(started)
			<-proceed
		}
		mu.Lock()
		order = append(order, v)
		mu.Unlock()
	})

	done1 := make(chan struct{})
	go func() {
		b.SetActivity("a", types.ActivityThinking)
		close(done1)
	}()

	select {
	case <-started:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for the first publish to start")
	}

	done2 := make(chan struct{})
	go func() {
		b.SetActivity("a", types.ActivityAwaitingApproval)
		close(done2)
	}()

	// Give the second update a chance to reach the board: its critical
	// section is lock operations and a map write only, so a handful of
	// scheduler yields are enough for it to run (and, once fixed, block
	// trying to acquire pubMu) before the first publish is released. This
	// state isn't otherwise observable: with the fix, the second update
	// holds b.mu until it acquires pubMu, so even View blocks until then.
	for i := 0; i < 1000; i++ {
		runtime.Gosched()
	}

	close(proceed)

	select {
	case <-done1:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for the first update to finish publishing")
	}
	select {
	case <-done2:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for the second update to finish publishing")
	}

	mu.Lock()
	got := append([]types.ActorView(nil), order...)
	mu.Unlock()

	if len(got) != 2 || got[0].Activity != types.ActivityThinking || got[1].Activity != types.ActivityAwaitingApproval {
		t.Fatalf("publish order = %+v, want [Thinking, AwaitingApproval]", got)
	}
	if v, _ := b.View("a"); got[len(got)-1] != v {
		t.Errorf("last published view %+v != current board view %+v", got[len(got)-1], v)
	}
}

func TestBoard_TelemetrySnapshotCompactAndStandard(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetStatuses([]actorstate.Status{
		{Name: "attendee-01", State: types.StateSuspended, SnapshotURI: "gs://b/1"},
		{Name: "attendee-02", State: types.StateRunning, WorkerPod: "worker-0"},
		{Name: "attendee-03", State: types.StateSuspended, SnapshotURI: "gs://b/3"},
		{Name: "attendee-16", State: types.StateSuspended},
	})
	b.SetActivity("attendee-02", types.ActivityThinking)
	b.SetActivity("attendee-03", types.ActivityAwaitingApproval)

	isLeased := func(id string) bool { return id == "attendee-16" }

	// Standard fleet (<= 256): full actors map, empty compact string.
	actors, compact, running, suspended := b.TelemetrySnapshot(50, isLeased)
	if compact != "" {
		t.Errorf("standard TelemetrySnapshot returned compact=%q, want empty", compact)
	}
	if running != 1 || suspended != 3 {
		t.Errorf("counts running=%d suspended=%d, want 1 and 3", running, suspended)
	}
	if !actors["attendee-16"].Leased {
		t.Errorf("attendee-16 Leased = false, want true")
	}

	// Large fleet (> 256): compact string populated, idle unleased actors pruned from map.
	actorsLarge, compactLarge, runningLarge, suspendedLarge := b.TelemetrySnapshot(300, isLeased)
	if len(compactLarge) != 300 {
		t.Fatalf("compactLarge len = %d, want 300", len(compactLarge))
	}
	if runningLarge != 1 || suspendedLarge != 3 {
		t.Errorf("large counts running=%d suspended=%d, want 1 and 3", runningLarge, suspendedLarge)
	}
	// attendee-01 (index 0): suspended with snapshot -> code 1 -> 'A'
	if got := compactLarge[0] - '@'; got != 1 {
		t.Errorf("attendee-01 badge code = %d, want 1", got)
	}
	// attendee-02 (index 1): running + thinking -> code 5
	if got := compactLarge[1] - '@'; got != 5 {
		t.Errorf("attendee-02 badge code = %d, want 5", got)
	}
	// attendee-03 (index 2): suspended in GCS while awaiting approval -> code 6
	if got := compactLarge[2] - '@'; got != 6 {
		t.Errorf("attendee-03 badge code = %d, want 6", got)
	}
	// attendee-16 (index 15): never started (0) + leased (16) -> code 16
	if got := compactLarge[15] - '@'; got != 16 {
		t.Errorf("attendee-16 badge code = %d, want 16", got)
	}
	if _, ok := actorsLarge["attendee-02"]; !ok {
		t.Errorf("active attendee-02 should remain in pruned actors map")
	}
	if _, ok := actorsLarge["attendee-03"]; ok {
		t.Errorf("suspended awaiting-approval attendee-03 should be pruned from large actors map")
	}
	if _, ok := actorsLarge["attendee-16"]; !ok {
		t.Errorf("leased attendee-16 should remain in pruned actors map")
	}
}

func TestBoard_HyperMode16Million(t *testing.T) {
	const total = 16777216
	b := actorstate.NewBoardWithCapacity(func(types.ActorView) {}, total)
	if !b.IsHyperMode() {
		t.Fatalf("expected IsHyperMode() == true for %d actors", total)
	}

	for _, id := range []string{"attendee-01", "attendee-02", "attendee-16777215"} {
		b.SetActivity(id, types.ActivityAwaitingApproval)
		b.SetSource(id, types.SourceMock)
	}
	b.SetActiveStatuses([]actorstate.Status{
		{Name: "attendee-4096", State: types.StateRunning, WorkerPod: "conf-workers-pod-1"},
		{Name: "attendee-8192", State: types.StateRunning, WorkerPod: "conf-workers-pod-2"},
	})
	if got := b.ActiveSeatCount(); got != 2 {
		t.Fatalf("ActiveSeatCount() = %d, want 2", got)
	}

	// Evicting attendee-8192 from active statuses marks it checkpointed SUSPENDED and frees b.views.
	b.SetActiveStatuses([]actorstate.Status{
		{Name: "attendee-4096", State: types.StateRunning, WorkerPod: "conf-workers-pod-1"},
	})
	if got := b.ActiveSeatCount(); got != 1 {
		t.Fatalf("ActiveSeatCount() after suspend = %d, want 1", got)
	}
	v8192, ok := b.View("attendee-8192")
	if !ok || v8192.State != types.StateSuspended || v8192.NeverStarted {
		t.Fatalf("View(attendee-8192) = %+v (ok=%v), want checkpointed SUSPENDED", v8192, ok)
	}

	leased := map[string]bool{"attendee-12288": true}
	actors, compact, running, suspended := b.TelemetrySnapshotWithLeases(total, func(id string) bool { return leased[id] }, []string{"attendee-12288"})
	if len(compact) != total {
		t.Fatalf("len(compact) = %d, want %d", len(compact), total)
	}
	if running != 1 || suspended != total-1 {
		t.Errorf("running=%d suspended=%d, want 1 and %d", running, suspended, total-1)
	}
	if got := compact[0] - '@'; got != 6 {
		t.Errorf("attendee-01 code = %d, want 6 (AwaitingApproval)", got)
	}
	if got := compact[4095] - '@'; got != 4 {
		t.Errorf("attendee-4096 code = %d, want 4 (Running)", got)
	}
	if got := compact[8191] - '@'; got != 1 {
		t.Errorf("attendee-8192 code = %d, want 1 (Checkpointed SUSPENDED)", got)
	}
	if got := compact[12287] - '@'; got != 16 {
		t.Errorf("attendee-12288 code = %d, want 16 (NeverStarted + Leased)", got)
	}
	if _, ok := actors["attendee-4096"]; !ok {
		t.Errorf("seated attendee-4096 missing from actors snapshot")
	}
	if _, ok := actors["attendee-12288"]; !ok {
		t.Errorf("leased attendee-12288 missing from actors snapshot")
	}

	// TelemetrySnapshotOptions with includeCompact=false omits the 16.78M string while preserving exact counters.
	_, compactOmitted, runOpt, suspOpt, activeCohortOpt, snapOpt, leasedOpt := b.TelemetrySnapshotOptions(
		total, func(id string) bool { return leased[id] }, []string{"attendee-12288"}, false,
	)
	if compactOmitted != "" {
		t.Errorf("compactOmitted len = %d, want 0", len(compactOmitted))
	}
	if runOpt != 1 || suspOpt != total-1 {
		t.Errorf("TelemetrySnapshotOptions running=%d suspended=%d, want 1 and %d", runOpt, suspOpt, total-1)
	}
	// Active cohort: indices 1, 2, 16777215 (AwaitingApproval = 3) + attendee-4096 (Running = 1) = 4;
	// Snapshotted: attendee-8192 (Checkpointed SUSPENDED = 1); Leased: attendee-12288 (= 1).
	if activeCohortOpt != 4 || snapOpt != 1 || leasedOpt != 1 {
		t.Errorf("TelemetrySnapshotOptions counters active=%d snap=%d leased=%d, want 4, 1, 1", activeCohortOpt, snapOpt, leasedOpt)
	}

	// Binary snapshot ("SWFL" 24-byte header + 16,777,216 raw bytes with bit 5 onPod and bit 4 leased).
	bin := b.FleetBinarySnapshot(total, func(id string) bool { return leased[id] }, []string{"attendee-12288"})
	if len(bin) != actorstate.FleetBinaryHeaderBytes+total {
		t.Fatalf("len(bin) = %d, want %d", len(bin), actorstate.FleetBinaryHeaderBytes+total)
	}
	if string(bin[0:4]) != "SWFL" {
		t.Fatalf("magic = %q, want SWFL", string(bin[0:4]))
	}
	if v := binary.LittleEndian.Uint32(bin[4:8]); v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
	if cnt := binary.LittleEndian.Uint32(bin[8:12]); cnt != total {
		t.Errorf("actorCount = %d, want %d", cnt, total)
	}
	if ac := binary.LittleEndian.Uint32(bin[12:16]); ac != 4 {
		t.Errorf("binary activeCohort = %d, want 4", ac)
	}
	if sc := binary.LittleEndian.Uint32(bin[16:20]); sc != 1 {
		t.Errorf("binary snapshotted = %d, want 1", sc)
	}
	if lc := binary.LittleEndian.Uint32(bin[20:24]); lc != 1 {
		t.Errorf("binary leased = %d, want 1", lc)
	}
	payload := bin[actorstate.FleetBinaryHeaderBytes:]
	if got := payload[0]; got != 6 {
		t.Errorf("payload[0] (attendee-01) = %d, want 6", got)
	}
	if got := payload[4095]; got != (4 | 32) {
		t.Errorf("payload[4095] (seated attendee-4096) = %d, want %d (Running | onPod)", got, 4|32)
	}
	if got := payload[12287]; got != 16 {
		t.Errorf("payload[12287] (leased attendee-12288) = %d, want 16", got)
	}

	// Fast ClearUnleasedActivityIndices preserves protected indices and 4096-stride slots while clearing unleased simulated activity.
	b.SetActivity("attendee-4096", types.ActivityThinking)
	b.SetSource("attendee-4096", types.SourceGemini)
	b.ClearUnleasedActivityIndices(map[int]bool{2: true}, func(id string) bool { return id == "attendee-02" }, 4096)
	binAfter := b.FleetBinarySnapshot(total, nil, nil)
	payloadAfter := binAfter[actorstate.FleetBinaryHeaderBytes:]
	if got := payloadAfter[0] & 15; got != 0 {
		t.Errorf("attendee-01 after ClearUnleasedActivityIndices = %d, want 0 (cleared)", got)
	}
	if got := payloadAfter[1] & 15; got != 6 {
		t.Errorf("protected attendee-02 after ClearUnleasedActivityIndices = %d, want 6 (preserved)", got)
	}
	if got := payloadAfter[4095] & 15; got != 4 {
		t.Errorf("stride-protected seated attendee-4096 = %d, want 4", got)
	}
}

func TestBoard_HyperModeLifecycleResumingThinkingExecutingRunningSuspendingSuspended(t *testing.T) {
	const total = 100000
	b := actorstate.NewBoardWithCapacity(func(types.ActorView) {}, total)

	codeOf := func(idx1 int) byte {
		bin := b.FleetBinarySnapshot(total, nil, nil)
		return bin[actorstate.FleetBinaryHeaderBytes+idx1-1]
	}

	// 0. NeverStarted -> code 0
	if got := codeOf(42); got != 0 {
		t.Fatalf("initial code = %d, want 0 (NeverStarted)", got)
	}

	// 1. BeginHyperResume -> code 2 (Resuming), surviving an intermediate empty poll before worker assignment.
	b.BeginHyperResume("attendee-42", types.ActivityThinking)
	if got := codeOf(42) & 15; got != 2 {
		t.Fatalf("after BeginHyperResume code = %d, want 2 (Resuming)", got)
	}
	b.SetActiveStatuses(nil)
	if got := codeOf(42) & 15; got != 2 {
		t.Fatalf("after pre-assignment SetActiveStatuses code = %d, want 2 (Resuming preserved)", got)
	}

	// 2. Worker pod assigned -> StateRunning + ActivityThinking -> code 5 | 32 (onPod)
	b.SetActiveStatuses([]actorstate.Status{
		{Name: "attendee-42", State: types.StateRunning, WorkerPod: "worker-7"},
	})
	if got := codeOf(42); got != (5 | 32) {
		t.Fatalf("after worker assignment code = %d, want %d (Thinking | onPod)", got, 5|32)
	}

	// 3. Auto-chained approve on already-seated actor -> ActivityExecuting -> code 7 | 32 (no Resuming detour)
	b.BeginHyperResume("attendee-42", types.ActivityExecuting)
	if got := codeOf(42); got != (7 | 32) {
		t.Fatalf("during approve execution code = %d, want %d (Executing | onPod)", got, 7|32)
	}

	// 4. Turn finishes, warm SUSPEND_AFTER window -> StateRunning + ActivityIdle -> code 4 | 32
	b.SetActivity("attendee-42", types.ActivityIdle)
	if got := codeOf(42); got != (4 | 32) {
		t.Fatalf("during warm running idle window code = %d, want %d (Running | onPod)", got, 4|32)
	}

	// 5. BeginHyperSuspend -> StateSuspending -> code 3 | 32, surviving an intermediate active-seat poll.
	b.BeginHyperSuspend("attendee-42")
	if got := codeOf(42); got != (3 | 32) {
		t.Fatalf("during BeginHyperSuspend code = %d, want %d (Suspending | onPod)", got, 3|32)
	}
	b.SetActiveStatuses([]actorstate.Status{
		{Name: "attendee-42", State: types.StateRunning, WorkerPod: "worker-7"},
	})
	if got := codeOf(42); got != (3 | 32) {
		t.Fatalf("during active-seat poll mid-suspend code = %d, want %d (Suspending | onPod preserved)", got, 3|32)
	}

	// 6. FinishHyperSuspend(true) -> Checkpointed SUSPENDED -> code 1 (unseated)
	if b.IsCheckpointedIndex(43) {
		t.Fatalf("IsCheckpointedIndex(43) = true on untouched NeverStarted slot, want false")
	}
	b.FinishHyperSuspend("attendee-42", true)
	if !b.IsCheckpointedIndex(42) {
		t.Fatalf("IsCheckpointedIndex(42) = false after FinishHyperSuspend, want true")
	}
	if got := codeOf(42); got != 1 {
		t.Fatalf("after FinishHyperSuspend code = %d, want 1 (Checkpointed SUSPENDED)", got)
	}
	if seats := b.ActiveSeatCount(); seats != 0 {
		t.Fatalf("ActiveSeatCount after FinishHyperSuspend = %d, want 0", seats)
	}

	// 7. MarkCheckpointedBatch hydrates pre-existing checkpointed actors on startup,
	// and unseated BeginHyperResume(ActivityExecuting) immediately shows code 7 (Executing).
	b.MarkCheckpointedBatch([]int{10, 20, 30})
	if !b.IsCheckpointedIndex(20) || codeOf(20) != 1 {
		t.Fatalf("after MarkCheckpointedBatch codeOf(20) = %d, want 1 (Checkpointed SUSPENDED)", codeOf(20))
	}
	b.SetActivity("attendee-20", types.ActivityAwaitingApproval)
	b.FinishHyperSuspend("attendee-20", true)
	if got := codeOf(20); got != 6 {
		t.Fatalf("while suspended awaiting approval codeOf(20) = %d, want 6", got)
	}
	b.BeginHyperResume("attendee-20", types.ActivityExecuting)
	if got := codeOf(20) & 15; got != 7 {
		t.Fatalf("after unseated BeginHyperResume(ActivityExecuting) code = %d, want 7 (Executing)", got)
	}
	b.BeginHyperSuspend("attendee-20")
	if got := codeOf(20) & 15; got != 3 {
		t.Fatalf("during post-execution BeginHyperSuspend code = %d, want 3 (Suspending)", got)
	}
	b.FinishHyperSuspend("attendee-20", true)
	if got := codeOf(20); got != 1 {
		t.Fatalf("after post-execution FinishHyperSuspend code = %d, want 1 (Checkpointed SUSPENDED)", got)
	}
}

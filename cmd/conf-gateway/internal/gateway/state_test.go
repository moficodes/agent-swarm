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

package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/agent-swarm/pkg/types"
	"github.com/google/go-cmp/cmp"
)

// fakeControl serves a fixed listing and records suspends.
type fakeControl struct {
	mu       sync.Mutex
	statuses []actorstate.Status
	listErr  error

	// suspendErr is returned by every Suspend.
	suspendErr error
	// holdSuspend makes Suspend wait for its context to end before
	// recording the suspend.
	holdSuspend bool
	suspended   chan string

	// revertErr is returned by every Revert.
	revertErr error
	reverted  chan string
}

func newFakeControl(statuses ...actorstate.Status) *fakeControl {
	return &fakeControl{statuses: statuses, suspended: make(chan string, 64), reverted: make(chan string, 64)}
}

func (f *fakeControl) set(statuses ...actorstate.Status) {
	f.mu.Lock()
	f.statuses = statuses
	f.mu.Unlock()
}

func (f *fakeControl) List(context.Context) ([]actorstate.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]actorstate.Status(nil), f.statuses...), f.listErr
}

func (f *fakeControl) Suspend(ctx context.Context, name string) error {
	if f.holdSuspend {
		<-ctx.Done()
	}
	f.suspended <- name
	return f.suspendErr
}

func (f *fakeControl) Revert(_ context.Context, name string) error {
	f.reverted <- name
	return f.revertErr
}

// waitSuspended returns the next suspended actor, failing after a generous
// deadline so a missing suspend fails the test instead of hanging it.
func waitSuspended(t *testing.T, f *fakeControl) string {
	t.Helper()
	select {
	case name := <-f.suspended:
		return name
	case <-time.After(10 * time.Second):
		t.Fatal("no actor was suspended")
		return ""
	}
}

// waitView reads SSE events until one matches.
func waitView(t *testing.T, sc *bufio.Scanner, match func(types.ActorView) bool) types.ActorView {
	t.Helper()
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "actor_view":
			var v types.ActorView
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
				t.Fatal(err)
			}
			if match(v) {
				return v
			}
		}
	}
	t.Fatalf("stream ended: %v", sc.Err())
	return types.ActorView{}
}

func openEvents(t *testing.T, gw *gateway.Gateway, attendee string) *bufio.Scanner {
	t.Helper()
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events?attendee_id="+attendee+"&token="+gw.MintToken(attendee), nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/events status = %d", resp.StatusCode)
	}
	return bufio.NewScanner(resp.Body)
}

func okActor(t *testing.T) *httptest.Server {
	t.Helper()
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Sender: types.SenderAssistant, Content: "ok"})
	}))
	t.Cleanup(actor.Close)
	return actor
}

func TestState_SSEStreamsSubstrateStateChanges(t *testing.T) {
	ctrl := newFakeControl(actorstate.Status{Name: "attendee-01", State: types.StateSuspended})
	gw := newTestGateway(t, "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-01")

	waitView(t, sc, func(v types.ActorView) bool { return v.NeverStarted })
	ctrl.set(actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "pool-abc"})
	v := waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateRunning })
	if v.WorkerPod != "pool-abc" {
		t.Errorf("WorkerPod = %q", v.WorkerPod)
	}
}

func TestState_ListFailureShowsUnavailable(t *testing.T) {
	ctrl := newFakeControl(actorstate.Status{Name: "attendee-02", State: types.StateRunning})
	gw := newTestGateway(t, "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-02")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateRunning })

	ctrl.mu.Lock()
	ctrl.listErr = context.DeadlineExceeded
	ctrl.mu.Unlock()
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateUnavailable })
}

func TestState_LocalModeIsUnavailable(t *testing.T) {
	gw := newTestGateway(t, "")
	sc := openEvents(t, gw, "attendee-03")
	v := waitView(t, sc, func(types.ActorView) bool { return true })
	if v.State != types.StateUnavailable || !strings.Contains(v.StateDetail, "local mode") {
		t.Errorf("view = %+v", v)
	}
}

func TestState_ActivityFollowsChatAndApproval(t *testing.T) {
	actorReached := make(chan struct{})
	releaseActor := make(chan struct{})
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actorReached <- struct{}{}
		<-releaseActor
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			Sender: types.SenderAssistant, Content: "ok", Source: types.SourceGemini,
			Approval: &types.ApprovalRequest{ApprovalID: "app-1", Code: "echo", Language: "sh"},
		})
	}))
	t.Cleanup(actor.Close)
	gw := newTestGateway(t, actor.URL)
	sc := openEvents(t, gw, "attendee-04")

	done := make(chan struct{})
	go func() {
		defer close(done)
		postJSON(gw, "attendee-04", "/api/chat", types.ChatRequest{Message: "hi"})
	}()
	<-actorReached
	waitView(t, sc, func(v types.ActorView) bool { return v.Activity == types.ActivityThinking })
	close(releaseActor)
	v := waitView(t, sc, func(v types.ActorView) bool { return v.Activity == types.ActivityAwaitingApproval })
	if v.Source != types.SourceGemini {
		t.Errorf("Source = %q", v.Source)
	}
	<-done
}

func TestState_AutoSuspendAfterIdle(t *testing.T) {
	actor := okActor(t)
	ctrl := newFakeControl()
	gw := newTestGateway(t, actor.URL, gateway.WithActorControl(ctrl), gateway.WithSuspendAfter(time.Millisecond))

	postJSON(gw, "attendee-05", "/api/chat", types.ChatRequest{Message: "hi"})
	if got := waitSuspended(t, ctrl); got != "attendee-05" {
		t.Errorf("suspended %q", got)
	}
}

func TestState_SuspendsAfterTurnByDefault(t *testing.T) {
	actor := okActor(t)
	ctrl := newFakeControl()
	gw := newTestGateway(t, actor.URL, gateway.WithActorControl(ctrl))

	if rec := postJSON(gw, "attendee-06", "/api/chat", types.ChatRequest{Message: "hi"}); rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d", rec.Code)
	}
	if got := waitSuspended(t, ctrl); got != "attendee-06" {
		t.Errorf("suspended %q, want attendee-06", got)
	}
}

func TestState_CloseCancelsPendingSuspend(t *testing.T) {
	actor := okActor(t)
	ctrl := newFakeControl()
	gw := newTestGateway(t, actor.URL, gateway.WithActorControl(ctrl), gateway.WithSuspendAfter(time.Hour))

	postJSON(gw, "attendee-08", "/api/chat", types.ChatRequest{Message: "hi"})
	postJSON(gw, "attendee-08", "/api/chat", types.ChatRequest{Message: "again"})
	gw.Close() // returns only once every pending timer is stopped
	select {
	case name := <-ctrl.suspended:
		t.Errorf("suspended %q before SuspendAfter elapsed", name)
	default:
	}
}

func TestState_ResetSuspendsOnlyRunningActors(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "p1"},
		actorstate.Status{Name: "attendee-02", State: types.StateSuspended},
		actorstate.Status{Name: "attendee-03", State: types.StateResuming},
	)
	gw := newTestGateway(t, "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-03")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateResuming })

	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, presenterRequest(gw, httptest.NewRequest(http.MethodPost, "/api/reset", nil)))
	var body struct {
		ActorsReset   int `json:"actors_reset"`
		SuspendErrors int `json:"suspend_errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding reset response %q: %v", rec.Body.String(), err)
	}
	if body.ActorsReset != 2 || body.SuspendErrors != 0 {
		t.Errorf("reset = %+v, want 2 actors reset and no errors", body)
	}
	// Reset waits for its suspends, so both are already recorded.
	got := map[string]bool{}
	for len(ctrl.suspended) > 0 {
		got[<-ctrl.suspended] = true
	}
	if len(got) != 2 || !got["attendee-01"] || !got["attendee-03"] {
		t.Errorf("suspended %v, want attendee-01 and attendee-03", got)
	}
}

type resetBody struct {
	ActorsReset    int `json:"actors_reset"`
	SuspendErrors  int `json:"suspend_errors"`
	ActorsReverted int `json:"actors_reverted"`
	RevertErrors   int `json:"revert_errors"`
}

func postReset(t *testing.T, gw *gateway.Gateway) resetBody {
	t.Helper()
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, presenterRequest(gw, httptest.NewRequest(http.MethodPost, "/api/reset", nil)))
	var body resetBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding reset response %q: %v", rec.Body.String(), err)
	}
	return body
}

func drain(ch chan string) map[string]bool {
	got := map[string]bool{}
	for len(ch) > 0 {
		got[<-ch] = true
	}
	return got
}

func TestState_ResetRevertsCrashedActors(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "p1"},
		actorstate.Status{Name: "attendee-02", State: types.StateSuspended},
		actorstate.Status{Name: "attendee-04", State: types.StateCrashed},
	)
	gw := newTestGateway(t, "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-04")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateCrashed })

	body := postReset(t, gw)
	if body.ActorsReverted != 1 || body.RevertErrors != 0 {
		t.Errorf("reset = %+v, want 1 actor reverted and no errors", body)
	}
	if got := drain(ctrl.reverted); len(got) != 1 || !got["attendee-04"] {
		t.Errorf("reverted %v, want only attendee-04", got)
	}
	if got := drain(ctrl.suspended); len(got) != 1 || !got["attendee-01"] {
		t.Errorf("suspended %v, want only attendee-01", got)
	}
}

func TestState_ResetReportsRevertFailures(t *testing.T) {
	ctrl := newFakeControl(actorstate.Status{Name: "attendee-04", State: types.StateCrashed})
	ctrl.revertErr = context.DeadlineExceeded
	gw := newTestGateway(t, "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-04")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateCrashed })

	body := postReset(t, gw)
	if body.ActorsReverted != 0 || body.RevertErrors != 1 {
		t.Errorf("reset = %+v, want 0 reverted and 1 revert error", body)
	}
}

func TestState_ResetRevertsBeforeClearingHistory(t *testing.T) {
	ctrl := newFakeControl()
	// revertedFirst records, for each history clear, whether the actor had
	// already been reverted when the clear reached it.
	revertedFirst := make(chan bool, 4)
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == types.ClearHistoryPath {
			revertedFirst <- len(ctrl.reverted) > 0
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Sender: types.SenderAssistant, Content: "ok"})
	}))
	t.Cleanup(actor.Close)
	gw := newTestGateway(t, actor.URL, gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))

	// The attendee chats, then their actor crashes.
	if rec := postJSON(gw, "attendee-04", "/api/chat", types.ChatRequest{Message: "hi"}); rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d", rec.Code)
	}
	waitSuspended(t, ctrl) // post-turn suspend
	ctrl.set(actorstate.Status{Name: "attendee-04", State: types.StateCrashed})
	sc := openEvents(t, gw, "attendee-04")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateCrashed })

	if body := postReset(t, gw); body.ActorsReverted != 1 || body.ActorsReset != 1 {
		t.Errorf("reset = %+v, want 1 reverted and 1 reset", body)
	}
	select {
	case ok := <-revertedFirst:
		if !ok {
			t.Error("history clear reached the actor before it was reverted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("history was never cleared")
	}
}

func TestState_ResetSendsResetEvent(t *testing.T) {
	gw := newTestGateway(t, "")
	sc := openEvents(t, gw, "attendee-09")
	waitView(t, sc, func(types.ActorView) bool { return true })

	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, presenterRequest(gw, httptest.NewRequest(http.MethodPost, "/api/reset", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d", rec.Code)
	}
	for sc.Scan() {
		if sc.Text() == "event: reset" {
			return
		}
	}
	t.Fatalf("stream ended without a reset event: %v", sc.Err())
}

func TestState_TelemetryWorkersComeFromRealPods(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "pool-b"},
		actorstate.Status{Name: "attendee-02", State: types.StateRunning, WorkerPod: "pool-a"},
		actorstate.Status{Name: "attendee-03", State: types.StateSuspended},
		actorstate.Status{Name: "attendee-04", State: types.StateSuspending, WorkerPod: "pool-a"},
	)
	gw := newTestGateway(t, "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond), gateway.WithSeatsPerWorker(4))
	sc := openEvents(t, gw, "attendee-01")
	waitView(t, sc, func(v types.ActorView) bool { return v.WorkerPod == "pool-b" })

	tel := getTelemetry(t, gw)
	want := []types.WorkerStatus{
		{PodName: "pool-a", Actors: []types.WorkerActor{
			{ActorID: "attendee-02", State: types.StateRunning},
			{ActorID: "attendee-04", State: types.StateSuspending},
		}},
		{PodName: "pool-b", Actors: []types.WorkerActor{
			{ActorID: "attendee-01", State: types.StateRunning},
		}},
	}
	if diff := cmp.Diff(want, tel.Workers); diff != "" {
		t.Errorf("Workers mismatch (-want +got):\n%s", diff)
	}
	if tel.SeatsPerWorker != 4 {
		t.Errorf("SeatsPerWorker = %d, want 4", tel.SeatsPerWorker)
	}
	if tel.Actors["attendee-03"].State != types.StateSuspended {
		t.Errorf("Actors = %+v", tel.Actors)
	}
}

func TestState_TelemetryIncludesIdleWorkerPods(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "pool-b"},
		actorstate.Status{Name: "attendee-02", State: types.StateSuspended},
	)
	src := &fakeWorkerScale{}
	pods := types.WorkerPods{
		Current:   2,
		Desired:   2,
		Max:       5,
		Available: true,
		PodNames:  []string{"pool-a", "pool-b"},
	}
	src.pods.Store(&pods)
	gw := newTestGateway(t, "",
		gateway.WithActorControl(ctrl),
		gateway.WithPollInterval(time.Millisecond),
		gateway.WithWorkerScale(src),
		gateway.WithWorkerScaleInterval(time.Millisecond),
		gateway.WithSeatsPerWorker(4),
	)
	sc := openEvents(t, gw, "attendee-01")
	waitView(t, sc, func(v types.ActorView) bool { return v.WorkerPod == "pool-b" })
	tel := waitWorkerPods(t, gw, pods)

	want := []types.WorkerStatus{
		{PodName: "pool-a"},
		{PodName: "pool-b", Actors: []types.WorkerActor{
			{ActorID: "attendee-01", State: types.StateRunning},
		}},
	}
	if diff := cmp.Diff(want, tel.Workers); diff != "" {
		t.Errorf("Workers mismatch (-want +got):\n%s", diff)
	}
}

func TestState_NonChatReplyLeavesActivityIdle(t *testing.T) {
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain text"))
	}))
	t.Cleanup(actor.Close)
	gw := newTestGateway(t, actor.URL)

	if rec := postJSON(gw, "attendee-09", "/api/chat", types.ChatRequest{Message: "hi"}); rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d", rec.Code)
	}
	if v, _ := gw.ActorView("attendee-09"); v.Activity != types.ActivityIdle {
		t.Errorf("Activity = %q, want idle", v.Activity)
	}
}

func TestState_IgnoresNonAttendeeActors(t *testing.T) {
	const goldenActorUID = "b2efa702-029c-4b4b-b219-d383126e0592"
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "pool-a"},
		actorstate.Status{Name: goldenActorUID, State: types.StateRunning, WorkerPod: "pool-a"},
		actorstate.Status{Name: "attendee-04", State: types.StateSuspended},
	)
	gw := newTestGateway(t, "",
		gateway.WithActorControl(ctrl),
		gateway.WithPollInterval(time.Millisecond),
		gateway.WithMaxActors(2),
	)
	sc := openEvents(t, gw, "attendee-01")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateRunning })

	tel := getTelemetry(t, gw)
	if _, ok := tel.Actors[goldenActorUID]; ok {
		t.Fatalf("telemetry exposed golden-snapshot builder actor %q: %+v", goldenActorUID, tel.Actors)
	}
	if _, ok := tel.Actors["attendee-04"]; ok {
		t.Fatalf("telemetry exposed out-of-range actor attendee-04 with maxActors=2: %+v", tel.Actors)
	}
	wantWorkers := []types.WorkerStatus{
		{PodName: "pool-a", Actors: []types.WorkerActor{
			{ActorID: "attendee-01", State: types.StateRunning},
		}},
	}
	if diff := cmp.Diff(wantWorkers, tel.Workers); diff != "" {
		t.Errorf("Workers mismatch while golden actor running (-want +got):\n%s", diff)
	}

	// Reset must not suspend the non-attendee golden-snapshot builder actor.
	if body := postReset(t, gw); body.ActorsReset != 1 {
		t.Errorf("reset = %+v, want only attendee-01 reset", body)
	}
	if got := drain(ctrl.suspended); len(got) != 1 || !got["attendee-01"] {
		t.Errorf("suspended %v, want only attendee-01", got)
	}

	// Once Substrate deletes the golden-snapshot actor, it must not linger in
	// telemetry as UNKNOWN ("not found in ateapi").
	ctrl.set(actorstate.Status{Name: "attendee-01", State: types.StateSuspended})
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateSuspended })

	tel = getTelemetry(t, gw)
	if len(tel.Actors) != 1 || tel.Actors["attendee-01"].State != types.StateSuspended {
		t.Errorf("Actors after golden actor deletion = %+v, want only attendee-01 SUSPENDED", tel.Actors)
	}
}

func TestState_ResetAndPollerRecoverOrphanedSuspendingActors(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateSuspending, WorkerPod: "pool-a"},
		actorstate.Status{Name: "attendee-02", State: types.StateSuspended},
	)
	gw := newTestGateway(t, "",
		gateway.WithActorControl(ctrl),
		gateway.WithPollInterval(5*time.Millisecond),
		gateway.WithStuckSuspendingAfter(time.Hour),
	)
	sc := openEvents(t, gw, "attendee-01")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateSuspending })

	// 1. Reset All re-drives idempotent SuspendActor on orphaned SUSPENDING actors.
	body := postReset(t, gw)
	if body.ActorsReset != 1 || body.SuspendErrors != 0 {
		t.Fatalf("reset = %+v, want 1 actor reset and 0 errors", body)
	}
	if got := drain(ctrl.suspended); len(got) != 1 || !got["attendee-01"] {
		t.Fatalf("reset suspended %v, want attendee-01", got)
	}

	// 2. Background poller automatically re-drives orphaned SUSPENDING actors once
	// StuckSuspendingAfter elapses with no turn or local suspend in flight.
	ctrlAuto := newFakeControl(
		actorstate.Status{Name: "attendee-13", State: types.StateSuspending, WorkerPod: "pool-a"},
	)
	gwAuto := newTestGateway(t, "",
		gateway.WithActorControl(ctrlAuto),
		gateway.WithPollInterval(5*time.Millisecond),
		gateway.WithStuckSuspendingAfter(25*time.Millisecond),
	)
	_ = gwAuto
	if got := waitSuspended(t, ctrlAuto); got != "attendee-13" {
		t.Fatalf("auto-recovered %q, want attendee-13", got)
	}
}

func TestState_PollerSuspendsOrphanedRunningActorsUnlessBusy(t *testing.T) {
	actorReached := make(chan struct{})
	releaseActor := make(chan struct{})
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(actorReached)
		<-releaseActor
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Sender: types.SenderAssistant, Content: "ok"})
	}))
	t.Cleanup(actor.Close)

	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-08", State: types.StateRunning, WorkerPod: "pool-a"},
		actorstate.Status{Name: "attendee-15", State: types.StateRunning, WorkerPod: "pool-a"},
	)
	gw := newTestGateway(t, actor.URL,
		gateway.WithActorControl(ctrl),
		gateway.WithPollInterval(5*time.Millisecond),
		gateway.WithStuckRunningAfter(25*time.Millisecond),
		gateway.WithSuspendAfter(0),
	)

	// Start a turn for attendee-08 before StuckRunningAfter elapses so only
	// the orphaned idle attendee-15 is auto-suspended by the poller.
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		postJSON(gw, "attendee-08", "/api/chat", types.ChatRequest{Message: "hi"})
	}()
	<-actorReached

	if got := waitSuspended(t, ctrl); got != "attendee-15" {
		t.Fatalf("poller suspended %q, want orphaned attendee-15 while attendee-08 is mid-turn", got)
	}
	// Mark attendee-15 suspended in fakeControl so the poller stops re-suspending
	// it while we verify attendee-08 is not suspended mid-turn.
	ctrl.set(
		actorstate.Status{Name: "attendee-08", State: types.StateRunning, WorkerPod: "pool-a"},
		actorstate.Status{Name: "attendee-15", State: types.StateSuspended},
	)
	_ = drain(ctrl.suspended)

	time.Sleep(40 * time.Millisecond)
	select {
	case unexpected := <-ctrl.suspended:
		t.Fatalf("poller suspended %q while attendee-08 turn was still in flight", unexpected)
	default:
	}

	close(releaseActor)
	<-turnDone
	if got := waitSuspended(t, ctrl); got != "attendee-08" {
		t.Fatalf("post-turn suspended %q, want attendee-08", got)
	}
}

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

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
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
}

func newFakeControl(statuses ...actorstate.Status) *fakeControl {
	return &fakeControl{statuses: statuses, suspended: make(chan string, 64)}
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
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
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
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-02")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateRunning })

	ctrl.mu.Lock()
	ctrl.listErr = context.DeadlineExceeded
	ctrl.mu.Unlock()
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateUnavailable })
}

func TestState_LocalModeIsUnavailable(t *testing.T) {
	gw := newTestGateway(t, "", "")
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
	gw := newTestGateway(t, actor.URL, "")
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
	gw := newTestGateway(t, actor.URL, "", gateway.WithActorControl(ctrl), gateway.WithSuspendAfter(time.Millisecond))

	postJSON(gw, "attendee-05", "/api/chat", types.ChatRequest{Message: "hi"})
	if got := waitSuspended(t, ctrl); got != "attendee-05" {
		t.Errorf("suspended %q", got)
	}
}

func TestState_SuspendsAfterTurnByDefault(t *testing.T) {
	actor := okActor(t)
	ctrl := newFakeControl()
	gw := newTestGateway(t, actor.URL, "", gateway.WithActorControl(ctrl))

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
	gw := newTestGateway(t, actor.URL, "", gateway.WithActorControl(ctrl), gateway.WithSuspendAfter(time.Hour))

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
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
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

func TestState_ResetSendsResetEvent(t *testing.T) {
	gw := newTestGateway(t, "", "")
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
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-01")
	waitView(t, sc, func(v types.ActorView) bool { return v.WorkerPod == "pool-b" })

	tel := getTelemetry(t, gw)
	want := []types.WorkerStatus{
		{PodName: "pool-a", ActiveActor: "attendee-02", State: types.StateRunning},
		{PodName: "pool-b", ActiveActor: "attendee-01", State: types.StateRunning},
	}
	if len(tel.Workers) != len(want) || tel.Workers[0] != want[0] || tel.Workers[1] != want[1] {
		t.Errorf("Workers = %+v, want %+v", tel.Workers, want)
	}
	if tel.Actors["attendee-03"].State != types.StateSuspended {
		t.Errorf("Actors = %+v", tel.Actors)
	}
}

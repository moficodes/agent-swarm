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

package simulation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

type fakeTarget struct {
	ctx   context.Context
	chats chan string
}

func (f *fakeTarget) Context() context.Context         { return f.ctx }
func (f *fakeTarget) MintToken(attendee string) string { return "tok-" + attendee }
func (f *fakeTarget) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-"+r.Header.Get("X-Attendee-ID") {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/chat" {
			var req types.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Mock {
				http.Error(w, "expected mock: true chat request", http.StatusBadRequest)
				return
			}
		}
		select {
		case f.chats <- r.Header.Get("X-Attendee-ID"):
		default:
		}
		_ = json.NewEncoder(w).Encode(types.ChatMessage{ID: "m", Sender: types.SenderAssistant, Content: "ok"})
	})
}

func TestRunner_StartDrivesTargetAndStop(t *testing.T) {
	f := &fakeTarget{ctx: context.Background(), chats: make(chan string, 16)}
	r := NewRunner(f, WithSimulationDelays(time.Millisecond, time.Millisecond))
	if err := r.Start(2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if running, count := r.Status(); !running || count != 2 {
		t.Fatalf("Status = %v, %d; want true, 2", running, count)
	}
	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case id := <-f.chats:
			seen[id] = true
		case <-deadline:
			r.Stop()
			t.Fatalf("timed out waiting for chats; seen=%v", seen)
		}
	}
	if !seen["attendee-01"] || !seen["attendee-02"] {
		t.Fatalf("chats came from %v, want attendee-01 and attendee-02", seen)
	}
	r.Stop()
	if running, count := r.Status(); running || count != 0 {
		t.Fatalf("Status after Stop = %v, %d; want false, 0", running, count)
	}
}

func TestRunner_StartRejectsBadCountAndClosedTarget(t *testing.T) {
	f := &fakeTarget{ctx: context.Background(), chats: make(chan string, 1)}
	if err := NewRunner(f).Start(0); err == nil {
		t.Error("Start(0) succeeded")
	}
	if err := NewRunner(f).Start(types.MaxSimulationAttendees + 1); err == nil {
		t.Errorf("Start(%d) succeeded", types.MaxSimulationAttendees+1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewRunner(&fakeTarget{ctx: ctx}).Start(1); err == nil {
		t.Error("Start succeeded on a closed target")
	}
}

type approvalFakeTarget struct {
	ctx       context.Context
	mu        sync.Mutex
	chats     []string
	approvals []string
}

func (f *approvalFakeTarget) Context() context.Context         { return f.ctx }
func (f *approvalFakeTarget) MintToken(attendee string) string { return "tok-" + attendee }
func (f *approvalFakeTarget) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attendee := r.Header.Get("X-Attendee-ID")
		switch r.URL.Path {
		case "/api/chat":
			f.mu.Lock()
			f.chats = append(f.chats, attendee)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(types.ChatMessage{
				ID:      "m-" + attendee,
				Sender:  types.SenderAssistant,
				Content: "proposing action",
				Approval: &types.ApprovalRequest{
					ApprovalID: "appr-" + attendee,
					Code:       "echo ok",
					Language:   "bash",
				},
			})
		case "/api/approve":
			f.mu.Lock()
			f.approvals = append(f.approvals, attendee)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(types.ChatMessage{
				ID:      "m-exec-" + attendee,
				Sender:  types.SenderAssistant,
				Content: "done",
			})
		}
	})
}

func TestRunner_DataGeneratorOrchestrationAndHandleTurn(t *testing.T) {
	var orchMu sync.Mutex
	var configs []dataGeneratorOrchestratorPayload
	orchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var p dataGeneratorOrchestratorPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		orchMu.Lock()
		configs = append(configs, p)
		orchMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer orchSrv.Close()

	target := &approvalFakeTarget{ctx: context.Background()}
	r := NewRunner(target,
		WithSimulationDelays(10*time.Millisecond, 10*time.Millisecond),
		WithDataGenerator(orchSrv.URL, "http://conf-gateway/api/simulation/turn", "test-key"),
	)

	if err := r.Start(4096); err != nil {
		t.Fatalf("Start(4096): %v", err)
	}
	orchMu.Lock()
	if len(configs) != 1 || configs[0].IsPaused || configs[0].GlobalTargetMPS != 160 {
		t.Fatalf("orchestrator configs after Start = %+v", configs)
	}
	orchMu.Unlock()

	// Drive a simulated turn via HandleTurn: simulated Mock LLM turns must
	// auto-chain /api/chat -> /api/approve in a single turn without leaving
	// the actor paused in AwaitingApproval.
	turnBody, _ := json.Marshal(TurnPayload{
		TurnID:        "t1",
		Prompt:        "Run benchmark sandbox test",
		ReviewDelayMs: 15000,
		IdleDelayMs:   50,
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/simulation/turn", bytes.NewReader(turnBody))
	rec1 := httptest.NewRecorder()
	r.HandleTurn(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first HandleTurn status = %d, want 200", rec1.Code)
	}

	target.mu.Lock()
	if len(target.chats) != 1 || len(target.approvals) != 1 || target.approvals[0] != target.chats[0] {
		t.Fatalf("after first turn chats=%v approvals=%v, want 1 chat auto-chained into 1 matching approval", target.chats, target.approvals)
	}
	firstAttendee := target.chats[0]
	target.mu.Unlock()

	// Driving a second HandleTurn immediately dispatches another distinct actor's
	// full chat -> approve cycle.
	req2 := httptest.NewRequest(http.MethodPost, "/api/simulation/turn", bytes.NewReader(turnBody))
	rec2 := httptest.NewRecorder()
	r.HandleTurn(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second HandleTurn status = %d, want 200", rec2.Code)
	}

	target.mu.Lock()
	if len(target.chats) != 2 || len(target.approvals) != 2 || target.chats[1] == firstAttendee {
		t.Fatalf("after second turn chats=%v approvals=%v, want 2 distinct auto-chained turns", target.chats, target.approvals)
	}
	target.mu.Unlock()

	r.Stop()
	orchMu.Lock()
	if len(configs) != 2 || !configs[1].IsPaused || configs[1].GlobalTargetMPS != 0 {
		t.Fatalf("orchestrator configs after Stop = %+v", configs)
	}
	orchMu.Unlock()
}

type resetFakeTarget struct {
	approvalFakeTarget
	reverted      []string
	suspended     []string
	completed     bool
	doneSuspended int
	doneReverted  int
	doneErrors    int
}

func (f *resetFakeTarget) SuspendActorForReset(_ context.Context, attendeeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspended = append(f.suspended, attendeeID)
	return nil
}

func (f *resetFakeTarget) RevertActorForReset(_ context.Context, attendeeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reverted = append(f.reverted, attendeeID)
	return nil
}

func (f *resetFakeTarget) ActiveActorsForReset() ([]string, []string) {
	return nil, nil
}

func (f *resetFakeTarget) OnResetComplete(suspended, reverted, errs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = true
	f.doneSuspended = suspended
	f.doneReverted = reverted
	f.doneErrors = errs
}

func TestRunner_DataGeneratorResetOrchestrationAndHandleTurn(t *testing.T) {
	var orchMu sync.Mutex
	var configs []dataGeneratorOrchestratorPayload
	orchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var p dataGeneratorOrchestratorPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		orchMu.Lock()
		configs = append(configs, p)
		orchMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer orchSrv.Close()

	target := &resetFakeTarget{approvalFakeTarget: approvalFakeTarget{ctx: context.Background()}}
	r := NewRunner(target,
		WithResetSettleWindow(0),
		WithDataGenerator(orchSrv.URL, "http://conf-gateway/api/simulation/turn", "test-key"),
	)

	started, err := r.StartReset([]string{"attendee-03"}, []string{"attendee-01", "attendee-02"})
	if err != nil || !started {
		t.Fatalf("StartReset = (%v, %v), want (true, nil)", started, err)
	}

	orchMu.Lock()
	if len(configs) != 1 || configs[0].IsPaused || configs[0].GlobalTargetMPS != resetTargetMPS {
		t.Fatalf("orchestrator configs after StartReset = %+v", configs)
	}
	orchMu.Unlock()

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/simulation/turn", bytes.NewReader([]byte(`{"mode":"reset"}`)))
		rec := httptest.NewRecorder()
		r.HandleTurn(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("reset HandleTurn %d status = %d, want 200", i+1, rec.Code)
		}
	}

	target.mu.Lock()
	if len(target.reverted) != 1 || target.reverted[0] != "attendee-03" {
		t.Fatalf("reverted = %v, want [attendee-03]", target.reverted)
	}
	if len(target.suspended) != 2 || target.suspended[0] != "attendee-01" || target.suspended[1] != "attendee-02" {
		t.Fatalf("suspended = %v, want [attendee-01 attendee-02]", target.suspended)
	}
	if !target.completed || target.doneSuspended != 2 || target.doneReverted != 1 || target.doneErrors != 0 {
		t.Fatalf("reset completion = (completed=%v, susp=%d, rev=%d, err=%d)", target.completed, target.doneSuspended, target.doneReverted, target.doneErrors)
	}
	target.mu.Unlock()

	orchMu.Lock()
	if len(configs) != 2 || !configs[1].IsPaused || configs[1].GlobalTargetMPS != 0 {
		t.Fatalf("orchestrator configs after reset completion = %+v", configs)
	}
	orchMu.Unlock()
}

type oversubFakeTarget struct {
	approvalFakeTarget
	canAcquireSeat bool
}

func (f *oversubFakeTarget) CanAcquireSimulatedSeat() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.canAcquireSeat
}

func TestRunner_RealExecutionOnlyWithoutStaticPurpleSeeding(t *testing.T) {
	orchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer orchSrv.Close()

	target := &oversubFakeTarget{
		approvalFakeTarget: approvalFakeTarget{ctx: context.Background()},
		canAcquireSeat:     false,
	}
	r := NewRunner(target,
		WithSimulationDelays(10*time.Millisecond, 10*time.Millisecond),
		WithDataGenerator(orchSrv.URL, "http://conf-gateway/api/simulation/turn", "test-key"),
	)

	if err := r.Start(4096); err != nil {
		t.Fatalf("Start(4096): %v", err)
	}
	defer r.Stop()

	// When physical seats are full (canAcquireSeat == false), HandleTurn must
	// apply backpressure (204 No Content) and NEVER synthetically paint un-run
	// actors into static Purple (AwaitingApproval).
	req := httptest.NewRequest(http.MethodPost, "/api/simulation/turn", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	r.HandleTurn(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("HandleTurn while seat-blocked status = %d, want 204", rec.Code)
	}

	target.mu.Lock()
	chatCount := len(target.chats)
	target.canAcquireSeat = true
	target.mu.Unlock()

	if chatCount != 0 {
		t.Fatalf("chatCount while seat-blocked = %d, want 0", chatCount)
	}

	// Once seat headroom opens up, HandleTurn dispatches a full chat -> approve container turn.
	req2 := httptest.NewRequest(http.MethodPost, "/api/simulation/turn", bytes.NewReader([]byte(`{}`)))
	rec2 := httptest.NewRecorder()
	r.HandleTurn(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second HandleTurn status = %d, want 200", rec2.Code)
	}

	target.mu.Lock()
	if len(target.chats) != 1 || len(target.approvals) != 1 {
		t.Fatalf("after seat opened chats=%d approvals=%d, want 1 and 1", len(target.chats), len(target.approvals))
	}
	target.mu.Unlock()
}

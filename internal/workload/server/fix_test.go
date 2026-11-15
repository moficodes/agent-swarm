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

package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/internal/workload/geminitest"
	"github.com/agent-substrate/agent-swarm/internal/workload/server"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// failingScript passes the check and exits 1 with "boom" on stderr.
const (
	failingScript = "import sys\nsys.exit('boom')"
	fixedScript   = "print('fixed')"
)

// fixServer returns an actor in Gemini mode whose model answers with bodies
// in order, the first of which a chat turn has already left pending.
func fixServer(t *testing.T, bodies ...string) (*server.WorkloadServer, *geminitest.Server, string) {
	t.Helper()
	fake := geminitest.New(t, bodies...)
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(geminiConfig))
	msg := postChat(t, s.Handler(), fake.URL, "k.mac", "do the thing")
	if msg.Approval == nil {
		t.Fatalf("chat reply carried no approval: %+v", msg)
	}
	return s, fake, msg.Approval.ApprovalID
}

// approveDriven approves id, relaying the actor's model calls to upstream
// the way the gateway would.
func approveDriven(t *testing.T, h http.Handler, upstream, id string) types.ChatMessage {
	t.Helper()
	body, _ := json.Marshal(types.ApprovalResponse{ApprovalID: id, Decision: types.DecisionApprove})
	rec := geminitest.Drive(t, h, upstream, httptest.NewRequest(http.MethodPost, "/api/approve", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body %s", rec.Code, rec.Body)
	}
	var msg types.ChatMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestApprove_FailedRunGetsAFixToApprove(t *testing.T) {
	s, fake, id := fixServer(t,
		geminitest.ApprovalResponse("run it", failingScript, "python"),
		geminitest.ExplainedApprovalResponse("It exited on purpose; this version prints instead.", "run it", fixedScript, "python"),
	)

	msg := approveDriven(t, s.Handler(), fake.URL, id)

	if msg.Execution == nil || !msg.Execution.Corrected || msg.Execution.ExitCode != 1 || !strings.Contains(msg.Execution.Stderr, "boom") {
		t.Errorf("Execution = %+v, want the failed run marked corrected", msg.Execution)
	}
	if msg.Approval == nil || msg.Approval.Code != fixedScript {
		t.Fatalf("Approval = %+v, want the fix", msg.Approval)
	}
	if msg.Content != "It exited on purpose; this version prints instead." {
		t.Errorf("Content = %q, want the model's explanation", msg.Content)
	}
	if msg.Source != types.SourceGemini {
		t.Errorf("Source = %q, want gemini", msg.Source)
	}
	if p := s.PendingApproval(); p == nil || p.ApprovalID != msg.Approval.ApprovalID {
		t.Errorf("PendingApproval = %+v, want the fix", p)
	}
	if body := string(fake.Requests()[1].Body); !strings.Contains(body, "sys.exit") || !strings.Contains(body, "boom") {
		t.Errorf("fix request lacks the failed script or its stderr: %s", body)
	}
	// History gains the fix reply, not a separate failed-run message.
	var assistant []types.ChatMessage
	for _, m := range s.History() {
		if m.Sender == types.SenderAssistant {
			assistant = append(assistant, m)
		}
	}
	if len(assistant) != 2 || assistant[1].ID != msg.ID {
		t.Errorf("assistant history = %+v, want the proposal and the fix", assistant)
	}
}

func TestApprove_FixThatFailsGetsThePlainReplyWithoutAModelCall(t *testing.T) {
	s, fake, id := fixServer(t,
		geminitest.ApprovalResponse("run it", failingScript, "python"),
		geminitest.ApprovalResponse("run it", failingScript, "python"),
	)
	fix := approveDriven(t, s.Handler(), fake.URL, id)
	if fix.Approval == nil {
		t.Fatalf("first failure got no fix: %+v", fix)
	}
	calls := len(fake.Requests())

	msg := approveDriven(t, s.Handler(), fake.URL, fix.Approval.ApprovalID)

	if n := len(fake.Requests()) - calls; n != 0 {
		t.Errorf("made %d model calls for a failed fix, want none", n)
	}
	if msg.Approval != nil || msg.Execution == nil || msg.Execution.Corrected || msg.Execution.ExitCode != 1 {
		t.Errorf("reply = %+v, want the failed run with no fix", msg)
	}
	if msg.Content != "That approach didn't work in this sandbox. Try asking for something simpler." {
		t.Errorf("Content = %q, want the plain reply", msg.Content)
	}
	if s.PendingApproval() != nil {
		t.Error("a failed fix left an approval pending")
	}
}

func TestApprove_FixModelErrorGetsThePlainReply(t *testing.T) {
	s, _, id := fixServer(t, geminitest.ApprovalResponse("run it", failingScript, "python"))

	// An empty upstream answers every model call with 502.
	msg := approveDriven(t, s.Handler(), "", id)

	if msg.Approval != nil || msg.Execution == nil || msg.Execution.Corrected {
		t.Errorf("reply = %+v, want the failed run with no fix", msg)
	}
	if !strings.Contains(msg.Content, "didn't work in this sandbox") || msg.Source == types.SourceMockFallback {
		t.Errorf("reply = %+v, want the plain reply, not the mock engine", msg)
	}
}

func TestApprove_NewRequestGetsItsOwnFix(t *testing.T) {
	s, fake, id := fixServer(t,
		geminitest.ApprovalResponse("run it", failingScript, "python"), // first request
		geminitest.ApprovalResponse("run it", failingScript, "python"), // its fix, which fails
		geminitest.ApprovalResponse("run it", failingScript, "python"), // second request
		geminitest.ExplainedApprovalResponse("Fixed.", "run it", fixedScript, "python"),
	)
	fix := approveDriven(t, s.Handler(), fake.URL, id)
	approveDriven(t, s.Handler(), fake.URL, fix.Approval.ApprovalID)

	next := postChat(t, s.Handler(), fake.URL, "k.mac", "try again")
	if next.Approval == nil {
		t.Fatalf("second request got no approval: %+v", next)
	}
	msg := approveDriven(t, s.Handler(), fake.URL, next.Approval.ApprovalID)

	if msg.Execution == nil || !msg.Execution.Corrected || msg.Approval == nil || msg.Approval.Code != fixedScript {
		t.Errorf("reply = %+v, want a fix for the new request", msg)
	}
}

func TestApprove_FailedRunInMockModeGetsThePlainReply(t *testing.T) {
	// Mock mode's proposal is a fixed script that runs cleanly on its own, so
	// make it fail without touching its code: hide python from PATH.
	t.Setenv("PATH", t.TempDir())
	fake := geminitest.New(t)
	s := server.NewWorkloadServer(
		server.WithWorkspaceDir(t.TempDir()),
		server.WithGeminiConfig(geminiConfig),
		server.WithForceMock(true),
	)
	proposal := postChat(t, s.Handler(), fake.URL, "k.mac", "do the thing")
	if proposal.Approval == nil {
		t.Fatalf("chat reply carried no approval: %+v", proposal)
	}

	msg := approveDriven(t, s.Handler(), fake.URL, proposal.Approval.ApprovalID)

	if n := len(fake.Requests()); n != 0 {
		t.Errorf("mock mode made %d upstream model requests, want 0", n)
	}
	if msg.Content != "That approach didn't work in this sandbox. Try asking for something simpler." {
		t.Errorf("Content = %q, want the plain reply", msg.Content)
	}
	if msg.Execution == nil || msg.Execution.ExitCode == 0 || msg.Execution.Corrected {
		t.Errorf("Execution = %+v, want a failed, uncorrected run", msg.Execution)
	}
	if msg.Approval != nil {
		t.Errorf("Approval = %+v, want no fix in mock mode", msg.Approval)
	}
	if s.PendingApproval() != nil {
		t.Error("a mock-mode failure left an approval pending")
	}
}

func TestApprove_SuccessfulRunIsUnchanged(t *testing.T) {
	s, fake, id := fixServer(t, geminitest.ApprovalResponse("run it", fixedScript, "python"))
	calls := len(fake.Requests())

	msg := approveDriven(t, s.Handler(), fake.URL, id)

	if len(fake.Requests()) != calls {
		t.Error("a successful run made a model call")
	}
	if msg.Execution == nil || msg.Execution.Corrected || msg.Execution.Stdout != "fixed\n" || !strings.HasPrefix(msg.Content, "Execution completed in ") {
		t.Errorf("reply = %+v, want the run's output", msg)
	}
}

func TestApprove_NoPostExecutionSleep(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("run it", fixedScript, "python"))
	s := server.NewWorkloadServer(
		server.WithWorkspaceDir(t.TempDir()),
		server.WithGeminiConfig(geminiConfig),
		server.WithMockDelays(0, 0, 3*time.Second, 3*time.Second),
	)
	proposal := postChat(t, s.Handler(), fake.URL, "k.mac", "do the thing")
	if proposal.Approval == nil {
		t.Fatalf("chat reply carried no approval: %+v", proposal)
	}

	// Approvals must return immediately after sandbox execution without artificial sleep.
	t0 := time.Now()
	msg := approveDriven(t, s.Handler(), fake.URL, proposal.Approval.ApprovalID)
	elapsed := time.Since(t0)
	if msg.Execution == nil || msg.Execution.ExitCode != 0 {
		t.Fatalf("Execution = %+v, want exit 0", msg.Execution)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("approval wall time = %v, want < 2s (no post-execution sleep)", elapsed)
	}
}

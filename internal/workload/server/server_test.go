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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/internal/workload/gemini"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/geminitest"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/server"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func proxyConfig(url string) gemini.Config {
	return gemini.Config{ProxyURL: url, Project: "proj", Location: "loc", Model: "gemini-2.5-flash"}
}

func postChat(t *testing.T, h http.Handler, key, msg string) types.ChatMessage {
	t.Helper()
	body, _ := json.Marshal(types.ChatRequest{Message: msg})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	if key != "" {
		req.Header.Set(types.ModelKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body %s", rec.Code, rec.Body)
	}
	var out types.ChatMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHealthz(t *testing.T) {
	srv := server.NewWorkloadServer(server.WithForceMock(true))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("unexpected error calling /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read /healthz response body: %v", err)
	}
	if string(body) != "OK" {
		t.Fatalf("expected body 'OK', got %q", string(body))
	}
}

func TestChat_MockMode(t *testing.T) {
	srv := server.NewWorkloadServer(server.WithForceMock(true))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqBody, err := json.Marshal(types.ChatRequest{
		Message: "Analyze sales telemetry and detect anomalies",
	})
	if err != nil {
		t.Fatalf("failed to marshal chat request: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/chat", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("unexpected error posting to /api/chat: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var chatMsg types.ChatMessage
	if err := json.NewDecoder(resp.Body).Decode(&chatMsg); err != nil {
		t.Fatalf("failed to decode chat message: %v", err)
	}

	if chatMsg.Sender != types.SenderAssistant {
		t.Errorf("expected sender %q, got %q", types.SenderAssistant, chatMsg.Sender)
	}
	if chatMsg.Source != types.SourceMock {
		t.Errorf("expected source %q, got %q", types.SourceMock, chatMsg.Source)
	}
	if chatMsg.Approval == nil {
		t.Fatal("expected approval request in response, got nil")
	}
	if chatMsg.Approval.ApprovalID == "" {
		t.Error("expected non-empty ApprovalID")
	}
	if chatMsg.Approval.Language != "python" {
		t.Errorf("expected python language, got %q", chatMsg.Approval.Language)
	}
	if chatMsg.Approval.RiskLevel != types.RiskHigh {
		t.Errorf("expected HIGH risk level, got %q", chatMsg.Approval.RiskLevel)
	}
	if !strings.Contains(chatMsg.Approval.Code, "detect_anomalies") {
		t.Errorf("expected approval code to contain 'detect_anomalies', got: %s", chatMsg.Approval.Code)
	}
}

func TestChat_MockedGeminiHTTPTransport(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse(
		"Execute real-time telemetry regression analysis",
		"print('executing gemini generated python script')",
		"python",
	))
	srv := server.NewWorkloadServer(server.WithGeminiConfig(proxyConfig(fake.URL)), server.WithWorkspaceDir(t.TempDir()))

	chatMsg := postChat(t, srv.Handler(), "test-gemini-key", "Check cluster metrics")

	if chatMsg.Approval == nil {
		t.Fatal("expected approval request parsed from gemini functionCall, got nil")
	}
	if chatMsg.Approval.Summary != "Execute real-time telemetry regression analysis" {
		t.Errorf("unexpected summary: %s", chatMsg.Approval.Summary)
	}
	if chatMsg.Approval.Code != "print('executing gemini generated python script')" {
		t.Errorf("unexpected code: %s", chatMsg.Approval.Code)
	}
	if chatMsg.Approval.Language != "python" {
		t.Errorf("unexpected language: %s", chatMsg.Approval.Language)
	}
	if chatMsg.Approval.RiskLevel != types.RiskHigh {
		t.Errorf("unexpected risk level: %s", chatMsg.Approval.RiskLevel)
	}
	if chatMsg.Source != types.SourceGemini {
		t.Errorf("Source = %q, want gemini", chatMsg.Source)
	}
}

func TestInMemoryStatePreservation(t *testing.T) {
	srv := server.NewWorkloadServer(server.WithForceMock(true))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Turn 1: Send user message
	req1, _ := json.Marshal(types.ChatRequest{Message: "Turn 1: Analyze sensor stream"})
	resp1, err := http.Post(ts.URL+"/api/chat", "application/json", bytes.NewReader(req1))
	if err != nil {
		t.Fatalf("turn 1 request failed: %v", err)
	}
	defer resp1.Body.Close()

	var msg1 types.ChatMessage
	if err := json.NewDecoder(resp1.Body).Decode(&msg1); err != nil {
		t.Fatalf("failed to decode turn 1 response: %v", err)
	}
	if msg1.Approval == nil {
		t.Fatal("turn 1 missing approval")
	}

	// Reject Turn 1 approval
	rejectBody, _ := json.Marshal(types.ApprovalResponse{
		ApprovalID: msg1.Approval.ApprovalID,
		Decision:   types.DecisionReject,
	})
	rejectResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(rejectBody))
	if err != nil {
		t.Fatalf("turn 1 reject failed: %v", err)
	}
	defer rejectResp.Body.Close()

	// Turn 2: Send second user message
	req2, _ := json.Marshal(types.ChatRequest{Message: "Turn 2: Check network egress"})
	resp2, err := http.Post(ts.URL+"/api/chat", "application/json", bytes.NewReader(req2))
	if err != nil {
		t.Fatalf("turn 2 request failed: %v", err)
	}
	defer resp2.Body.Close()

	var msg2 types.ChatMessage
	if err := json.NewDecoder(resp2.Body).Decode(&msg2); err != nil {
		t.Fatalf("failed to decode turn 2 response: %v", err)
	}

	// Inspect in-memory state preservation
	history := srv.History()
	if len(history) < 4 {
		t.Fatalf("expected at least 4 messages in history, got %d", len(history))
	}

	// Verify order:
	// 0: User turn 1
	// 1: Assistant turn 1 (with approval)
	// 2: System / Assistant rejection notice
	// 3: User turn 2
	// 4: Assistant turn 2
	if history[0].Sender != types.SenderUser || history[0].Content != "Turn 1: Analyze sensor stream" {
		t.Errorf("history[0] unexpected: %+v", history[0])
	}
	if history[1].Sender != types.SenderAssistant || history[1].Approval == nil {
		t.Errorf("history[1] unexpected: %+v", history[1])
	}
	if history[2].Sender != types.SenderSystem && history[2].Sender != types.SenderAssistant {
		t.Errorf("history[2] unexpected sender: %s", history[2].Sender)
	}
	if history[3].Sender != types.SenderUser || history[3].Content != "Turn 2: Check network egress" {
		t.Errorf("history[3] unexpected: %+v", history[3])
	}
}

func TestApprove_Execute(t *testing.T) {
	workspace := t.TempDir()
	srv := server.NewWorkloadServer(
		server.WithForceMock(true),
		server.WithWorkspaceDir(workspace),
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Generate approval request
	chatReq, _ := json.Marshal(types.ChatRequest{Message: "Run outlier detection"})
	chatResp, err := http.Post(ts.URL+"/api/chat", "application/json", bytes.NewReader(chatReq))
	if err != nil {
		t.Fatalf("chat request failed: %v", err)
	}
	defer chatResp.Body.Close()

	var chatMsg types.ChatMessage
	if err := json.NewDecoder(chatResp.Body).Decode(&chatMsg); err != nil {
		t.Fatalf("failed to decode chat message: %v", err)
	}
	if chatMsg.Approval == nil {
		t.Fatal("expected approval request")
	}

	// 2. Submit approval
	appReq, _ := json.Marshal(types.ApprovalResponse{
		ApprovalID: chatMsg.Approval.ApprovalID,
		Decision:   types.DecisionApprove,
	})
	appResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(appReq))
	if err != nil {
		t.Fatalf("approve request failed: %v", err)
	}
	defer appResp.Body.Close()

	if appResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", appResp.StatusCode)
	}

	var resultMsg types.ChatMessage
	if err := json.NewDecoder(appResp.Body).Decode(&resultMsg); err != nil {
		t.Fatalf("failed to decode approve response: %v", err)
	}

	if resultMsg.Sender != types.SenderAssistant {
		t.Errorf("expected sender assistant, got %s", resultMsg.Sender)
	}
	if !strings.Contains(resultMsg.Content, "[SANDBOX]") && !strings.Contains(resultMsg.Content, "Execution completed") {
		t.Errorf("expected execution output in content, got: %s", resultMsg.Content)
	}

	// 3. Trying to approve the same ID again must fail with 404
	secondAppResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(appReq))
	if err != nil {
		t.Fatalf("second approve request failed: %v", err)
	}
	defer secondAppResp.Body.Close()

	if secondAppResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404 for consumed approval ID, got %d", secondAppResp.StatusCode)
	}
}

func TestApprove_Reject(t *testing.T) {
	srv := server.NewWorkloadServer(server.WithForceMock(true))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	chatReq, _ := json.Marshal(types.ChatRequest{Message: "Compute metrics"})
	chatResp, err := http.Post(ts.URL+"/api/chat", "application/json", bytes.NewReader(chatReq))
	if err != nil {
		t.Fatalf("chat request failed: %v", err)
	}
	defer chatResp.Body.Close()

	var chatMsg types.ChatMessage
	_ = json.NewDecoder(chatResp.Body).Decode(&chatMsg)
	if chatMsg.Approval == nil {
		t.Fatal("expected approval request")
	}

	// Reject approval
	rejectReq, _ := json.Marshal(types.ApprovalResponse{
		ApprovalID: chatMsg.Approval.ApprovalID,
		Decision:   types.DecisionReject,
	})
	rejectResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(rejectReq))
	if err != nil {
		t.Fatalf("reject request failed: %v", err)
	}
	defer rejectResp.Body.Close()

	if rejectResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rejectResp.StatusCode)
	}

	var rejectMsg types.ChatMessage
	if err := json.NewDecoder(rejectResp.Body).Decode(&rejectMsg); err != nil {
		t.Fatalf("failed to decode reject message: %v", err)
	}
	if !strings.Contains(strings.ToLower(rejectMsg.Content), "cancel") && !strings.Contains(strings.ToLower(rejectMsg.Content), "reject") {
		t.Errorf("expected cancellation message, got: %s", rejectMsg.Content)
	}

	// Ensure pending approval was cleared
	secondRejectResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(rejectReq))
	if err != nil {
		t.Fatalf("second reject request failed: %v", err)
	}
	defer secondRejectResp.Body.Close()

	if secondRejectResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for already-processed approval, got %d", secondRejectResp.StatusCode)
	}
}

func TestApprove_InvalidAndNotFound(t *testing.T) {
	srv := server.NewWorkloadServer(server.WithForceMock(true))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	t.Run("nonexistent approval ID returns 404", func(t *testing.T) {
		req, _ := json.Marshal(types.ApprovalResponse{
			ApprovalID: "nonexistent-id",
			Decision:   types.DecisionApprove,
		})
		resp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(req))
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("invalid decision returns 400", func(t *testing.T) {
		req, _ := json.Marshal(map[string]string{
			"approval_id": "app-123",
			"decision":    "MAYBE",
		})
		resp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(req))
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid decision, got %d", resp.StatusCode)
		}
	})

	t.Run("malformed json returns 400", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/api/approve", "application/json", strings.NewReader("bad json"))
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for malformed json, got %d", resp.StatusCode)
		}
	})

	t.Run("non-POST method returns 405", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/approve")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", resp.StatusCode)
		}
	})
}

func TestConcurrencySafety(t *testing.T) {
	srv := server.NewWorkloadServer(server.WithForceMock(true))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			// Concurrently query healthz
			hResp, err := http.Get(ts.URL + "/healthz")
			if err == nil {
				_ = hResp.Body.Close()
			}

			// Concurrently query chat
			chatReq, _ := json.Marshal(types.ChatRequest{
				Message: fmt.Sprintf("Worker %d: Analyze load spikes", workerID),
			})
			resp, err := http.Post(ts.URL+"/api/chat", "application/json", bytes.NewReader(chatReq))
			if err != nil {
				return
			}
			defer resp.Body.Close()

			var msg types.ChatMessage
			if err := json.NewDecoder(resp.Body).Decode(&msg); err == nil && msg.Approval != nil {
				// Concurrently approve or reject
				decision := types.DecisionApprove
				if workerID%2 == 0 {
					decision = types.DecisionReject
				}
				appReq, _ := json.Marshal(types.ApprovalResponse{
					ApprovalID: msg.Approval.ApprovalID,
					Decision:   decision,
				})
				appResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(appReq))
				if err == nil {
					_ = appResp.Body.Close()
				}
			}

			// Concurrently inspect history
			_ = srv.History()
		}()
	}

	wg.Wait()
}

func TestApprove_IncludeStderr(t *testing.T) {
	workspace := t.TempDir()

	fake := geminitest.New(t, geminitest.ApprovalResponse(
		"Run script producing stderr output",
		"import sys\nsys.stderr.write('Warning: cache miss on metric telemetry\\n')\nprint('Processed 100 events')",
		"python",
	))

	srv := server.NewWorkloadServer(
		server.WithGeminiConfig(proxyConfig(fake.URL)),
		server.WithWorkspaceDir(workspace),
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	chatMsg := postChat(t, srv.Handler(), "test-key", "Run analysis with warnings")
	if chatMsg.Approval == nil {
		t.Fatal("expected approval request")
	}

	appReq, _ := json.Marshal(types.ApprovalResponse{
		ApprovalID: chatMsg.Approval.ApprovalID,
		Decision:   types.DecisionApprove,
	})
	appResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(appReq))
	if err != nil {
		t.Fatalf("approve request failed: %v", err)
	}
	defer appResp.Body.Close()

	if appResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", appResp.StatusCode)
	}

	var resultMsg types.ChatMessage
	if err := json.NewDecoder(appResp.Body).Decode(&resultMsg); err != nil {
		t.Fatalf("failed to decode approve response: %v", err)
	}

	if !strings.Contains(resultMsg.Content, "Processed 100 events") {
		t.Errorf("expected stdout in result content, got: %s", resultMsg.Content)
	}
	if !strings.Contains(resultMsg.Content, "\nStderr:\n") {
		t.Errorf("expected '\\nStderr:\\n' in result content, got: %s", resultMsg.Content)
	}
	if !strings.Contains(resultMsg.Content, "Warning: cache miss on metric telemetry") {
		t.Errorf("expected stderr message in result content, got: %s", resultMsg.Content)
	}
}

func TestChat_MultiTurnToolExecutionLoop(t *testing.T) {
	t.Run("multi-turn loop yielding approval", func(t *testing.T) {
		fake := geminitest.New(t,
			geminitest.FunctionCallResponse("fetch_research_data", map[string]any{"topic": "cluster_telemetry"}),
			geminitest.ApprovalResponse("Apply telemetry mitigation script", "print('mitigation executed')", "python"),
		)
		srv := server.NewWorkloadServer(server.WithGeminiConfig(proxyConfig(fake.URL)), server.WithWorkspaceDir(t.TempDir()))

		msg := postChat(t, srv.Handler(), "k.mac", "Analyze metrics and mitigate anomalies")

		reqs := fake.Requests()
		if len(reqs) != 2 {
			t.Fatalf("expected 2 turns in tool execution loop, got %d", len(reqs))
		}
		if !strings.Contains(string(reqs[1].Body), `"functionResponse"`) {
			t.Errorf("expected functionResponse in turn 2 request, got: %s", reqs[1].Body)
		}

		if msg.Approval == nil {
			t.Fatal("expected approval request in response after multi-turn loop")
		}
		if msg.Approval.Summary != "Apply telemetry mitigation script" {
			t.Errorf("unexpected approval summary: %s", msg.Approval.Summary)
		}
		if msg.Approval.Code != "print('mitigation executed')" {
			t.Errorf("unexpected approval code: %s", msg.Approval.Code)
		}
		if msg.Source != types.SourceGemini {
			t.Errorf("Source = %q, want gemini", msg.Source)
		}
	})

	t.Run("multi-turn loop yielding text", func(t *testing.T) {
		fake := geminitest.New(t,
			geminitest.FunctionCallResponse("fetch_research_data", map[string]any{"topic": "system_telemetry"}),
			geminitest.TextResponse("All 7 telemetry samples are within nominal parameters."),
		)
		srv := server.NewWorkloadServer(server.WithGeminiConfig(proxyConfig(fake.URL)), server.WithWorkspaceDir(t.TempDir()))

		msg := postChat(t, srv.Handler(), "k.mac", "Check cluster telemetry status")

		if n := len(fake.Requests()); n != 2 {
			t.Fatalf("expected 2 turns in tool execution loop, got %d", n)
		}

		if msg.Content != "All 7 telemetry samples are within nominal parameters." {
			t.Errorf("unexpected content: %s", msg.Content)
		}
		if msg.Approval != nil {
			t.Errorf("expected nil approval for text-only response, got: %+v", msg.Approval)
		}
	})
}

func TestFineGrainedMutexLocking(t *testing.T) {
	t.Run("mutex not blocked during handleApprove execution", func(t *testing.T) {
		workspace := t.TempDir()

		// Mock Gemini to propose a shell sleep command
		fake := geminitest.New(t, geminitest.ApprovalResponse("Slow background job", "sleep 0.4", "sh"))

		srv := server.NewWorkloadServer(
			server.WithGeminiConfig(proxyConfig(fake.URL)),
			server.WithWorkspaceDir(workspace),
		)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		// Step 1: Create approval request
		chatReq, _ := json.Marshal(types.ChatRequest{Message: "Run slow job"})
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(chatReq))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(types.ModelKeyHeader, "k.mac")
		chatResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("chat request failed: %v", err)
		}
		defer chatResp.Body.Close()

		var chatMsg types.ChatMessage
		_ = json.NewDecoder(chatResp.Body).Decode(&chatMsg)
		if chatMsg.Approval == nil {
			t.Fatal("expected approval request")
		}

		// Step 2: Start approval in goroutine (which executes sleep 0.4s)
		appReq, _ := json.Marshal(types.ApprovalResponse{
			ApprovalID: chatMsg.Approval.ApprovalID,
			Decision:   types.DecisionApprove,
		})

		approveDone := make(chan struct{})
		go func() {
			defer close(approveDone)
			resp, appErr := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(appReq))
			if appErr == nil {
				_ = resp.Body.Close()
			}
		}()

		// Wait briefly so handleApprove starts executing
		time.Sleep(100 * time.Millisecond)

		// Step 3: Concurrently call srv.History() and measure duration.
		// If mutex is blocked by executor, this will take >= 300ms.
		start := time.Now()
		hist := srv.History()
		duration := time.Since(start)

		if duration > 200*time.Millisecond {
			t.Fatalf("srv.History() was blocked by executor execution for %v (expected < 200ms)", duration)
		}
		if len(hist) < 2 {
			t.Fatalf("expected at least 2 history messages, got %d", len(hist))
		}

		// Also verify pending approval was cleared immediately before execution
		pending := srv.PendingApproval()
		if pending != nil {
			t.Errorf("expected pending approval to be cleared during execution, got: %+v", pending)
		}

		<-approveDone
	})

	t.Run("mutex not blocked during handleChat Gemini generation", func(t *testing.T) {
		// geminitest has no delay hook, so simulate slow upstream latency with a
		// raw fake speaking the same generateContent response shape.
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(400 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, geminitest.TextResponse("Response after latency"))
		}))
		defer fake.Close()

		srv := server.NewWorkloadServer(server.WithGeminiConfig(proxyConfig(fake.URL)))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		chatDone := make(chan struct{})
		go func() {
			defer close(chatDone)
			reqBody, _ := json.Marshal(types.ChatRequest{Message: "Check latency"})
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(types.ModelKeyHeader, "k.mac")
			resp, chatErr := http.DefaultClient.Do(req)
			if chatErr == nil {
				_ = resp.Body.Close()
			}
		}()

		// Wait briefly so handleChat has entered Generate
		time.Sleep(100 * time.Millisecond)

		// History() must not be blocked while Generate is running
		start := time.Now()
		hist := srv.History()
		duration := time.Since(start)

		if duration > 200*time.Millisecond {
			t.Fatalf("srv.History() was blocked by Gemini generation for %v (expected < 200ms)", duration)
		}
		// The user message should already be present in history!
		if len(hist) != 1 || hist[0].Content != "Check latency" {
			t.Fatalf("expected user message in history while generation is running, got: %+v", hist)
		}

		<-chatDone

		// After completion, assistant message is appended
		histAfter := srv.History()
		if len(histAfter) != 2 {
			t.Fatalf("expected 2 messages after chat completed, got %d", len(histAfter))
		}
	})
}

func TestChat_UsesModelKeyFromGatewayHeader(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("from gemini"))
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(proxyConfig(fake.URL)))

	msg := postChat(t, s.Handler(), "attendee-01.mac", "hi")

	if msg.Content != "from gemini" || msg.Source != types.SourceGemini {
		t.Errorf("reply = %q source %q, want gemini reply", msg.Content, msg.Source)
	}
	if got := fake.Requests()[0].Authorization; got != "Bearer attendee-01.mac" {
		t.Errorf("proxy Authorization = %q", got)
	}
	if s.ModelKey() != "attendee-01.mac" {
		t.Errorf("ModelKey() = %q", s.ModelKey())
	}
}

func TestChat_ModelFailureFallsBackToLabeledMock(t *testing.T) {
	fake := geminitest.NewStatus(t, http.StatusInternalServerError)
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(proxyConfig(fake.URL)))

	msg := postChat(t, s.Handler(), "k.mac", "hi")

	if msg.Source != types.SourceMockFallback {
		t.Errorf("Source = %q, want mock-fallback", msg.Source)
	}
}

func TestChat_NoProxyConfiguredUsesLabeledMock(t *testing.T) {
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(gemini.Config{}))
	if msg := postChat(t, s.Handler(), "", "hi"); msg.Source != types.SourceMock {
		t.Errorf("Source = %q, want mock", msg.Source)
	}
}

func TestChat_ForceMockSkipsProxy(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("unused"))
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()),
		server.WithGeminiConfig(proxyConfig(fake.URL)), server.WithForceMock(true))

	if msg := postChat(t, s.Handler(), "k.mac", "hi"); msg.Source != types.SourceMock {
		t.Errorf("Source = %q, want mock", msg.Source)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Errorf("proxy got %d requests, want 0", n)
	}
}

func TestChat_HistoryIsSentOnFollowingTurn(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("first answer"), geminitest.TextResponse("second answer"))
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(proxyConfig(fake.URL)))

	postChat(t, s.Handler(), "k.mac", "remember the word pelican")
	postChat(t, s.Handler(), "k.mac", "what word?")

	first := string(fake.Requests()[0].Body)
	second := string(fake.Requests()[1].Body)
	if !strings.Contains(second, "pelican") || !strings.Contains(second, "first answer") {
		t.Errorf("second model request lacks history: %s", second)
	}
	// The user's own turn must appear exactly once: handleChat appends it to
	// history before calling Generate, so BuildContents must not also append
	// it as the trailing prompt.
	if n := strings.Count(first, "remember the word pelican"); n != 1 {
		t.Errorf("first request contains the prompt %d times, want 1: %s", n, first)
	}
	if n := strings.Count(second, "what word?"); n != 1 {
		t.Errorf("second request contains the prompt %d times, want 1: %s", n, second)
	}
}

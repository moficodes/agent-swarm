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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload/internal/server"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

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
	// Mock Gemini REST API endpoint
	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("x-goog-api-key")
		if apiKey != "test-gemini-key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if !strings.Contains(r.URL.Path, "/v1beta/models/") || !strings.Contains(r.URL.Path, ":generateContent") {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		// Return Gemini generateContent response with request_human_approval function call
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role": "model",
						"parts": []map[string]any{
							{
								"text": "I have created an anomaly detection script for you. Please approve execution:",
							},
							{
								"functionCall": map[string]any{
									"name": "request_human_approval",
									"args": map[string]any{
										"summary":    "Execute real-time telemetry regression analysis",
										"code":       "print('executing gemini generated python script')",
										"language":   "python",
										"risk_level": "HIGH",
									},
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer geminiServer.Close()

	client := server.NewGeminiClient(
		server.WithBaseURL(geminiServer.URL),
		server.WithModel("gemini-2.5-flash-lite"),
	)
	srv := server.NewWorkloadServer(
		server.WithGeminiClient(client),
		server.WithForceMock(false),
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqBody, _ := json.Marshal(types.ChatRequest{Message: "Check cluster metrics"})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gemini-API-Key", "test-gemini-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request to /api/chat failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var chatMsg types.ChatMessage
	if err := json.NewDecoder(resp.Body).Decode(&chatMsg); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

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

func TestGeminiClient_ConfigurationAndFallback(t *testing.T) {
	t.Run("default model is gemini-2.5-flash", func(t *testing.T) {
		t.Setenv("GEMINI_MODEL", "")
		c := server.NewGeminiClient()
		if c.Model() != "gemini-2.5-flash" {
			t.Errorf("expected default model 'gemini-2.5-flash', got %q", c.Model())
		}
	})

	t.Run("env var GEMINI_MODEL overrides default", func(t *testing.T) {
		t.Setenv("GEMINI_MODEL", "gemini-2.5-flash-lite")
		c := server.NewGeminiClient()
		if c.Model() != "gemini-2.5-flash-lite" {
			t.Errorf("expected model 'gemini-2.5-flash-lite', got %q", c.Model())
		}
	})

	t.Run("explicit WithModel option overrides env var", func(t *testing.T) {
		t.Setenv("GEMINI_MODEL", "gemini-2.5-flash-lite")
		c := server.NewGeminiClient(server.WithModel("gemini-custom-pro"))
		if c.Model() != "gemini-custom-pro" {
			t.Errorf("expected model 'gemini-custom-pro', got %q", c.Model())
		}
	})

	t.Run("tool declarations include required tools", func(t *testing.T) {
		c := server.NewGeminiClient()
		tools := c.ToolDeclarations()
		expectedTools := []string{
			"request_human_approval",
			"execute_sandbox_code",
			"fetch_research_data",
		}
		for _, wantTool := range expectedTools {
			found := false
			for _, tool := range tools {
				for _, fn := range tool.FunctionDeclarations {
					if fn.Name == wantTool {
						found = true
						break
					}
				}
			}
			if !found {
				t.Errorf("missing tool declaration: %q", wantTool)
			}
		}
	})

	t.Run("fallback to MockEngine when API key is empty", func(t *testing.T) {
		t.Setenv("PROJECT_ID", "")
		t.Setenv("GOOGLE_CLOUD_PROJECT", "")
		c := server.NewGeminiClient(server.WithProject(""))
		msg, err := c.GenerateContent(context.Background(), "", nil, "Analyze anomalies")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg == nil || msg.Approval == nil {
			t.Fatal("expected mock engine message with approval when API key is empty")
		}
	})

	t.Run("fallback to MockEngine when HTTP endpoint returns 500 error", func(t *testing.T) {
		errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal service failure", http.StatusInternalServerError)
		}))
		defer errServer.Close()

		c := server.NewGeminiClient(server.WithBaseURL(errServer.URL))
		msg, err := c.GenerateContent(context.Background(), "some-key", nil, "Analyze anomalies")
		if err != nil {
			t.Fatalf("unexpected error during fallback: %v", err)
		}
		if msg == nil || msg.Approval == nil {
			t.Fatal("expected fallback to mock engine when Gemini API fails with 500")
		}
	})
}

func TestApprove_IncludeStderr(t *testing.T) {
	workspace := t.TempDir()

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role": "model",
						"parts": []map[string]any{
							{
								"functionCall": map[string]any{
									"name": "request_human_approval",
									"args": map[string]any{
										"summary":    "Run script producing stderr output",
										"code":       "import sys\nsys.stderr.write('Warning: cache miss on metric telemetry\\n')\nprint('Processed 100 events')",
										"language":   "python",
										"risk_level": "HIGH",
									},
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer geminiServer.Close()

	client := server.NewGeminiClient(
		server.WithBaseURL(geminiServer.URL),
		server.WithModel("gemini-2.5-flash"),
	)
	srv := server.NewWorkloadServer(
		server.WithGeminiClient(client),
		server.WithWorkspaceDir(workspace),
		server.WithForceMock(false),
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	chatReq, _ := json.Marshal(types.ChatRequest{Message: "Run analysis with warnings"})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(chatReq))
	if err != nil {
		t.Fatalf("failed to create chat request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gemini-API-Key", "test-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat request failed: %v", err)
	}
	defer resp.Body.Close()

	var chatMsg types.ChatMessage
	if err := json.NewDecoder(resp.Body).Decode(&chatMsg); err != nil {
		t.Fatalf("failed to decode chat response: %v", err)
	}
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
		var turnCount int
		var mu sync.Mutex

		geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			turnCount++

			var reqBody server.GenerateContentRequest
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
				return
			}

			if turnCount == 1 {
				resp := map[string]any{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"role": "model",
								"parts": []map[string]any{
									{
										"functionCall": map[string]any{
											"name": "fetch_research_data",
											"args": map[string]any{
												"topic": "cluster_telemetry",
											},
										},
									},
								},
							},
							"finishReason": "STOP",
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			if turnCount == 2 {
				foundToolResponse := false
				for _, c := range reqBody.Contents {
					for _, p := range c.Parts {
						if p.FunctionResponse != nil && p.FunctionResponse.Name == "fetch_research_data" {
							foundToolResponse = true
							if p.FunctionResponse.Response["status"] != "success" {
								t.Errorf("expected status 'success' in function response, got: %v", p.FunctionResponse.Response["status"])
							}
							if p.FunctionResponse.Response["telemetry"] == nil {
								t.Error("expected telemetry in function response")
							}
						}
					}
				}
				if !foundToolResponse {
					t.Error("expected functionResponse part in turn 2 request")
				}

				resp := map[string]any{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"role": "model",
								"parts": []map[string]any{
									{
										"text": "Telemetry analyzed. Detected anomalous spikes requiring mitigation:",
									},
									{
										"functionCall": map[string]any{
											"name": "request_human_approval",
											"args": map[string]any{
												"summary":    "Apply telemetry mitigation script",
												"code":       "print('mitigation executed')",
												"language":   "python",
												"risk_level": "HIGH",
											},
										},
									},
								},
							},
							"finishReason": "STOP",
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			http.Error(w, "unexpected turn count", http.StatusInternalServerError)
		}))
		defer geminiServer.Close()

		client := server.NewGeminiClient(
			server.WithBaseURL(geminiServer.URL),
			server.WithModel("gemini-2.5-flash"),
		)
		srv := server.NewWorkloadServer(
			server.WithGeminiClient(client),
			server.WithForceMock(false),
		)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		chatReq, _ := json.Marshal(types.ChatRequest{Message: "Analyze metrics and mitigate anomalies"})
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(chatReq))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gemini-API-Key", "test-api-key")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		var msg types.ChatMessage
		if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		mu.Lock()
		finalTurnCount := turnCount
		mu.Unlock()

		if finalTurnCount != 2 {
			t.Errorf("expected 2 turns in tool execution loop, got %d", finalTurnCount)
		}

		if !strings.Contains(msg.Content, "Telemetry analyzed") {
			t.Errorf("expected telemetry analysis in content, got: %s", msg.Content)
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
	})

	t.Run("multi-turn loop yielding text", func(t *testing.T) {
		var turnCount int
		var mu sync.Mutex

		geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			turnCount++

			if turnCount == 1 {
				resp := map[string]any{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"role": "model",
								"parts": []map[string]any{
									{
										"functionCall": map[string]any{
											"name": "fetch_research_data",
											"args": map[string]any{
												"topic": "system_telemetry",
											},
										},
									},
								},
							},
							"finishReason": "STOP",
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			if turnCount == 2 {
				resp := map[string]any{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"role": "model",
								"parts": []map[string]any{
									{
										"text": "All 7 telemetry samples are within nominal parameters.",
									},
								},
							},
							"finishReason": "STOP",
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			http.Error(w, "unexpected turn count", http.StatusInternalServerError)
		}))
		defer geminiServer.Close()

		client := server.NewGeminiClient(
			server.WithBaseURL(geminiServer.URL),
			server.WithModel("gemini-2.5-flash"),
		)
		srv := server.NewWorkloadServer(
			server.WithGeminiClient(client),
			server.WithForceMock(false),
		)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		chatReq, _ := json.Marshal(types.ChatRequest{Message: "Check cluster telemetry status"})
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(chatReq))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gemini-API-Key", "test-api-key")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		var msg types.ChatMessage
		if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		mu.Lock()
		finalTurnCount := turnCount
		mu.Unlock()

		if finalTurnCount != 2 {
			t.Errorf("expected 2 turns in tool execution loop, got %d", finalTurnCount)
		}

		if msg.Content != "All 7 telemetry samples are within nominal parameters." {
			t.Errorf("unexpected content: %s", msg.Content)
		}
		if msg.Approval != nil {
			t.Errorf("expected nil approval for text-only response, got: %+v", msg.Approval)
		}
	})
}

func TestGeminiClient_PreventDuplicateUserPrompt(t *testing.T) {
	var capturedContents []*server.Content
	var mu sync.Mutex

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		var reqBody server.GenerateContentRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedContents = reqBody.Contents
		mu.Unlock()

		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role": "model",
						"parts": []map[string]any{
							{"text": "Acknowledged."},
						},
					},
					"finishReason": "STOP",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer geminiServer.Close()

	client := server.NewGeminiClient(
		server.WithBaseURL(geminiServer.URL),
		server.WithModel("gemini-2.5-flash"),
	)

	t.Run("does not duplicate prompt if already at end of history", func(t *testing.T) {
		history := []types.ChatMessage{
			{Sender: types.SenderUser, Content: "First prompt"},
			{Sender: types.SenderAssistant, Content: "Response 1"},
			{Sender: types.SenderUser, Content: "Second prompt"},
		}
		_, err := client.GenerateContent(context.Background(), "test-key", history, "Second prompt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		mu.Lock()
		contents := capturedContents
		mu.Unlock()

		if len(contents) != 3 {
			t.Fatalf("expected exactly 3 contents, got %d", len(contents))
		}
		if contents[2].Parts[0].Text != "Second prompt" {
			t.Errorf("expected last content to be 'Second prompt', got %q", contents[2].Parts[0].Text)
		}
	})

	t.Run("appends prompt if not at end of history", func(t *testing.T) {
		history := []types.ChatMessage{
			{Sender: types.SenderUser, Content: "First prompt"},
			{Sender: types.SenderAssistant, Content: "Response 1"},
		}
		_, err := client.GenerateContent(context.Background(), "test-key", history, "New question")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		mu.Lock()
		contents := capturedContents
		mu.Unlock()

		if len(contents) != 3 {
			t.Fatalf("expected exactly 3 contents, got %d", len(contents))
		}
		if contents[2].Parts[0].Text != "New question" {
			t.Errorf("expected last content to be 'New question', got %q", contents[2].Parts[0].Text)
		}
	})
}

func TestGeminiClient_ConsecutiveSameRoleHistoryMerging(t *testing.T) {
	var capturedContents []*server.Content
	var mu sync.Mutex

	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		var reqBody server.GenerateContentRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedContents = reqBody.Contents
		mu.Unlock()

		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role": "model",
						"parts": []map[string]any{
							{"text": "Merged turns acknowledged."},
						},
					},
					"finishReason": "STOP",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer geminiServer.Close()

	client := server.NewGeminiClient(
		server.WithBaseURL(geminiServer.URL),
		server.WithModel("gemini-2.5-flash"),
	)

	t.Run("consecutive user and system turns merged into single user content with multiple parts", func(t *testing.T) {
		history := []types.ChatMessage{
			{Sender: types.SenderUser, Content: "Run initial analysis"},
			{Sender: types.SenderAssistant, Content: "Proposed script for approval"},
			{Sender: types.SenderSystem, Content: "Execution rejected by attendee. Operation canceled."},
		}
		_, err := client.GenerateContent(context.Background(), "test-key", history, "Please do something else instead")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		mu.Lock()
		contents := capturedContents
		mu.Unlock()

		// Expected alternating roles: user, model, user (system rejection + new prompt merged)
		if len(contents) != 3 {
			t.Fatalf("expected 3 contents, got %d", len(contents))
		}
		if contents[0].Role != "user" || len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != "Run initial analysis" {
			t.Errorf("contents[0] mismatch: %+v", contents[0])
		}
		if contents[1].Role != "model" || len(contents[1].Parts) != 1 || contents[1].Parts[0].Text != "Proposed script for approval" {
			t.Errorf("contents[1] mismatch: %+v", contents[1])
		}
		if contents[2].Role != "user" {
			t.Errorf("expected role 'user', got %q", contents[2].Role)
		}
		if len(contents[2].Parts) != 2 {
			t.Fatalf("expected 2 parts merged in contents[2], got %d", len(contents[2].Parts))
		}
		if contents[2].Parts[0].Text != "Execution rejected by attendee. Operation canceled." {
			t.Errorf("part 0 mismatch: %q", contents[2].Parts[0].Text)
		}
		if contents[2].Parts[1].Text != "Please do something else instead" {
			t.Errorf("part 1 mismatch: %q", contents[2].Parts[1].Text)
		}
	})

	t.Run("consecutive assistant turns merged into single model content with multiple parts", func(t *testing.T) {
		history := []types.ChatMessage{
			{Sender: types.SenderUser, Content: "Execute task"},
			{Sender: types.SenderAssistant, Content: "Analysis plan"},
			{Sender: types.SenderAssistant, Content: "Execution completed in 42ms:\noutput"},
		}
		_, err := client.GenerateContent(context.Background(), "test-key", history, "What were the results?")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		mu.Lock()
		contents := capturedContents
		mu.Unlock()

		// Expected:
		// 0: user ("Execute task")
		// 1: model (merged: "Analysis plan" + "Execution completed in 42ms:\noutput")
		// 2: user ("What were the results?")
		if len(contents) != 3 {
			t.Fatalf("expected 3 contents, got %d", len(contents))
		}
		if contents[0].Role != "user" {
			t.Errorf("contents[0] expected role 'user', got %q", contents[0].Role)
		}
		if contents[1].Role != "model" {
			t.Errorf("contents[1] expected role 'model', got %q", contents[1].Role)
		}
		if len(contents[1].Parts) != 2 {
			t.Fatalf("expected 2 parts in model content, got %d", len(contents[1].Parts))
		}
		if contents[1].Parts[0].Text != "Analysis plan" {
			t.Errorf("part 0 mismatch: %q", contents[1].Parts[0].Text)
		}
		if contents[1].Parts[1].Text != "Execution completed in 42ms:\noutput" {
			t.Errorf("part 1 mismatch: %q", contents[1].Parts[1].Text)
		}
		if contents[2].Role != "user" {
			t.Errorf("contents[2] expected role 'user', got %q", contents[2].Role)
		}
	})

	t.Run("strict alternating roles check across any history", func(t *testing.T) {
		history := []types.ChatMessage{
			{Sender: types.SenderUser, Content: "msg 1"},
			{Sender: types.SenderUser, Content: "msg 2"},
			{Sender: types.SenderAssistant, Content: "resp 1"},
			{Sender: types.SenderAssistant, Content: "resp 2"},
			{Sender: types.SenderSystem, Content: "notice 1"},
			{Sender: types.SenderUser, Content: "msg 3"},
		}
		_, err := client.GenerateContent(context.Background(), "test-key", history, "msg 4")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		mu.Lock()
		contents := capturedContents
		mu.Unlock()

		for i := 1; i < len(contents); i++ {
			if contents[i].Role == contents[i-1].Role {
				t.Fatalf("consecutive same role %q found at index %d and %d", contents[i].Role, i-1, i)
			}
		}
	})
}

func TestGeminiClient_ContextCancellation(t *testing.T) {
	client := server.NewGeminiClient(
		server.WithBaseURL("https://example.com"),
		server.WithModel("gemini-2.5-flash"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	msg, err := client.GenerateContent(ctx, "test-key", nil, "test prompt")
	if err == nil {
		t.Fatalf("expected error from canceled context, got nil error with msg: %+v", msg)
	}
	if ctx.Err() == nil || err != context.Canceled {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
	if msg != nil {
		t.Fatalf("expected nil msg when context is canceled, got %+v", msg)
	}
}

func TestGeminiClient_ExecuteSandboxCodeApproval(t *testing.T) {
	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("x-goog-api-key")
		if apiKey != "test-key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role": "model",
						"parts": []map[string]any{
							{
								"functionCall": map[string]any{
									"name": "execute_sandbox_code",
									"args": map[string]any{
										"code":     "print('hello sandbox')",
										"language": "python",
										"summary":  "Test sandbox execution",
									},
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer geminiServer.Close()

	client := server.NewGeminiClient(
		server.WithBaseURL(geminiServer.URL),
		server.WithModel("gemini-2.5-flash"),
	)

	msg, err := client.GenerateContent(context.Background(), "test-key", nil, "Run python script")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil ChatMessage")
	}
	if msg.Approval == nil {
		t.Fatal("expected approval request when Gemini invokes execute_sandbox_code")
	}
	if msg.Approval.Code != "print('hello sandbox')" {
		t.Errorf("unexpected approval code: %s", msg.Approval.Code)
	}
	if msg.Approval.Language != "python" {
		t.Errorf("unexpected approval language: %s", msg.Approval.Language)
	}
	if msg.Approval.RiskLevel != types.RiskHigh {
		t.Errorf("expected RiskHigh, got %s", msg.Approval.RiskLevel)
	}
	if msg.Approval.Summary != "Test sandbox execution" {
		t.Errorf("unexpected approval summary: %s", msg.Approval.Summary)
	}
}

func TestFineGrainedMutexLocking(t *testing.T) {
	t.Run("mutex not blocked during handleApprove execution", func(t *testing.T) {
		workspace := t.TempDir()

		// Mock Gemini to propose a shell sleep command
		geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"role": "model",
							"parts": []map[string]any{
								{
									"functionCall": map[string]any{
										"name": "request_human_approval",
										"args": map[string]any{
											"summary":    "Slow background job",
											"code":       "sleep 0.4",
											"language":   "sh",
											"risk_level": "HIGH",
										},
									},
								},
							},
						},
						"finishReason": "STOP",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer geminiServer.Close()

		client := server.NewGeminiClient(
			server.WithBaseURL(geminiServer.URL),
			server.WithModel("gemini-2.5-flash"),
		)
		srv := server.NewWorkloadServer(
			server.WithGeminiClient(client),
			server.WithWorkspaceDir(workspace),
			server.WithForceMock(false),
		)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		// Step 1: Create approval request
		chatReq, _ := json.Marshal(types.ChatRequest{Message: "Run slow job"})
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(chatReq))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gemini-API-Key", "test-key")
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
		geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulate 400ms external Gemini API latency
			time.Sleep(400 * time.Millisecond)
			resp := map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"role": "model",
							"parts": []map[string]any{
								{"text": "Response after latency"},
							},
						},
						"finishReason": "STOP",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer geminiServer.Close()

		client := server.NewGeminiClient(
			server.WithBaseURL(geminiServer.URL),
			server.WithModel("gemini-2.5-flash"),
		)
		srv := server.NewWorkloadServer(
			server.WithGeminiClient(client),
			server.WithForceMock(false),
		)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		chatDone := make(chan struct{})
		go func() {
			defer close(chatDone)
			reqBody, _ := json.Marshal(types.ChatRequest{Message: "Check latency"})
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Gemini-API-Key", "test-key")
			resp, chatErr := http.DefaultClient.Do(req)
			if chatErr == nil {
				_ = resp.Body.Close()
			}
		}()

		// Wait briefly so handleChat has entered GenerateContent
		time.Sleep(100 * time.Millisecond)

		// History() must not be blocked while GenerateContent is running
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

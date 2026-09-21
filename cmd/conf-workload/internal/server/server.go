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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload/internal/engine"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// ServerOption configures WorkloadServer.
type ServerOption func(*WorkloadServer)

// WithForceMock forces deterministic offline reasoning without invoking external APIs.
func WithForceMock(forceMock bool) ServerOption {
	return func(s *WorkloadServer) {
		s.forceMock = forceMock
	}
}

// WithGeminiClient sets a custom GeminiClient.
func WithGeminiClient(client *GeminiClient) ServerOption {
	return func(s *WorkloadServer) {
		if client != nil {
			s.geminiClient = client
		}
	}
}

// WithWorkspaceDir configures the execution sandbox workspace directory.
func WithWorkspaceDir(dir string) ServerOption {
	return func(s *WorkloadServer) {
		if dir != "" {
			s.workspaceDir = dir
			s.executor = engine.NewSandboxExecutor(dir)
		}
	}
}

// WithSandboxExecutor configures a custom SandboxExecutor.
func WithSandboxExecutor(executor *engine.SandboxExecutor) ServerOption {
	return func(s *WorkloadServer) {
		if executor != nil {
			s.executor = executor
		}
	}
}

// WithMockEngine configures a custom MockEngine.
func WithMockEngine(m *engine.MockEngine) ServerOption {
	return func(s *WorkloadServer) {
		if m != nil {
			s.mockEngine = m
		}
	}
}

// WorkloadServer manages the HTTP interface and agent lifecycle inside the container sandbox.
type WorkloadServer struct {
	mux             *http.ServeMux
	executor        *engine.SandboxExecutor
	mockEngine      *engine.MockEngine
	geminiClient    *GeminiClient
	forceMock       bool
	workspaceDir    string
	mu              sync.Mutex
	pendingApproval *types.ApprovalRequest
	history         []types.ChatMessage
}

// NewWorkloadServer creates a new initialized WorkloadServer.
func NewWorkloadServer(opts ...ServerOption) *WorkloadServer {
	workspace := os.Getenv("SANDBOX_WORKSPACE")
	if workspace == "" {
		workspace = "/tmp/workspace"
	}

	mockEngine := engine.NewMockEngine()
	s := &WorkloadServer{
		mux:          http.NewServeMux(),
		workspaceDir: workspace,
		executor:     engine.NewSandboxExecutor(workspace),
		mockEngine:   mockEngine,
		geminiClient: NewGeminiClient(WithGeminiMockEngine(mockEngine)),
		forceMock:    os.Getenv("DEMO_MOCK_LLM") == "true",
	}

	for _, opt := range opts {
		opt(s)
	}

	s.routes()
	return s
}

// Handler returns the HTTP handler for the server.
func (s *WorkloadServer) Handler() http.Handler {
	return s.mux
}

// History returns a snapshot of the in-memory conversation history.
func (s *WorkloadServer) History() []types.ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	res := make([]types.ChatMessage, len(s.history))
	copy(res, s.history)
	return res
}

// PendingApproval returns a copy of any active approval request waiting for human decision.
func (s *WorkloadServer) PendingApproval() *types.ApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pendingApproval == nil {
		return nil
	}
	copyReq := *s.pendingApproval
	return &copyReq
}

func (s *WorkloadServer) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/api/chat", s.handleChat)
	s.mux.HandleFunc("/api/approve", s.handleApprove)
}

func (s *WorkloadServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (s *WorkloadServer) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json payload: %v", err), http.StatusBadRequest)
		return
	}

	apiKey := r.Header.Get("X-Gemini-API-Key")
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	userMsg := types.ChatMessage{
		ID:        fmt.Sprintf("user-%d", time.Now().UnixNano()),
		Sender:    types.SenderUser,
		Content:   req.Message,
		Timestamp: time.Now().Unix(),
	}
	s.history = append(s.history, userMsg)

	var assistantMsg *types.ChatMessage
	var err error

	if s.forceMock {
		assistantMsg = s.mockEngine.GenerateTurn(req.Message)
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		assistantMsg, err = s.geminiClient.GenerateContent(ctx, apiKey, s.history, req.Message)
		if err != nil || assistantMsg == nil {
			assistantMsg = s.mockEngine.GenerateTurn(req.Message)
		}
	}

	if assistantMsg.Approval != nil {
		s.pendingApproval = assistantMsg.Approval
	}
	s.history = append(s.history, *assistantMsg)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(assistantMsg)
}

func (s *WorkloadServer) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.ApprovalResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}

	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pendingApproval == nil || s.pendingApproval.ApprovalID != req.ApprovalID {
		http.Error(w, "no matching pending approval found", http.StatusNotFound)
		return
	}

	if req.Decision == types.DecisionReject {
		s.pendingApproval = nil
		rejectMsg := types.ChatMessage{
			ID:        fmt.Sprintf("sys-%d", time.Now().UnixNano()),
			Sender:    types.SenderSystem,
			Content:   "Execution rejected by attendee. Operation canceled.",
			Timestamp: time.Now().Unix(),
		}
		s.history = append(s.history, rejectMsg)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rejectMsg)
		return
	}

	// Decision is APPROVE
	lang := s.pendingApproval.Language
	code := s.pendingApproval.Code
	s.pendingApproval = nil

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	res, err := s.executor.Execute(ctx, lang, code)

	output := ""
	if err != nil {
		output = fmt.Sprintf("Execution error: %v", err)
		if res != nil && (res.Stderr != "" || res.ExitCode != 0) {
			output += fmt.Sprintf("\nStderr:\n%s", res.Stderr)
		}
	} else {
		output = fmt.Sprintf("Execution completed in %dms (exit code %d):\n\n%s", res.DurationMs, res.ExitCode, res.Stdout)
		if res.Stderr != "" || res.ExitCode != 0 {
			output += fmt.Sprintf("\nStderr:\n%s", res.Stderr)
		}
	}

	resultMsg := types.ChatMessage{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   output,
		Timestamp: time.Now().Unix(),
	}
	s.history = append(s.history, resultMsg)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resultMsg)
}

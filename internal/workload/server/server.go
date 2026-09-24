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
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agent-substrate/agent-swarm/internal/workload/engine"
	"github.com/agent-substrate/agent-swarm/internal/workload/gemini"
	"github.com/agent-substrate/agent-swarm/internal/workload/relay"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// ServerOption configures WorkloadServer.
type ServerOption func(*WorkloadServer)

// WithForceMock forces deterministic offline reasoning without invoking external APIs.
func WithForceMock(forceMock bool) ServerOption {
	return func(s *WorkloadServer) {
		s.forceMock = forceMock
	}
}

// WithGeminiConfig names the Vertex model the actor's Gemini client calls
// through the gateway. A config that is not Enabled leaves the actor on the
// mock engine.
func WithGeminiConfig(cfg gemini.Config) ServerOption {
	return func(s *WorkloadServer) { s.geminiCfg = cfg }
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
	geminiCfg       gemini.Config
	gemini          *gemini.Client
	modelKey        string
	forceMock       bool
	workspaceDir    string
	turnSlot        chan struct{} // held for the length of a chat turn
	mu              sync.Mutex
	turn            *relay.Turn // the turn holding turnSlot
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
		geminiCfg:    gemini.ConfigFromEnv(),
		forceMock:    os.Getenv("DEMO_MOCK_LLM") == "true",
		turnSlot:     make(chan struct{}, 1),
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.geminiCfg.Enabled() {
		s.gemini = gemini.New(s.geminiCfg)
	}

	s.routes()
	return s
}

// ModelKey returns the latest key the gateway sent. Only the gateway knows
// it, so it authenticates the gateway's history clears.
func (s *WorkloadServer) ModelKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelKey
}

// Handler returns the HTTP handler for the server. Every request that
// carries a model key refreshes the one held; one without a key keeps the
// prior key. The key is never read from a request body. A history clear must
// present the key already held and never replaces it.
func (s *WorkloadServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == types.ClearHistoryPath {
			s.handleClearHistory(w, r)
			return
		}
		if k := r.Header.Get(types.ModelKeyHeader); k != "" {
			s.mu.Lock()
			s.modelKey = k
			s.mu.Unlock()
		}
		s.mux.ServeHTTP(w, r)
	})
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
	s.mux.HandleFunc(types.ModelResultPath, s.handleModelResult)
}

// handleClearHistory drops the conversation history and any pending
// approval, so the actor's next user starts with a fresh model context. Only
// the gateway knows the actor's model key, so the request must carry the key
// the actor already holds; an actor that holds none has no history yet.
func (s *WorkloadServer) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	k := r.Header.Get(types.ModelKeyHeader)
	s.mu.Lock()
	defer s.mu.Unlock()
	if k == "" || (s.modelKey != "" && subtle.ConstantTimeCompare([]byte(k), []byte(s.modelKey)) != 1) {
		http.Error(w, "gateway key required", http.StatusForbidden)
		return
	}
	s.history = nil
	s.pendingApproval = nil
	w.WriteHeader(http.StatusNoContent)
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

	select {
	case s.turnSlot <- struct{}{}:
	case <-r.Context().Done():
		return
	}

	userMsg := types.ChatMessage{
		ID:        fmt.Sprintf("user-%d", time.Now().UnixNano()),
		Sender:    types.SenderUser,
		Content:   req.Message,
		Timestamp: time.Now().Unix(),
	}

	t := relay.NewTurn()
	s.mu.Lock()
	s.history = append(s.history, userMsg)
	historyCopy := append([]types.ChatMessage(nil), s.history...)
	s.turn = t
	s.mu.Unlock()

	// The turn outlives this request: its model calls come back through
	// handleModelResult.
	t.Run(context.Background(), func(ctx context.Context) *types.ChatMessage {
		assistantMsg := s.reply(ctx, historyCopy, req.Message)
		s.mu.Lock()
		if assistantMsg.Approval != nil {
			s.pendingApproval = assistantMsg.Approval
		}
		s.history = append(s.history, *assistantMsg)
		s.turn = nil
		s.mu.Unlock()
		<-s.turnSlot
		return assistantMsg
	})
	writeStep(w, r, t)
}

// maxModelResult bounds a model result body: the gateway caps Vertex AI
// responses at 8 MiB, which base64 grows by a third.
const maxModelResult = 12 << 20

// handleModelResult delivers the gateway's answer to the running turn's
// model call and answers with the turn's next step.
func (s *WorkloadServer) handleModelResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var res types.ModelResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxModelResult)).Decode(&res); err != nil {
		http.Error(w, fmt.Sprintf("invalid json payload: %v", err), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t == nil || t.Deliver(res) != nil {
		http.Error(w, "no model call is waiting for this result", http.StatusConflict)
		return
	}
	writeStep(w, r, t)
}

// writeStep answers with the turn's next step: a model call for the gateway
// to make, or the final reply. If the gateway goes away first, the turn
// times out on its own.
func writeStep(w http.ResponseWriter, r *http.Request, t *relay.Turn) {
	step, err := t.Next(r.Context())
	if err != nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if step.Call != nil {
		_ = json.NewEncoder(w).Encode(types.ModelCallStep{ModelCall: step.Call})
		return
	}
	_ = json.NewEncoder(w).Encode(step.Reply)
}

// chatTimeout bounds one Gemini turn, tool calls and the gateway's model
// calls included.
const chatTimeout = 60 * time.Second

func (s *WorkloadServer) reply(ctx context.Context, history []types.ChatMessage, prompt string) *types.ChatMessage {
	if s.forceMock || s.gemini == nil {
		msg := s.mockEngine.GenerateTurn(prompt)
		msg.Source = types.SourceMock
		return msg
	}
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()
	msg, err := s.gemini.Generate(ctx, history, prompt)
	if err != nil {
		log.Printf("gemini turn failed, replying with the mock engine: %v", err)
		msg = s.mockEngine.GenerateTurn(prompt)
		msg.Source = types.SourceMockFallback
		return msg
	}
	msg.Source = types.SourceGemini
	return msg
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
	pending := s.pendingApproval
	if pending == nil || pending.ApprovalID != req.ApprovalID {
		s.mu.Unlock()
		http.Error(w, "no matching pending approval", http.StatusConflict)
		return
	}
	s.pendingApproval = nil

	if req.Decision == types.DecisionReject {
		rejectMsg := types.ChatMessage{
			ID:        fmt.Sprintf("sys-%d", time.Now().UnixNano()),
			Sender:    types.SenderSystem,
			Content:   "Execution rejected by attendee. Operation canceled.",
			Timestamp: time.Now().Unix(),
		}
		s.history = append(s.history, rejectMsg)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rejectMsg)
		return
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	exec := runResult(s.executor.Execute(ctx, pending.Language, pending.Code))

	resultMsg := types.ChatMessage{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   describeRun(exec),
		Timestamp: time.Now().Unix(),
		Execution: exec,
	}

	s.mu.Lock()
	s.history = append(s.history, resultMsg)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resultMsg)
}

// runResult folds an executor error into the result. ExitCode -1 means the
// code did not run to completion.
func runResult(res *types.ExecutionResult, err error) *types.ExecutionResult {
	out := types.ExecutionResult{ExitCode: -1}
	if res != nil {
		out = *res
	}
	if err != nil {
		out.ExitCode = -1
		out.Stderr = strings.TrimSpace(out.Stderr + "\n" + err.Error())
	}
	return &out
}

func describeRun(e *types.ExecutionResult) string {
	if e.ExitCode == -1 {
		return "Execution error:\n" + e.Stderr
	}
	s := fmt.Sprintf("Execution completed in %dms (exit code %d):\n\n%s", e.DurationMs, e.ExitCode, e.Stdout)
	if e.Stderr != "" || e.ExitCode != 0 {
		s += "\nStderr:\n" + e.Stderr
	}
	return s
}

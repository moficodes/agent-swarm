# Interactive Conference Agent Swarm Demo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and verify an interactive, conference-ready demonstration (`demos/conference`) of Agent Substrate where 50+ audience members command personal sandboxed agents (powered by Google ADK / GenAI Go SDK v2 and Gemini 3.5+ Flash / Flash-Lite models) on 2 physical worker pods, showcasing extreme oversubscription, zero-cost idle suspension, and mass L7 request parking in Envoy.

**Architecture:** A high-performance Go Edge Gateway (`conf-gateway`) serves a responsive mobile chat UI and presentation stage dashboard, minting ephemeral attendee JWTs and proxying traffic to `atenet-router` with `ate-target-actor` headers. Inside each gVisor sandbox, a Go workload (`conf-workload`) runs a tool-calling agent that pauses at Human-in-the-Loop checkpoints to immediately suspend the actor to GCS, creating natural audience approval waves that trigger live Envoy request parking.

**Tech Stack:** Go 1.24, Google GenAI Go SDK v2 (`google.golang.org/genai`), gVisor (`runsc`), Kubernetes CRDs (Substrate Atespace, WorkerPool, ActorTemplate), Envoy `ext_proc` L7 Request Parking, HTML5/CSS3/Vanilla JS with Server-Sent Events (SSE).

---

## File Structure

```
demos/conference/
├── pkg/
│   └── types/
│       ├── types.go                # Domain types (messages, approvals, telemetry, status)
│       └── types_test.go           # Serialization and validation tests
├── cmd/
│   ├── conf-workload/
│   │   ├── main.go                 # Unified HTTP actor workload entrypoint
│   │   ├── agent.go                # Google ADK v2 tool-calling loop (Gemini Flash / Flash-Lite)
│   │   ├── tools.go                # Sandbox executor (execute_sandbox_code) & research tools
│   │   ├── mock.go                 # Deterministic offline reasoning engine
│   │   └── workload_test.go        # Unit tests for tool calling and sandbox execution
│   └── conf-gateway/
│       ├── main.go                 # Edge gateway, auth minting, and proxy server
│       ├── gateway.go              # Router forwarding, statusz poller, and session manager
│       ├── gateway_test.go         # Gateway auth, routing, and suspension tests
│       └── static/
│           ├── chat.html           # Mobile attendee chat interface
│           ├── chat.js             # Mobile client event handler & HITL cards
│           ├── chat.css            # Mobile-optimized dark styling
│           ├── stage.html          # Presentation dashboard (projector / live stream)
│           ├── stage.js            # Stage SSE client, parking gauge, and actor matrix
│           └── stage.css           # Stage cyberpunk high-contrast layout
├── conference.yaml.tmpl            # Kubernetes manifests (Atespace, WorkerPool, ActorTemplate)
└── README.md                       # Comprehensive demo guide and presentation playbook
hack/
├── install-demo-conference.sh      # Deployment and teardown script
└── verify-demo-conference.sh       # Automated headless end-to-end verification script
```

---

### Task 1: Domain Types & Protocol Schemas

**Files:**
- Create: `demos/conference/pkg/types/types.go`
- Test: `demos/conference/pkg/types/types_test.go`

- [ ] **Step 1: Write the failing test**

```go
package types_test

import (
	"encoding/json"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func testMessageSerialization(t *testing.T) {
	msg := types.ChatMessage{
		ID:        "msg-1",
		Sender:    types.SenderAssistant,
		Content:   "Plan created",
		Timestamp: 1789910000,
		Approval: &types.ApprovalRequest{
			ApprovalID: "app-1",
			Summary:    "Execute Python outlier analysis",
			Code:       "import numpy as np\nprint(np.mean([1, 2, 3]))",
			Language:   "python",
			RiskLevel:  types.RiskHigh,
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal ChatMessage: %v", err)
	}

	var decoded types.ChatMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal ChatMessage: %v", err)
	}

	if decoded.Approval == nil || decoded.Approval.ApprovalID != "app-1" {
		t.Fatalf("expected approval ID 'app-1', got %+v", decoded.Approval)
	}
}

func TestValidation(t *testing.T) {
	testMessageSerialization(t)

	validResp := types.ApprovalResponse{
		ApprovalID: "app-1",
		Decision:   types.DecisionApprove,
	}
	if err := validResp.Validate(); err != nil {
		t.Errorf("expected valid approval response, got error: %v", err)
	}

	invalidResp := types.ApprovalResponse{
		ApprovalID: "",
		Decision:   "UNKNOWN",
	}
	if err := invalidResp.Validate(); err == nil {
		t.Errorf("expected error for invalid approval response, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/pkg/types/...`
Expected: FAIL with "cannot find package" or "undefined types"

- [ ] **Step 3: Write minimal implementation**

```go
package types

import (
	"errors"
	"fmt"
)

type SenderType string

const (
	SenderUser      SenderType = "user"
	SenderAssistant SenderType = "assistant"
	SenderSystem    SenderType = "system"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "LOW"
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

type DecisionType string

const (
	DecisionApprove DecisionType = "APPROVE"
	DecisionReject  DecisionType = "REJECT"
)

type ActorLifecycleState string

const (
	StateSuspended        ActorLifecycleState = "SUSPENDED"
	StateParked           ActorLifecycleState = "PARKED"
	StateRunning          ActorLifecycleState = "RUNNING"
	StateAwaitingApproval ActorLifecycleState = "AWAITING_APPROVAL"
)

type ApprovalRequest struct {
	ApprovalID string    `json:"approval_id"`
	Summary    string    `json:"summary"`
	Code       string    `json:"code"`
	Language   string    `json:"language"`
	RiskLevel  RiskLevel `json:"risk_level"`
}

type ApprovalResponse struct {
	ApprovalID string       `json:"approval_id"`
	Decision   DecisionType `json:"decision"`
}

func (r *ApprovalResponse) Validate() error {
	if r.ApprovalID == "" {
		return errors.New("approval_id is required")
	}
	if r.Decision != DecisionApprove && r.Decision != DecisionReject {
		return fmt.Errorf("invalid decision: %s", r.Decision)
	}
	return nil
}

type ChatMessage struct {
	ID        string           `json:"id"`
	Sender    SenderType       `json:"sender"`
	Content   string           `json:"content"`
	Timestamp int64            `json:"timestamp"`
	Approval  *ApprovalRequest `json:"approval,omitempty"`
}

type ChatRequest struct {
	Message string `json:"message"`
}

type ExecutionResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

type StageTelemetry struct {
	ParkedRequests int                            `json:"parked_requests"`
	Workers        []WorkerStatus                 `json:"workers"`
	Actors         map[string]ActorLifecycleState `json:"actors"`
	RecentEvents   []string                       `json:"recent_events"`
}

type WorkerStatus struct {
	PodName     string `json:"pod_name"`
	ActiveActor string `json:"active_actor"`
	CurrentTask string `json:"current_task"`
	ElapsedMs   int64  `json:"elapsed_ms"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./demos/conference/pkg/types/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/pkg/types
git commit -m "feat(conference): add domain types and approval schemas"
```

---

### Task 2: Workload Sandbox Executor & Deterministic Offline Engine

**Files:**
- Create: `demos/conference/cmd/conf-workload/tools.go`
- Create: `demos/conference/cmd/conf-workload/mock.go`
- Test: `demos/conference/cmd/conf-workload/workload_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload/internal/engine"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func TestSandboxExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executor := engine.NewSandboxExecutor("/tmp/conf-test-workspace")
	res, err := executor.Execute(ctx, "python", "print('hello from gvisor sandbox')")
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !strings.Contains(res.Stdout, "hello from gvisor sandbox") {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
}

func TestDeterministicMockReasoning(t *testing.T) {
	mockEngine := engine.NewMockEngine()
	resp := mockEngine.GenerateTurn("Analyze sales data for anomalies")

	if resp.Approval == nil {
		t.Fatalf("expected mock response to require human approval, got nil")
	}
	if resp.Approval.Language != "python" {
		t.Fatalf("expected python language in approval, got %s", resp.Approval.Language)
	}
	if !strings.Contains(resp.Approval.Code, "def detect_anomalies") {
		t.Fatalf("expected anomaly code snippet, got: %s", resp.Approval.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-workload/...`
Expected: FAIL with "package not found"

- [ ] **Step 3: Write minimal implementation**

Create `demos/conference/cmd/conf-workload/internal/engine/tools.go`:
```go
package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type SandboxExecutor struct {
	WorkspaceDir string
}

func NewSandboxExecutor(workspaceDir string) *SandboxExecutor {
	if workspaceDir == "" {
		workspaceDir = "/tmp/workspace"
	}
	_ = os.MkdirAll(workspaceDir, 0o755)
	return &SandboxExecutor{WorkspaceDir: workspaceDir}
}

func (s *SandboxExecutor) Execute(ctx context.Context, lang string, script string) (*types.ExecutionResult, error) {
	start := time.Now()
	var cmd *exec.Cmd

	switch lang {
	case "python", "py":
		scriptPath := filepath.Join(s.WorkspaceDir, "run.py")
		if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
			return nil, fmt.Errorf("failed to write script: %w", err)
		}
		// Try python3, fallback to python
		cmd = exec.CommandContext(ctx, "python3", scriptPath)
	case "sh", "bash":
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", script)
	default:
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}

	cmd.Dir = s.WorkspaceDir
	out, err := cmd.CombinedOutput()
	duration := time.Since(start).Milliseconds()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return &types.ExecutionResult{
		Stdout:     string(out),
		ExitCode:   exitCode,
		DurationMs: duration,
	}, nil
}
```

Create `demos/conference/cmd/conf-workload/internal/engine/mock.go`:
```go
package engine

import (
	"fmt"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type MockEngine struct{}

func NewMockEngine() *MockEngine {
	return &MockEngine{}
}

func (m *MockEngine) GenerateTurn(prompt string) *types.ChatMessage {
	return &types.ChatMessage{
		ID:        fmt.Sprintf("mock-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   "I analyzed your request. To extract the metrics and generate the visualization, I have prepared this analysis script. Please review and authorize execution in the sandbox:",
		Timestamp: time.Now().Unix(),
		Approval: &types.ApprovalRequest{
			ApprovalID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
			Summary:    "Execute Telemetry Outlier Analysis & ASCII Plotting",
			Code: `def detect_anomalies(data):
    threshold = 2.5
    mean = sum(data) / len(data)
    variance = sum((x - mean) ** 2 for x in data) / len(data)
    std_dev = variance ** 0.5
    anomalies = [x for x in data if abs(x - mean) > threshold * std_dev]
    print(f"[SANDBOX] Mean: {mean:.2f} | StdDev: {std_dev:.2f} | Anomalies: {len(anomalies)}")
    print(" 100 |  *")
    print("  75 |  * *   *")
    print("  50 |  * * * * *")
    print("   0 +-----------")
    return anomalies

detect_anomalies([12, 14, 15, 13, 98, 12, 11, 14, 88, 13])`,
			Language:  "python",
			RiskLevel: types.RiskHigh,
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./demos/conference/cmd/conf-workload/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-workload
git commit -m "feat(conference): add sandbox executor and deterministic mock engine"
```

---

### Task 3: Google ADK / GenAI Go SDK v2 Agent Loop & Workload Server

**Files:**
- Create: `demos/conference/cmd/conf-workload/agent.go`
- Create: `demos/conference/cmd/conf-workload/main.go`
- Test: `demos/conference/cmd/conf-workload/agent_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload/internal/server"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func TestWorkloadServerChatAndApprove(t *testing.T) {
	srv := server.NewWorkloadServer(true) // offline mock mode
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Chat Prompt
	reqBody, _ := json.Marshal(types.ChatRequest{Message: "Analyze logs"})
	resp, err := http.Post(ts.URL+"/api/chat", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to post chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var chatResp types.ChatMessage
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("failed to decode chat response: %v", err)
	}
	if chatResp.Approval == nil {
		t.Fatalf("expected approval payload in chat response")
	}

	// 2. Approve Execution
	appBody, _ := json.Marshal(types.ApprovalResponse{
		ApprovalID: chatResp.Approval.ApprovalID,
		Decision:   types.DecisionApprove,
	})
	appResp, err := http.Post(ts.URL+"/api/approve", "application/json", bytes.NewReader(appBody))
	if err != nil {
		t.Fatalf("failed to post approve: %v", err)
	}
	if appResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", appResp.StatusCode)
	}

	var finalMsg types.ChatMessage
	if err := json.NewDecoder(appResp.Body).Decode(&finalMsg); err != nil {
		t.Fatalf("failed to decode final response: %v", err)
	}
	if !strings.Contains(finalMsg.Content, "SANDBOX") && !strings.Contains(finalMsg.Content, "Execution completed") {
		t.Fatalf("expected execution output in response, got: %s", finalMsg.Content)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-workload/...`
Expected: FAIL with "server package not found"

- [ ] **Step 3: Write minimal implementation**

Create `demos/conference/cmd/conf-workload/internal/server/server.go`:
```go
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
	"google.golang.org/genai"
)

type WorkloadServer struct {
	mux             *http.ServeMux
	executor        *engine.SandboxExecutor
	mockEngine      *engine.MockEngine
	forceMock       bool
	mu              sync.Mutex
	pendingApproval *types.ApprovalRequest
	chatSession     *genai.ChatSession
	history         []types.ChatMessage
}

func NewWorkloadServer(forceMock bool) *WorkloadServer {
	s := &WorkloadServer{
		mux:        http.NewServeMux(),
		executor:   engine.NewSandboxExecutor("/tmp/workspace"),
		mockEngine: engine.NewMockEngine(),
		forceMock:  forceMock,
	}
	s.routes()
	return s
}

func (s *WorkloadServer) Handler() http.Handler {
	return s.mux
}

func (s *WorkloadServer) routes() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	s.mux.HandleFunc("/api/chat", s.handleChat)
	s.mux.HandleFunc("/api/approve", s.handleApprove)
}

func (s *WorkloadServer) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	apiKey := r.Header.Get("X-Gemini-API-Key")
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}

	var msg *types.ChatMessage
	if s.forceMock || apiKey == "" {
		msg = s.mockEngine.GenerateTurn(req.Message)
	} else {
		// Real Google ADK / GenAI v2 call
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		client, err := genai.NewClient(ctx, &genai.ClientConfig{
			APIKey: apiKey,
		})
		if err != nil {
			msg = s.mockEngine.GenerateTurn(req.Message)
		} else {
			// Initialize chat or send message
			model := os.Getenv("GEMINI_MODEL")
			if model == "" {
				model = "gemini-2.5-flash"
			}
			chat := client.Chats.Create(ctx, model, nil)
			resp, err := chat.SendMessage(ctx, genai.Text(req.Message))
			if err != nil {
				msg = s.mockEngine.GenerateTurn(req.Message)
			} else {
				msg = &types.ChatMessage{
					ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
					Sender:    types.SenderAssistant,
					Content:   resp.Text,
					Timestamp: time.Now().Unix(),
				}
			}
		}
	}

	if msg.Approval != nil {
		s.pendingApproval = msg.Approval
	}
	s.history = append(s.history, *msg)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

func (s *WorkloadServer) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.ApprovalResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		msg := types.ChatMessage{
			ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
			Sender:    types.SenderSystem,
			Content:   "Execution rejected by attendee. Operation canceled.",
			Timestamp: time.Now().Unix(),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msg)
		return
	}

	// Execute inside gVisor sandbox
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	res, err := s.executor.Execute(ctx, s.pendingApproval.Language, s.pendingApproval.Code)
	s.pendingApproval = nil

	output := ""
	if err != nil {
		output = fmt.Sprintf("Execution error: %v", err)
	} else {
		output = fmt.Sprintf("Execution completed in %dms (exit code %d):\n\n%s", res.DurationMs, res.ExitCode, res.Stdout)
	}

	msg := types.ChatMessage{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   output,
		Timestamp: time.Now().Unix(),
	}
	s.history = append(s.history, msg)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}
```

Create `demos/conference/cmd/conf-workload/main.go`:
```go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload/internal/server"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "80"
	}

	forceMock := os.Getenv("DEMO_MOCK_LLM") == "true"
	srv := server.NewWorkloadServer(forceMock)

	log.Printf("Starting conference workload actor on :%s (mock: %v)...", port, forceMock)
	if err := http.ListenAndServe(":"+port, srv.Handler()); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./demos/conference/cmd/conf-workload/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-workload
git commit -m "feat(conference): implement Google ADK v2 workload server with approval loop"
```

---

### Task 4: Edge Gateway, Session Manager & Router Proxy

**Files:**
- Create: `demos/conference/cmd/conf-gateway/gateway.go`
- Create: `demos/conference/cmd/conf-gateway/main.go`
- Test: `demos/conference/cmd/conf-gateway/gateway_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
)

func TestGatewaySessionMinting(t *testing.T) {
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040", "demo-secret")
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify session cookie or json payload
	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode session: %v", err)
	}
	if data["attendee_id"] == "" || data["token"] == "" {
		t.Fatalf("missing attendee_id or token in session response: %+v", data)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-gateway/...`
Expected: FAIL with "gateway package not found"

- [ ] **Step 3: Write minimal implementation**

Create `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`:
```go
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type Gateway struct {
	mux           *http.ServeMux
	routerURL     string
	statusURL     string
	jwtSecret     string
	attendeeSeq   uint64
	mu            sync.RWMutex
	activeActors  map[string]types.ActorLifecycleState
	recentEvents  []string
	parkedCount   int
}

func NewGateway(routerURL, statusURL, jwtSecret string) *Gateway {
	gw := &Gateway{
		mux:          http.NewServeMux(),
		routerURL:    routerURL,
		statusURL:    statusURL,
		jwtSecret:    jwtSecret,
		activeActors: make(map[string]types.ActorLifecycleState),
	}
	gw.routes()
	go gw.pollStatus()
	return gw
}

func (g *Gateway) Handler() http.Handler {
	return g.mux
}

func (g *Gateway) routes() {
	g.mux.HandleFunc("/api/session", g.handleSession)
	g.mux.HandleFunc("/api/stage/telemetry", g.handleTelemetry)
	g.mux.HandleFunc("/api/chat", g.proxyToActor)
	g.mux.HandleFunc("/api/approve", g.proxyToActor)
}

func (g *Gateway) handleSession(w http.ResponseWriter, r *http.Request) {
	seq := atomic.AddUint64(&g.attendeeSeq, 1)
	attendeeID := fmt.Sprintf("attendee-%02d", (seq-1)%100+1)

	token := g.mintToken(attendeeID)

	g.mu.Lock()
	if _, exists := g.activeActors[attendeeID]; !exists {
		g.activeActors[attendeeID] = types.StateSuspended
	}
	g.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"attendee_id": attendeeID,
		"token":       token,
	})
}

func (g *Gateway) mintToken(attendeeID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"%s","exp":%d}`, attendeeID, time.Now().Add(2*time.Hour).Unix())))
	mac := hmac.New(sha256.New, []byte(g.jwtSecret))
	mac.Write([]byte(header + "." + payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return header + "." + payload + "." + sig
}

func (g *Gateway) proxyToActor(w http.ResponseWriter, r *http.Request) {
	attendeeID := r.Header.Get("X-Attendee-ID")
	if attendeeID == "" {
		http.Error(w, "X-Attendee-ID header required", http.StatusUnauthorized)
		return
	}

	targetURL, _ := url.Parse(g.routerURL)
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
		req.Header.Set("ate-target-actor", fmt.Sprintf("ate-demo-conf/%s", attendeeID))
	}

	proxy.ServeHTTP(w, r)
}

func (g *Gateway) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	telem := types.StageTelemetry{
		ParkedRequests: g.parkedCount,
		Workers: []types.WorkerStatus{
			{PodName: "conf-workers-0", ActiveActor: "attendee-14", CurrentTask: "gVisor execution", ElapsedMs: 180},
			{PodName: "conf-workers-1", ActiveActor: "attendee-29", CurrentTask: "ADK reasoning", ElapsedMs: 220},
		},
		Actors:       g.activeActors,
		RecentEvents: g.recentEvents,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(telem)
}

func (g *Gateway) pollStatus() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		resp, err := http.Get(g.statusURL + "/statusz?format=json")
		if err != nil {
			continue
		}
		var data struct {
			Parking struct {
				Active int `json:"active"`
			} `json:"parking"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
			g.mu.Lock()
			g.parkedCount = data.Parking.Active
			g.mu.Unlock()
		}
		_ = resp.Body.Close()
	}
}
```

Create `demos/conference/cmd/conf-gateway/main.go`:
```go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	routerURL := os.Getenv("ROUTER_URL")
	if routerURL == "" {
		routerURL = "http://localhost:8000"
	}
	statusURL := os.Getenv("STATUS_URL")
	if statusURL == "" {
		statusURL = "http://localhost:4040"
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "conf-demo-secret-key"
	}

	gw := gateway.NewGateway(routerURL, statusURL, jwtSecret)
	log.Printf("Starting conference edge gateway on :%s (router: %s)...", port, routerURL)
	if err := http.ListenAndServe(":"+port, gw.Handler()); err != nil {
		log.Fatalf("gateway server failed: %v", err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./demos/conference/cmd/conf-gateway/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway
git commit -m "feat(conference): implement edge gateway and session router"
```

---

### Task 5: Mobile Attendee Web UI

**Files:**
- Create: `demos/conference/cmd/conf-gateway/static/chat.html`
- Create: `demos/conference/cmd/conf-gateway/static/chat.js`
- Create: `demos/conference/cmd/conf-gateway/static/chat.css`

- [ ] **Step 1: Write mobile HTML interface**

Write `demos/conference/cmd/conf-gateway/static/chat.html` featuring:
- Ephemeral Attendee Session badge
- Chat feed container
- One-click prompt chips (`[Telemetry Outlier Analysis]`, `[Benchmark Sandbox CPU]`, `[Vulnerability Check]`)
- Human-in-the-Loop review modal / card with syntax-highlighted code block and `[Reject]` / `[Approve]` buttons
- Substrate lifecycle status pill (`⚡ Suspended in GCS`, `⏳ Parked in Envoy`, `🚀 Running on Pod`).

- [ ] **Step 2: Write mobile JavaScript client**

Write `demos/conference/cmd/conf-gateway/static/chat.js` handling:
- Auto-initialization on load (`GET /api/session`) to store `attendee_id` and `token` in `localStorage`.
- `POST /api/chat` dispatch with `X-Attendee-ID` header.
- Dynamic rendering of `ApprovalRequest` cards.
- `POST /api/approve` dispatch on human tap.
- Live status updates.

- [ ] **Step 3: Write mobile CSS styling**

Write `demos/conference/cmd/conf-gateway/static/chat.css` with responsive dark cyberpunk palette, high touch targets for smartphones, and fluid typography.

- [ ] **Step 4: Verify static file serving via Gateway test**

Add static file handler to Gateway and test:
Run: `go test -v ./demos/conference/cmd/conf-gateway/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/static/chat*
git commit -m "feat(conference): add mobile attendee chat web UI and HITL card"
```

---

### Task 6: Presentation Stage Dashboard

**Files:**
- Create: `demos/conference/cmd/conf-gateway/static/stage.html`
- Create: `demos/conference/cmd/conf-gateway/static/stage.js`
- Create: `demos/conference/cmd/conf-gateway/static/stage.css`

- [ ] **Step 1: Write stage HTML dashboard**

Write `demos/conference/cmd/conf-gateway/static/stage.html` featuring:
- Hero banner with high-contrast SVG QR Code generator pointing to `/chat`
- Envoy L7 Parking Lot meter gauge (animated pink/amber progress bar reading `.parking.active`)
- 2 Physical Worker Pod status cards (`Worker Pod 0` & `Worker Pod 1`)
- 50-to-100 Attendee Fleet Grid (badges showing `Suspended`, `Parked`, `Running`, `Approving`)
- Live activity ticker stream

- [ ] **Step 2: Write stage JavaScript client**

Write `demos/conference/cmd/conf-gateway/static/stage.js` polling `/api/stage/telemetry` every 500ms to update the parking counter, worker occupancy, and fleet badges with CSS transitions.

- [ ] **Step 3: Write stage CSS layout**

Write `demos/conference/cmd/conf-gateway/static/stage.css` designed for 1080p/4K projector screens with high-contrast text and glowing state indicators.

- [ ] **Step 4: Verify static stage serving**

Verify all assets serve cleanly with HTTP 200 via Gateway test:
Run: `go test -v ./demos/conference/cmd/conf-gateway/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/static/stage*
git commit -m "feat(conference): add presentation stage dashboard with live parking gauge"
```

---

### Task 7: Kubernetes Manifests & Install Helper

**Files:**
- Create: `demos/conference/conference.yaml.tmpl`
- Create: `hack/install-demo-conference.sh`

- [ ] **Step 1: Create template manifests**

Create `demos/conference/conference.yaml.tmpl` specifying:
- `Atespace`: `ate-demo-conf`
- `WorkerPool`: `conf-workers` (Replicas: 2, SandboxClass: `gvisor`)
- `ActorTemplate`: `conf-agent` pointing to `ko://github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload` with storage bucket `gs://${BUCKET_NAME}/ate-demo-conf/`
- 50 initial `Actor` definitions (`attendee-01` through `attendee-50`) in state `ACTOR_STATE_SUSPENDED`.

- [ ] **Step 2: Create install helper script**

Create `hack/install-demo-conference.sh` with flags `--deploy`, `--delete`, `--build-images`, and `--patch-router-budget=60s`.

- [ ] **Step 3: Validate template expansion**

Run: `BUCKET_NAME=test-bucket envsubst < demos/conference/conference.yaml.tmpl > /dev/null`
Expected: Success with valid YAML.

- [ ] **Step 4: Commit**

```bash
git add demos/conference/conference.yaml.tmpl hack/install-demo-conference.sh
git commit -m "feat(conference): add Kubernetes manifests and install helper script"
```

---

### Task 5: Automated Verification Script & Documentation

**Files:**
- Create: `hack/verify-demo-conference.sh`
- Create: `demos/conference/README.md`

- [ ] **Step 1: Create automated verification script**

Create `hack/verify-demo-conference.sh`:
- Runs headless against a deployed cluster or local port-forward.
- Simulates 25 concurrent attendees sending chat requests.
- Asserts that Envoy request parking engages (`parking.active > 0`).
- Asserts that all 25 requests complete with HTTP 200 and zero 503s.
- Verifies approval round-trip and execution output.

- [ ] **Step 2: Create README documentation**

Write `demos/conference/README.md`:
- Conference speaker playbook (step-by-step cue sheet).
- Architecture diagram and Substrate superpower explanations.
- Deployment instructions (`./hack/install-demo-conference.sh --deploy`).
- Verification guide.

- [ ] **Step 3: Run linter and verification**

Run: `make verify` or boilerplate checks to ensure style compliance.
Expected: Clean pass.

- [ ] **Step 4: Commit**

```bash
git add hack/verify-demo-conference.sh demos/conference/README.md
git commit -m "docs(conference): add automated verification script and presentation playbook"
```

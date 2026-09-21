# Conference Demo Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement 4 major enhancements for the Interactive Conference Agent Swarm Demo (`demos/conference`): a 1-click stage audience simulation engine (25–50 virtual attendees), an interactive Actor Spotlight & Inspection Drawer, real-time Server-Sent Events (SSE) state synchronization (eliminating faked timers), and a 1-click cluster reset controller.

**Architecture:** Extend `conf-gateway` with SSE event streaming (`/api/events`), actor inspector endpoint (`/api/actor/{id}`), reset controller (`/api/reset`), and simulation runner (`/api/simulation`). Update `/stage` with simulation buttons, reset control, and a slide-out Spotlight drawer. Update `/chat` to bind the status pill to authentic SSE events.

**Tech Stack:** Go 1.24, Google GenAI Go SDK v2 (`google.golang.org/genai`), Server-Sent Events (SSE), HTML5/CSS3/Vanilla JS, Kubernetes Substrate CLI (`kubectl-ate`).

---

## File Structure

```
demos/conference/
├── pkg/
│   ├── types/
│   │   ├── types.go                # Add ActorDetails, SimulationRequest, SimulationStatus types
│   │   └── types_test.go           # Serialization and validation tests
│   └── ai/
│       └── gemini.go               # Vertex AI client
├── cmd/
│   └── conf-gateway/
│       ├── main.go                 # Configuration and option injection
│       ├── internal/
│       │   └── gateway/
│       │       ├── gateway.go      # Core routes, SSE broker, inspector, reset, and simulation
│       │       ├── sse.go          # Event broker & SSE client broadcaster
│       │       ├── simulation.go   # Virtual attendee simulation runner
│       │       └── gateway_test.go # Comprehensive unit tests for new endpoints
│       └── static/
│           ├── stage.html          # Stage UI with simulation buttons, reset, and spotlight drawer
│           ├── stage.js            # Click-to-inspect, simulation trigger, reset handler
│           ├── stage.css           # Spotlight drawer slide-out CSS & simulation badges
│           ├── chat.html           # Mobile attendee interface
│           └── chat.js             # Connects to /api/events SSE stream, replaces synthetic timer
hack/
├── verify-demo-conference.sh       # Automated verification with simulation and reset checks
└── install-demo-conference.sh      # Cluster deployment helper
```

---

### Task 1: Domain Types for Simulation, Inspection & SSE Events

**Files:**
- Modify: `demos/conference/pkg/types/types.go`
- Test: `demos/conference/pkg/types/types_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestEnhancementTypesSerialization(t *testing.T) {
	details := types.ActorDetails{
		ActorID:           "attendee-07",
		State:             types.StateAwaitingApproval,
		WorkerPod:         "conf-workers-0",
		SnapshotLocation:  "gs://test-bucket/ate-demo-conf/actors/...",
		SnapshotSizeBytes: 384000,
		ChatHistory: []types.ChatMessage{
			{ID: "1", Sender: types.SenderUser, Content: "Hello"},
		},
		PendingApproval: &types.ApprovalRequest{
			ApprovalID: "app-1",
			Summary:    "Run script",
			Code:       "print(1)",
			Language:   "python",
			RiskLevel:  types.RiskHigh,
		},
		LastResult: &types.ExecutionResult{
			Stdout:     "output",
			ExitCode:   0,
			DurationMs: 25,
		},
	}

	data, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("failed to marshal ActorDetails: %v", err)
	}

	var decoded types.ActorDetails
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal ActorDetails: %v", err)
	}
	if decoded.ActorID != "attendee-07" || decoded.State != types.StateAwaitingApproval {
		t.Fatalf("unexpected decoded ActorDetails: %+v", decoded)
	}

	simReq := types.SimulationRequest{
		Action: "start",
		Count:  25,
	}
	if err := simReq.Validate(); err != nil {
		t.Errorf("expected valid simulation request, got: %v", err)
	}

	simReqInvalid := types.SimulationRequest{
		Action: "invalid",
		Count:  0,
	}
	if err := simReqInvalid.Validate(); err == nil {
		t.Errorf("expected error for invalid simulation request, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/pkg/types/...`
Expected: FAIL with undefined `ActorDetails`, `SimulationRequest`.

- [ ] **Step 3: Implement minimal code**

In `demos/conference/pkg/types/types.go`:
```go
// ActorDetails provides a full snapshot of an actor's current state and telemetry for the inspector drawer.
type ActorDetails struct {
	ActorID           string                 `json:"actor_id"`
	State             ActorLifecycleState    `json:"state"`
	WorkerPod         string                 `json:"worker_pod"`
	SnapshotLocation  string                 `json:"snapshot_location"`
	SnapshotSizeBytes int64                  `json:"snapshot_size_bytes"`
	ChatHistory       []ChatMessage          `json:"chat_history"`
	PendingApproval   *ApprovalRequest       `json:"pending_approval,omitempty"`
	LastResult        *ExecutionResult       `json:"last_result,omitempty"`
}

// SimulationRequest specifies actions to control the virtual attendee swarm.
type SimulationRequest struct {
	Action string `json:"action"` // "start" or "stop"
	Count  int    `json:"count,omitempty"`
}

func (s *SimulationRequest) Validate() error {
	if s == nil {
		return errors.New("simulation request is nil")
	}
	if s.Action != "start" && s.Action != "stop" {
		return fmt.Errorf("invalid action: %s (expected 'start' or 'stop')", s.Action)
	}
	if s.Action == "start" && (s.Count < 1 || s.Count > 100) {
		return fmt.Errorf("invalid count: %d (must be between 1 and 100)", s.Count)
	}
	return nil
}

// StateChangeEvent is streamed over SSE to mobile clients.
type StateChangeEvent struct {
	AttendeeID string              `json:"attendee_id"`
	State      ActorLifecycleState `json:"state"`
	Label      string              `json:"label"`
	Subtext    string              `json:"subtext"`
	Timestamp  int64               `json:"timestamp"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./demos/conference/pkg/types/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/pkg/types
git commit -m "feat(conference): add actor details, simulation request, and state change event types"
```

---

### Task 2: Real-Time SSE State Broker (`/api/events`) in `conf-gateway`

**Files:**
- Create: `demos/conference/cmd/conf-gateway/internal/gateway/sse.go`
- Modify: `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`
- Test: `demos/conference/cmd/conf-gateway/internal/gateway/gateway_test.go`

- [ ] **Step 1: Write the failing test**

In `gateway_test.go`:
```go
func TestSSEEventStreaming(t *testing.T) {
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?attendee_id=attendee-01", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to connect to SSE stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %s", resp.Header.Get("Content-Type"))
	}

	// Broadcast an event and assert client receives it
	gw.BroadcastStateChange("attendee-01", types.StateRunning, "Running in Sandbox", "gVisor execution")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-gateway/internal/gateway -run TestSSEEventStreaming`
Expected: FAIL with undefined `BroadcastStateChange` or 404 for `/api/events`.

- [ ] **Step 3: Implement SSE Broker & Handler**

Create `demos/conference/cmd/conf-gateway/internal/gateway/sse.go`:
```go
package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type EventBroker struct {
	mu          sync.RWMutex
	subscribers map[string][]chan types.StateChangeEvent
}

func NewEventBroker() *EventBroker {
	return &EventBroker{
		subscribers: make(map[string][]chan types.StateChangeEvent),
	}
}

func (b *EventBroker) Subscribe(attendeeID string) chan types.StateChangeEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan types.StateChangeEvent, 16)
	b.subscribers[attendeeID] = append(b.subscribers[attendeeID], ch)
	return ch
}

func (b *EventBroker) Unsubscribe(attendeeID string, ch chan types.StateChangeEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subscribers[attendeeID]
	for i, sub := range subs {
		if sub == ch {
			b.subscribers[attendeeID] = append(subs[:i], subs[i+1:]...)
			close(ch)
			break
		}
	}
	if len(b.subscribers[attendeeID]) == 0 {
		delete(b.subscribers, attendeeID)
	}
}

func (b *EventBroker) Publish(event types.StateChangeEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers[event.AttendeeID] {
		select {
		case ch <- event:
		default:
		}
	}
}
```

In `gateway.go`:
Register `g.mux.HandleFunc("/api/events", g.handleEvents)` and expose `BroadcastStateChange`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -race ./demos/conference/cmd/conf-gateway/internal/gateway -run TestSSEEventStreaming`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/internal/gateway
git commit -m "feat(conference): add real-time SSE event broker and /api/events endpoint"
```

---

### Task 3: Actor Spotlight Inspector API (`GET /api/actor/{id}`)

**Files:**
- Modify: `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`
- Test: `demos/conference/cmd/conf-gateway/internal/gateway/gateway_test.go`

- [ ] **Step 1: Write the failing test**

In `gateway_test.go`:
```go
func TestActorInspectorEndpoint(t *testing.T) {
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Initial actor inspection
	resp, err := http.Get(ts.URL + "/api/actor/attendee-05")
	if err != nil {
		t.Fatalf("failed to get actor details: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var details types.ActorDetails
	if err := json.NewDecoder(resp.Body).Decode(&details); err != nil {
		t.Fatalf("failed to decode details: %v", err)
	}
	if details.ActorID != "attendee-05" || details.State != types.StateSuspended {
		t.Fatalf("unexpected details: %+v", details)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-gateway/internal/gateway -run TestActorInspectorEndpoint`
Expected: FAIL with 404 for `/api/actor/attendee-05`.

- [ ] **Step 3: Implement minimal code**

In `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`:
- Register `g.mux.HandleFunc("/api/actor/", g.handleActorDetails)`
- Implement `handleActorDetails`: parses `attendeeID = strings.TrimPrefix(r.URL.Path, "/api/actor/")` and returns `types.ActorDetails` with current state, last worker pod, snapshot path, chat history, pending approval, and last result.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -race ./demos/conference/cmd/conf-gateway/internal/gateway -run TestActorInspectorEndpoint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/internal/gateway
git commit -m "feat(conference): add /api/actor/{id} inspector endpoint for spotlight drawer"
```

---

### Task 4: 1-Click Cluster Reset Controller (`POST /api/reset`)

**Files:**
- Modify: `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`
- Test: `demos/conference/cmd/conf-gateway/internal/gateway/gateway_test.go`

- [ ] **Step 1: Write the failing test**

In `gateway_test.go`:
```go
func TestResetEndpoint(t *testing.T) {
	suspender := &mockSuspender{}
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040", gateway.WithSuspender(suspender))
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Set some running actors
	gw.SetActorState("attendee-01", types.StateRunning)
	gw.SetActorState("attendee-02", types.StateAwaitingApproval)

	resp, err := http.Post(ts.URL+"/api/reset", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to call reset: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify all actors are StateSuspended and suspender was called for running actors
	st1, _ := gw.GetActorState("attendee-01")
	st2, _ := gw.GetActorState("attendee-02")
	if st1 != types.StateSuspended || st2 != types.StateSuspended {
		t.Errorf("expected all actors StateSuspended, got %s and %s", st1, st2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-gateway/internal/gateway -run TestResetEndpoint`
Expected: FAIL with 404 for `/api/reset`.

- [ ] **Step 3: Implement minimal code**

In `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`:
- Register `g.mux.HandleFunc("/api/reset", g.handleReset)`
- Implement `handleReset`:
  1. If `g.simRunner != nil`: cancels active simulation.
  2. Wipes `pendingApprovals` and `chatHistories`.
  3. Identifies active actors (`StateRunning` / `StateAwaitingApproval`) and calls `g.suspender.SuspendActor` in parallel goroutines.
  4. Resets all actors to `types.StateSuspended`.
  5. Appends log event `"Cluster reset: all actors suspended to GCS, worker pods freed"`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -race ./demos/conference/cmd/conf-gateway/internal/gateway -run TestResetEndpoint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/internal/gateway
git commit -m "feat(conference): implement /api/reset endpoint and cluster suspend sweep"
```

---

### Task 5: 1-Click Audience Simulation Engine (`POST /api/simulation`)

**Files:**
- Create: `demos/conference/cmd/conf-gateway/internal/gateway/simulation.go`
- Modify: `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`
- Test: `demos/conference/cmd/conf-gateway/internal/gateway/gateway_test.go`

- [ ] **Step 1: Write the failing test**

In `gateway_test.go`:
```go
func TestSimulationLifecycle(t *testing.T) {
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Start simulation with 5 virtual attendees
	reqBody := `{"action":"start","count":5}`
	resp, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to start simulation: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify simulation is active
	time.Sleep(100 * time.Millisecond)

	// Stop simulation
	stopBody := `{"action":"stop"}`
	respStop, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(stopBody))
	if err != nil || respStop.StatusCode != http.StatusOK {
		t.Fatalf("failed to stop simulation: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-gateway/internal/gateway -run TestSimulationLifecycle`
Expected: FAIL with 404 for `/api/simulation`.

- [ ] **Step 3: Implement minimal code**

Create `demos/conference/cmd/conf-gateway/internal/gateway/simulation.go`:
- `SimulationRunner` manages a cancelable `context.Context` and running virtual attendee goroutines.
- Virtual attendees execute: prompt generation $\to$ reading delay $\to$ approval surge.
- Register `g.mux.HandleFunc("/api/simulation", g.handleSimulation)` in `gateway.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -race ./demos/conference/cmd/conf-gateway/internal/gateway -run TestSimulationLifecycle`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/internal/gateway
git commit -m "feat(conference): implement audience simulation engine with /api/simulation endpoint"
```

---

### Task 6: Frontend Stage Dashboard & Mobile Chat UI Upgrades

**Files:**
- Modify: `demos/conference/cmd/conf-gateway/static/stage.html`
- Modify: `demos/conference/cmd/conf-gateway/static/stage.js`
- Modify: `demos/conference/cmd/conf-gateway/static/stage.css`
- Modify: `demos/conference/cmd/conf-gateway/static/chat.js`

- [ ] **Step 1: Update `stage.html`**
  - Add Simulation Button Group in header: `[▶ Simulate 25]`, `[▶ Simulate 50]`, `[■ Stop]`.
  - Add `[⟲ Reset All]` button in header.
  - Add slide-out `#spotlight-drawer` container with Close button (`✕`), overview card, prompt/thought preview, code block, output console, and `[Open Phone View]` button.

- [ ] **Step 2: Update `stage.js`**
  - Attach click handlers to fleet badges (`#badge-attendee-XX`) calling `openSpotlightDrawer(attendeeID)`.
  - Fetch `GET /api/actor/{id}` and populate drawer with live code and GCS snapshot details.
  - Bind simulation buttons to `POST /api/simulation`.
  - Bind reset button to `POST /api/reset`.

- [ ] **Step 3: Update `stage.css`**
  - Style `#spotlight-drawer` as a fixed slide-out panel (`transform: translateX(100%)` $\to$ `translateX(0)`).
  - Style simulation control buttons with glowing states when active.

- [ ] **Step 4: Update `chat.js` to use SSE**
  - Connect to `new EventSource('/api/events?attendee_id=' + state.attendeeId)`.
  - Remove fake `state.parkedTimer` `setTimeout`.
  - Update `#status-pill` directly from received server events (`THINKING_VERTEX_AI`, `AWAITING_APPROVAL`, `PARKED_ENVOY`, `RUNNING_GVISOR`, `SUSPENDED`).

- [ ] **Step 5: Verify static assets and unit tests**

Run: `go test -v -race ./demos/conference/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add demos/conference/cmd/conf-gateway/static/
git commit -m "feat(conference): add stage simulation buttons, reset control, spotlight drawer, and mobile SSE sync"
```

---

### Task 7: Automated End-to-End Verification & Documentation Updates

**Files:**
- Modify: `hack/verify-demo-conference.sh`
- Modify: `demos/conference/README.md`

- [ ] **Step 1: Update `hack/verify-demo-conference.sh`**
  - Add Step 7 testing `/api/simulation` start and stop.
  - Add Step 8 testing `/api/actor/{id}` spotlight endpoint.
  - Add Step 9 testing `/api/reset`.

- [ ] **Step 2: Update `demos/conference/README.md`**
  - Document the new simulation buttons, Reset All button, Spotlight drawer, and real SSE state sync.
  - Update presentation playbook cues.

- [ ] **Step 3: Run repository linters**

Run: `./hack/verify/boilerplate.sh && ./hack/verify/gofmt.sh && ./hack/verify/go-modules.sh && ./hack/verify/licenses.sh && ./hack/verify/golangci-lint.sh`
Expected: Clean pass.

- [ ] **Step 4: Commit**

```bash
git add hack/verify-demo-conference.sh demos/conference/README.md
git commit -m "docs(conference): update verification script and documentation with simulation, spotlight, and reset features"
```

# Conference Live Actor Action Stream Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a real-time Live Actor Action Stream for the conference demo (`demos/conference`), streaming every user prompt, Gemini reasoning, generated Python code, human approval decision, and gVisor terminal output across all 50–100 actors, accessible via a dedicated `/feed` page and an integrated view switcher on `/stage`.

**Architecture:** `conf-gateway` maintains an in-memory ring buffer of the last 100 action events and broadcasts new action cards over an SSE stream (`GET /api/stream`) and history endpoint (`GET /api/feed/history`). The frontend provides a full-page cyber-themed dashboard at `/feed` and an instant in-place toggle (`[⊞ Grid Matrix | ≡ Live Stream]`) on `/stage`.

**Tech Stack:** Go 1.24, Server-Sent Events (SSE), HTML5/CSS3/Vanilla JS, Google GenAI Go SDK v2.

---

## File Structure

```
demos/conference/
├── pkg/
│   └── types/
│       ├── types.go                # Add ActionLogEntry struct
│       └── types_test.go           # Round-trip JSON tests for ActionLogEntry
├── cmd/
│   └── conf-gateway/
│       ├── internal/
│       │   └── gateway/
│       │       ├── gateway.go      # Stream endpoints, action buffer, and event triggers
│       │       ├── sse.go          # ActionLogEntry broadcast support
│       │       └── gateway_test.go # Tests for /api/stream and /api/feed/history
│       └── static/
│           ├── feed.html           # Dedicated full-page live action stream
│           ├── feed.js             # SSE listener, filter, search, auto-scroll
│           ├── feed.css            # Cyberpunk feed styling and card animations
│           ├── stage.html          # View switcher toggle & embedded stream container
│           ├── stage.js            # Switcher logic between grid and stream views
│           └── stage.css           # Switcher styling and stream layout on stage
hack/
├── verify-demo-conference.sh       # Automated verification with /api/stream checks
└── install-demo-conference.sh      # Deployment helper
```

---

### Task 1: Domain Type `ActionLogEntry`

**Files:**
- Modify: `demos/conference/pkg/types/types.go`
- Test: `demos/conference/pkg/types/types_test.go`

- [ ] **Step 1: Write failing test in `types_test.go`**

```go
func TestActionLogEntrySerialization(t *testing.T) {
	entry := types.ActionLogEntry{
		ID:            "act-12345",
		AttendeeID:    "attendee-07",
		Timestamp:     1790040000,
		Prompt:        "Calculate 2^32 - 1 and its prime factors",
		ModelResponse: "I analyzed your request...",
		Code:          "def prime_factors(n): ...",
		Language:      "python",
		RiskLevel:     types.RiskHigh,
		ApprovalID:    "app-999",
		Decision:      types.DecisionApprove,
		Execution: &types.ExecutionResult{
			Stdout:     "Decimal: 4294967295\nPrime Factors: 3, 5, 17, 257, 65537",
			ExitCode:   0,
			DurationMs: 24,
		},
		WorkerPod: "conf-workers-0",
		State:     types.StateRunningGVisor,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("failed to marshal ActionLogEntry: %v", err)
	}

	var decoded types.ActionLogEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal ActionLogEntry: %v", err)
	}

	if decoded.ID != "act-12345" || decoded.AttendeeID != "attendee-07" || decoded.Execution == nil {
		t.Fatalf("unexpected decoded ActionLogEntry: %+v", decoded)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/pkg/types -run TestActionLogEntrySerialization`
Expected: FAIL with undefined `ActionLogEntry`.

- [ ] **Step 3: Implement `ActionLogEntry` in `types.go`**

```go
// ActionLogEntry represents a full conversational or execution event streamed across the fleet.
type ActionLogEntry struct {
	ID            string              `json:"id"`
	AttendeeID    string              `json:"attendee_id"`
	Timestamp     int64               `json:"timestamp"`
	Prompt        string              `json:"prompt"`
	ModelResponse string              `json:"model_response,omitempty"`
	Code          string              `json:"code,omitempty"`
	Language      string              `json:"language,omitempty"`
	RiskLevel     RiskLevel           `json:"risk_level,omitempty"`
	ApprovalID    string              `json:"approval_id,omitempty"`
	Decision      DecisionType        `json:"decision,omitempty"`
	Execution     *ExecutionResult    `json:"execution,omitempty"`
	WorkerPod     string              `json:"worker_pod,omitempty"`
	State         ActorLifecycleState `json:"state"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./demos/conference/pkg/types -run TestActionLogEntrySerialization`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add demos/conference/pkg/types
git commit -m "feat(conference): define ActionLogEntry schema for real-time action streaming"
```

---

### Task 2: Backend Streaming Broker & Feed Endpoints in `conf-gateway`

**Files:**
- Modify: `demos/conference/cmd/conf-gateway/internal/gateway/sse.go`
- Modify: `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`
- Test: `demos/conference/cmd/conf-gateway/internal/gateway/gateway_test.go`

- [ ] **Step 1: Write failing tests in `gateway_test.go`**

Test `GET /api/feed/history`:
- Returns 200 OK with empty array or existing `ActionLogEntry` items.
Test `GET /api/stream`:
- Returns 200 OK with `Content-Type: text/event-stream`.
- Receives `event: action_entry` when a prompt or approval is processed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -v ./demos/conference/cmd/conf-gateway/internal/gateway -run TestActionStreamEndpoints`
Expected: FAIL with 404 on `/api/stream` and `/api/feed/history`.

- [ ] **Step 3: Implement action buffer and broadcast in `gateway.go` and `sse.go`**

1. In `sse.go`:
   - Add action stream subscriber channel support to `EventBroker`:
     `actionSubscribers []chan types.ActionLogEntry`
     `SubscribeActions() chan types.ActionLogEntry`
     `UnsubscribeActions(ch chan types.ActionLogEntry)`
     `PublishAction(entry types.ActionLogEntry)`
2. In `gateway.go`:
   - Add `actionFeed []types.ActionLogEntry` (bounded to 100 entries) in `Gateway`.
   - Register `g.mux.HandleFunc("/api/feed/history", g.handleFeedHistory)`.
   - Register `g.mux.HandleFunc("/api/stream", g.handleActionStream)`.
   - In `handleChatRequest`: emit `ActionLogEntry` when prompt arrives and when approval is generated.
   - In `handleApproveRequest`: emit `ActionLogEntry` when approved and when gVisor execution completes with output.
   - In `handleReset`: clear `actionFeed` and broadcast reset event over stream.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v -race ./demos/conference/cmd/conf-gateway/internal/gateway -run TestActionStreamEndpoints`
Run: `go test -v -race ./demos/conference/...`

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/internal/gateway
git commit -m "feat(conference): implement /api/stream SSE endpoint and /api/feed/history buffer"
```

---

### Task 3: Dedicated Full-Screen Live Stream Page (`/feed`)

**Files:**
- Create: `demos/conference/cmd/conf-gateway/static/feed.html`
- Create: `demos/conference/cmd/conf-gateway/static/feed.js`
- Create: `demos/conference/cmd/conf-gateway/static/feed.css`
- Modify: `demos/conference/cmd/conf-gateway/internal/gateway/gateway.go`
- Test: `demos/conference/cmd/conf-gateway/internal/gateway/gateway_test.go`

- [ ] **Step 1: Write failing test in `gateway_test.go`**

Verify `GET /feed` returns 200 OK with `feed.html` containing `#action-stream`, filter buttons, and search input.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./demos/conference/cmd/conf-gateway/internal/gateway -run TestFeedRoute`
Expected: FAIL with 404 for `/feed`.

- [ ] **Step 3: Implement `feed.html`, `feed.js`, and `feed.css`**

1. `feed.html`:
   - Top banner with title, Envoy parked lot meter, and `[⟲ Reset All]` button.
   - Filter bar: filter pills (`All`, `Awaiting Approval`, `Executing`, `Completed`), search box, auto-scroll toggle.
   - Stream container: `#action-stream`.
2. `feed.js`:
   - Fetches `GET /api/feed/history` on load to render previous cards.
   - Connects to `new EventSource('/api/stream')`.
   - Appends/prepends cards with smooth CSS fade-in.
   - Filters cards in real time by state or search text.
   - Clicking code copies snippet; clicking attendee opens `/chat?attendee=...`.
3. `feed.css`:
   - Cyberpunk terminal theme matching `stage.css`.
   - Card layout with prompt, code, gVisor output, and GCS snapshot pill.
4. In `gateway.go`:
   - Register route `g.mux.HandleFunc("/feed", g.handleFeed)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v -race ./demos/conference/cmd/conf-gateway/internal/gateway -run TestFeedRoute`
Run: `go test -v -race ./demos/conference/...`

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway
git commit -m "feat(conference): add dedicated full-screen /feed streaming dashboard"
```

---

### Task 4: Integrated Stage View Switcher on `/stage`

**Files:**
- Modify: `demos/conference/cmd/conf-gateway/static/stage.html`
- Modify: `demos/conference/cmd/conf-gateway/static/stage.js`
- Modify: `demos/conference/cmd/conf-gateway/static/stage.css`

- [ ] **Step 1: Update `stage.html`**
  - Add view switcher buttons in header:
    `<button id="view-mode-grid" class="btn-view active">⊞ Grid Matrix</button>`
    `<button id="view-mode-stream" class="btn-view">≡ Live Stream</button>`
  - Add container `#stage-stream-view` (hidden by default) alongside `#stage-grid-view`.

- [ ] **Step 2: Update `stage.js`**
  - Bind click listeners to `#view-mode-grid` and `#view-mode-stream`.
  - When switching to stream:
    - Hide grid view, show stream view.
    - Connect to `/api/stream` (or render shared feed cards).
  - Clicking any card in the stream view opens the **Spotlight Drawer**!

- [ ] **Step 3: Update `stage.css`**
  - Style `.btn-view` segmented pill switcher with active states.
  - Style `#stage-stream-view` to integrate cleanly into the stage main content area.

- [ ] **Step 4: Run unit tests and static linters**

Run: `go test -v -race ./demos/conference/...`
Run: `./hack/verify/gofmt.sh && ./hack/verify/boilerplate.sh`

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/static/
git commit -m "feat(conference): add integrated view switcher on stage between grid matrix and live stream"
```

---

### Task 5: Automated Verification & Documentation Updates

**Files:**
- Modify: `hack/verify-demo-conference.sh`
- Modify: `demos/conference/README.md`

- [x] **Step 1: Update `hack/verify-demo-conference.sh`**
  - Add Step 10: Verify `GET /feed` serves 200 OK.
  - Add Step 11: Verify `GET /api/feed/history` returns valid JSON history array.
  - Add Step 12: Verify `GET /api/stream` establishes SSE connection.
  - Update banner to "ALL 12 VERIFICATION STEPS PASSED SUCCESSFULLY! (exit 0)".

- [x] **Step 2: Update `demos/conference/README.md`**
  - Document `/feed` and the stage view switcher.
  - Update speaker playbook with live stream presentation cues.

- [x] **Step 3: Run repository linters**

Run: `./hack/verify/boilerplate.sh && ./hack/verify/gofmt.sh && ./hack/verify/go-modules.sh && ./hack/verify/licenses.sh && ./hack/verify/golangci-lint.sh`
Expected: Clean pass.

- [ ] **Step 4: Commit**

```bash
git add hack/verify-demo-conference.sh demos/conference/README.md
git commit -m "docs(conference): update verification script and documentation with live action stream features"
```

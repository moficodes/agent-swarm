# Design: Conference Live Actor Action Stream (`demos/conference`)

## 1. Executive Summary & Purpose

The Interactive Conference Agent Swarm Demo (`demos/conference`) enables up to 100 attendees or virtual attendees to command personal sandboxed agents over Substrate's 2-worker pool.
While the presentation stage dashboard (`/stage`) provides high-level fleet matrix status and an individual spotlight inspector, presenters and conference audiences need a high-visibility, continuous live stream showing the actual conversation turns, Gemini reasoning, generated scripts, and sandbox execution outputs happening across all actors in real time.

This design specifies the **Live Actor Action Stream**:
1. **Real-Time SSE Action Event Stream (`GET /api/stream` & `GET /api/feed/history`):** A backend broker that captures every user prompt, Gemini response, generated code snippet, human approval, and gVisor terminal output, broadcasting them over Server-Sent Events to all connected screens.
2. **Dedicated Full-Screen Live Stream (`/feed`):** A high-density, auto-scrolling live console designed for secondary projection screens or monitoring displays, complete with instant filtering, search, and a 1-click `[⟲ Reset All]` control.
3. **Integrated Stage View Switcher on `/stage`:** A segmented toggle on the presentation header (`[⊞ Grid Matrix | ≡ Live Stream]`) allowing the speaker to flip the main stage area seamlessly between the 100-badge matrix and the live action stream without leaving the presentation dashboard.

---

## 2. Domain Model & Protocol Schemas

### 2.1 `types.ActionLogEntry`
Defined in `demos/conference/pkg/types/types.go`:
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

---

## 3. Backend Streaming Infrastructure (`conf-gateway`)

### 3.1 Historical Feed Endpoint (`GET /api/feed/history`)
* **Endpoint:** `GET /api/feed/history`
* **Response:** JSON array of the most recent 50 `types.ActionLogEntry` records.
* **Purpose:** Ensures any freshly opened browser tab or refreshed screen immediately renders previous conversation turns and executions before listening for new SSE events.

### 3.2 Real-Time Action Stream (`GET /api/stream`)
* **Endpoint:** `GET /api/stream`
* **Content-Type:** `text/event-stream`
* **Broadcasting Trigger Points:**
  1. **Prompt Received (`THINKING_VERTEX_AI`):** Emits an `ActionLogEntry` with prompt text, attendee ID, and timestamp.
  2. **Approval Card Yielded (`AWAITING_APPROVAL`):** Updates the entry with the generated Python/Bash code snippet, model summary, approval ID, and risk level.
  3. **Code Executed (`RUNNING_GVISOR` $\to$ `SUSPENDED`):** Updates the entry with the real stdout, stderr, process exit code, duration (ms), and worker pod binding (`conf-workers-0` or `conf-workers-1`).
  4. **Cluster Reset (`event: reset`):** Notifies clients to clear in-memory feed cards or display reset notice.

---

## 4. User Interfaces

### 4.1 Dedicated Full-Screen Live Stream (`/feed`)
* **Route:** `GET /feed` served by `conf-gateway`.
* **Header Controls:**
  * Substrate branding & live Envoy parking lot meter.
  * Filter pills: `[All]`, `[Awaiting Approval]`, `[Running in Sandbox]`, `[Completed]`.
  * Real-time search input: filter cards live by attendee ID (`attendee-14`) or text keywords.
  * Auto-scroll toggle switch (`[✓ Auto-scroll to top]`).
  * 1-Click `[⟲ Reset All]` button.
* **Action Stream Container (`#action-stream`):**
  * Auto-scrolling card stream with smooth CSS fade-in animations.
  * DOM buffer capped at 100 cards to guarantee 60fps performance and zero memory leaks.
  * Interactive features:
    * 1-click copy for Python code blocks.
    * 1-click popup opening the attendee's live mobile phone view (`/chat?attendee=attendee-XX`).

### 4.2 Integrated Stage View Switcher (`/stage`)
* **Header Switcher:** In the presentation header, adds:
  ```html
  <div class="view-switcher">
    <button id="view-mode-grid" class="btn-view active">⊞ Grid Matrix</button>
    <button id="view-mode-stream" class="btn-view">≡ Live Stream</button>
  </div>
  ```
* **Seamless In-Place Toggle:**
  * `[⊞ Grid Matrix]`: Displays the 100-badge Fleet Matrix.
  * `[≡ Live Stream]`: Swaps the main container to display the live streaming action feed.
  * Preserves top hardware cards (Worker Pod-0, Worker Pod-1, Envoy Parking Meter, Simulation buttons, Reset button) in both views.
  * Clicking an attendee badge or action card opens the **Actor Spotlight Drawer** in either view.

---

## 5. Testing & Verification Plan

1. **Unit Tests (`gateway_test.go`):**
   * Test `GET /api/feed/history` returns valid JSON history array.
   * Test `GET /api/stream` establishes SSE connection with `text/event-stream` and receives `event: action_entry` upon prompt and approval execution.
   * Test `/feed` serves HTTP 200 with required stream DOM elements.
2. **End-to-End Live Verification:**
   * Open `/feed` and `/stage`.
   * Start a 25-attendee simulation (`[▶ Simulate 25]`).
   * Verify action cards stream live with real prompt text, Gemini Python code, and gVisor sandbox stdout.
   * Toggle between Grid Matrix and Live Stream on `/stage`.
   * Verify `[⟲ Reset All]` clears the stream cleanly.

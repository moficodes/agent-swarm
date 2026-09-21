# Design: Interactive Conference Demo Enhancements (`demos/conference`)

## 1. Executive Summary & Motivation

The Interactive Conference Agent Swarm Demo (`demos/conference`) successfully demonstrates 50x oversubscription, zero-cost idle suspension, and mass L7 request parking on GKE using Google GenAI SDK v2 on Vertex AI via Workload Identity.

To maximize presentation impact, audience engagement, and operator ergonomics during live conference keynotes and rehearsals, this design specifies four major enhancements:
1. **1-Click Stage Simulation Controller (`POST /api/simulation`):** Allows a presenter to simulate 25 to 50 active virtual attendees directly from the presentation dashboard with a single click, demonstrating extreme oversubscription and request parking without requiring dozens of live humans in the auditorium.
2. **Actor Spotlight & Inspection Drawer (`GET /api/actor/{id}`):** Clicking any badge in the 50–100 fleet grid on `/stage` slides out a live inspector displaying the attendee's prompt, the exact Python script Gemini generated, real execution stdout/stderr, GCS snapshot metrics, and a direct link to open their mobile phone view.
3. **Real-Time Server-Sent Events (SSE) State Synchronization (`GET /api/events`):** Eliminates inaccurate client-side `setTimeout` guesses in `chat.js`. The gateway pushes authentic server state transitions (`THINKING_VERTEX_AI`, `AWAITING_APPROVAL`, `PARKED_ENVOY`, `RUNNING_GVISOR`, `SUSPENDED`) directly to attendee smartphones.
4. **1-Click Cluster Reset Controller (`POST /api/reset`):** A stage button that halts active simulations, clears in-memory state and pending approvals, and suspends any active worker pods on the GKE cluster, restoring a clean state in under one second between demo runs.

---

## 2. Component Specifications

### 2.1 Component 1: 1-Click Audience Simulation Engine

#### User Experience
On the top navigation bar of the presentation stage dashboard (`/stage`):
* Buttons:
  * `[▶ Simulate 25]`
  * `[▶ Simulate 50]`
  * `[■ Stop Simulation]` (visible while simulation is active, with animated indicator)

#### Backend Architecture (`conf-gateway`)
* **Endpoint:** `POST /api/simulation`
  ```json
  {
    "action": "start",
    "count": 25
  }
  ```
  or
  ```json
  {
    "action": "stop"
  }
  ```
* **Virtual Attendee Routine:**
  Managed via a cancelable `context.Context`:
  1. Each virtual attendee runs in an independent goroutine with staggered start offsets (0 to 3 seconds).
  2. Calls `/api/session` to obtain an assigned ID (`attendee-01` .. `attendee-50`) and JWT.
  3. Selects a varied prompt from a curated pool:
     * *"Calculate prime numbers between 1 and 50 and format as a table"*
     * *"Plot a 1D random walk in ASCII text"*
     * *"Calculate the first 15 Fibonacci numbers"*
     * *"Calculate 2^32 - 1 and its prime factors"*
     * *"Plot a sine wave in ASCII text"*
  4. Posts prompt to `/api/chat` $\to$ Vertex AI generates code and returns `ApprovalRequest`.
  5. Waits a randomized human reading delay (3 to 6 seconds).
  6. Sends `POST /api/approve` $\to$ hits `atenet-router`. Because 25–50 attendees approve in waves, **Envoy's L7 Request Parking buffer immediately engages** on the stage screen!
  7. Receives the gVisor sandbox execution output, logs completion, and sleeps.

---

### 2.2 Component 2: Actor Spotlight & Inspection Drawer

#### User Experience
* On `/stage`, every badge in the 50–100 fleet grid (`#badge-attendee-01` .. `#badge-attendee-100`) is interactive (`cursor: pointer`, hover glow).
* Clicking a badge slides out the **Actor Spotlight Drawer** from the right edge of the screen:
  * **Header:** Attendee ID (`Attendee #14`), current state badge (`AWAITING APPROVAL`), and close button (`✕`).
  * **Overview Card:**
    * Worker Pod: `conf-workers-0` (or `idle`)
    * GCS Snapshot: `gs://snapshot-substrate.../` (~375 KB)
    * Cluster Footprint: `0 vCPU / 0 MB RAM` while suspended
  * **Prompt & Reasoning:**
    * Attendee prompt text
    * LLM summary
  * **Code Preview:** Syntax-highlighted code block of the generated script.
  * **Terminal Output:** Real-time stdout and stderr from the gVisor execution.
  * **Action Button:** `[↗ Open Attendee Phone View]` opening `/chat?attendee=attendee-XX` in a popup window.

#### Backend API
* **Endpoint:** `GET /api/actor/{id}`
  * Authentication: Open for stage dashboard (or validated via bearer token).
  * Response:
    ```json
    {
      "actor_id": "attendee-14",
      "state": "AWAITING_APPROVAL",
      "worker_pod": "conf-workers-0",
      "snapshot_location": "gs://snapshot-substrate-test-mofilabs-uswest1/ate-demo-conf/...",
      "snapshot_size_bytes": 384000,
      "chat_history": [
        {"sender": "user", "content": "Calculate primes"},
        {"sender": "assistant", "content": "I prepared the script..."}
      ],
      "pending_approval": {
        "approval_id": "app-12345",
        "summary": "Calculate primes between 1 and 50",
        "code": "def is_prime(n): ...",
        "language": "python",
        "risk_level": "HIGH"
      },
      "last_result": {
        "exit_code": 0,
        "stdout": "Primes: 2, 3, 5, 7 ...",
        "stderr": "",
        "duration_ms": 25
      }
    }
    ```

---

### 2.3 Component 3: Real-Time SSE State Synchronization

#### The Problem
In the previous implementation of `chat.js`, a client-side timer:
`setTimeout(function() { if (state.inFlight) setStatus('parked'); }, 1000);`
artificially forced the UI status pill to `"L7 parked"` after 1 second, causing a visual conflict where the stage dashboard showed `RUNNING` or `THINKING` while the phone showed `L7 parked`.

#### The Solution: Server-Sent Events (`GET /api/events`)
* Remove the fake client timer in `chat.js`.
* **Endpoint:** `GET /api/events?attendee_id=attendee-XX`
  * Format: `text/event-stream`
  * Connection opened on page load via `new EventSource('/api/events?attendee_id=' + state.attendeeId)`.
* **Event Structure:**
  ```json
  event: state_change
  data: {
    "attendee_id": "attendee-14",
    "state": "THINKING_VERTEX_AI",
    "label": "🧠 Reasoning on Vertex AI...",
    "subtext": "Gemini 2.5 Flash generating analysis script"
  }
  ```
* **Supported Authentic Lifecycle States:**
  1. `THINKING_VERTEX_AI`: Generated while Gemini is actively reasoning on Google Cloud.
  2. `AWAITING_APPROVAL`: Generated when the approval card is yielded and the actor container is frozen to GCS at 0 compute cost.
  3. `PARKED_ENVOY`: Generated when the request enters Envoy's `ext_proc` queue awaiting a worker slot.
  4. `RUNNING_GVISOR`: Generated when the actor is restored onto a physical worker pod and executing the script.
  5. `SUSPENDED`: Generated when execution completes and memory pages are snapshotted to GCS.

---

### 2.4 Component 4: 1-Click Cluster Reset Controller

#### User Experience
* A prominent **`[⟲ Reset All]`** button on the stage dashboard header bar.
* Displays feedback: `Resetting cluster...` $\to$ `Clean slate restored`.

#### Backend Implementation (`POST /api/reset`)
1. Cancels any active audience simulation goroutines.
2. Clears `conf-gateway` in-memory state:
   * Wipes `pendingApprovals`
   * Wipes `chatHistories`
   * Sets all 50–100 badges in `activeActors` back to `SUSPENDED`.
3. Cluster Sweep:
   * Finds any actor currently marked `RUNNING` or `AWAITING_APPROVAL`.
   * Dispatches parallel `kubectl-ate suspend actor <id> -a ate-demo-conf` calls to ensure both worker pods return to 100% idle.
4. Broadcasts a `reset` event over SSE so any open mobile phone views clear their spinners and reset.

---

## 3. Data Flow & Security Architecture

```
                                 [Presentation Screen (/stage)]
                                   │                   ▲
                     POST /api/simulation              │ SSE / Telemetry
                     POST /api/reset                   │ GET /api/actor/{id}
                                   ▼                   │
+─────────────────────────────────── conf-gateway ───────────────────────────────────+
│                                                                                    │
│  * Vertex AI Agent Loop (google.golang.org/genai via GKE Workload Identity)        │
│  * Simulation Runner (25–50 Virtual Attendees with human pauses & approval bursts) │
│  * State Event Broker (SSE stream to mobile clients & stage)                       │
│  * Reset Controller (Gateway wipe + parallel actor suspend sweep)                  │
│                                                                                    │
+─────────────────────────────────────┬──────────────────────────────────────────────+
                                      │ Proxy POST /api/approve
                                      │ ate-target-actor: ate-demo-conf/attendee-XX
                                      ▼
+─────────────────────────────── atenet-router ──────────────────────────────────────+
│                                                                                    │
│  * L7 Request Parking Buffer (--parked-request-budget=60s)                         │
│  * Holds approval bursts in memory without 503s                                    │
│                                                                                    │
+─────────────────────────────────────┬──────────────────────────────────────────────+
                                      │ Dispatches to available worker slot
                                      ▼
+─────────────────────────────── GKE WorkerPool ─────────────────────────────────────+
│                                                                                    │
│  conf-workers (2 Replicas: pod-0, pod-1)                                           │
│  * Executes Python 3.12 scripts inside gVisor sandbox boundary (5s timeout)        │
│  * Full memory pages snapshotted to GCS (<300ms restore, 0 MB RAM idle)            │
│                                                                                    │
+────────────────────────────────────────────────────────────────────────────────────+
```

---

## 4. Testing & Verification Plan

1. **Unit Tests (`gateway_test.go`):**
   * Test simulation start/stop lifecycle.
   * Test `/api/actor/{id}` inspector endpoint for suspended, awaiting, and running states.
   * Test SSE event streaming (`/api/events`) and client broadcast.
   * Test `/api/reset` clears approvals, histories, and calls actor suspenders.
2. **End-to-End Live Verification:**
   * Trigger `[▶ Simulate 25]` from `/stage` and verify:
     - Envoy L7 parking meter spikes on the stage screen.
     - Clicking attendee badges opens the Spotlight drawer showing live generated code.
     - Mobile phone view receives real-time SSE state updates without fake timers.
     - Clicking `[⟲ Reset All]` suspends all actors and frees both worker pods.

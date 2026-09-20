# Design: Interactive Conference Agent Swarm Demo (`demos/conference`)

## 1. Executive Summary

This design specifies an interactive, conference-ready demonstration of **Agent Substrate**.
In a live presentation setting, 50 to 100+ audience members scan a QR code from the stage screen using their smartphones. With zero login friction, each attendee is provisioned an isolated, stateful AI agent running in a gVisor sandbox on the Kubernetes cluster.

Each agent is built using the official **Google ADK / GenAI Go SDK v2** (`google.golang.org/genai`), powered by modern **Gemini 3.5+ Flash and Flash-Lite** models. The agent assists the attendee with data research and code execution, declaring tools for sandboxed code execution, data retrieval, and explicit **Human-in-the-Loop (HITL)** approval.

The demo provides an authentic live showcase of Substrate's defining superpowers:
1. **Extreme Oversubscription (25x–50x Density):** 50–100 active attendee agents multiplex onto a WorkerPool of **only 2 physical worker pods**, cutting idle compute footprint by 96–98%.
2. **Zero-Cost Idle Suspension:** When an agent pauses to ask the attendee for approval to run a script, Substrate immediately snapshots its RAM and disk state to Google Cloud Storage (GCS) and frees the worker pod. The attendee can spend minutes reading the proposal while the actor consumes **0 vCPU and 0 MB RAM**.
3. **Mass L7 Request Parking in Envoy:** When the speaker directs the audience to *"Inspect and hit Approve"*, 50+ attendees submit approvals simultaneously. Envoy's `ext_proc` filter parks 48+ requests in memory with `--parked-request-budget=60s`, draining them through the 2 worker pods without a single HTTP 503 error or dropped packet.
4. **Dual Interface:** A main stage presentation dashboard displaying live cluster telemetry, worker pod occupancy, and the live Envoy parking lot meter alongside a responsive mobile web chat for attendees.

---

## 2. Success Criteria & Key Metrics

| Metric | Target | Verification Method |
|---|---|---|
| **Actor Density** | 50–100 actors on 2 worker pods (25x–50x oversubscription) | `kubectl get actors -n ate-demo-conf` vs `kubectl get pods -n ate-demo-conf` |
| **Wake-from-Sleep Latency** | < 300 ms restore from GCS snapshot | Measured via `atenet-router` turn latency and UI telemetry |
| **Zero Idle Cost** | 0 vCPU, 0 RAM consumed while awaiting human approval | Actor state verified as `ACTOR_STATE_SUSPENDED` during review |
| **L7 Request Parking** | Surge of 50+ concurrent requests results in 0 dropped requests and 0 HTTP 503 errors | `atenet.router.parking.active > 0` captured from `/statusz` JSON |
| **Sandbox Isolation** | Code execution contained inside gVisor (`runsc`) boundary with 5s timeout | Script execution trapped inside container `/tmp/workspace` |
| **Conference Resiliency** | 100% demo uptime under flaky Wi-Fi or Gemini 429 quota exhaustion | Automatic fallback to embedded deterministic mock reasoning engine |

---

## 3. System Architecture & Topology

```
+-------------------------------------------------------------------------+
|                          CONFERENCE AUDIENCE                            |
|                                                                         |
|  [Attendee Phone 1]   [Attendee Phone 2]  ...  [Attendee Phone 50+]     |
|          |                   |                         |                |
|          +-------------------+-------------------------+                |
|                              |                                          |
|                 HTTPS / WSS (Mobile Chat UI)                            |
+------------------------------|------------------------------------------+
                               v
+-------------------------------------------------------------------------+
|                  DEMO EDGE GATEWAY (conf-gateway)                       |
|                                                                         |
|  * Mints Ephemeral Attendee JWTs (sub: attendee-X)                      |
|  * Injects Gemini API Key from Secret into HTTP Headers                 |
|  * Proxies Stage Dashboard & Mobile Web Assets                          |
|  * Dispatches to atenet-router with ate-target-actor Header              |
+------------------------------|------------------------------------------+
                               v
+-------------------------------------------------------------------------+
|                   atenet-router (Envoy + ext_proc)                      |
|                                                                         |
|  * Target Routing: ate-target-actor: ate-demo-conf/attendee-X           |
|  * L7 Request Parking: --parked-request-budget=60s                      |
|  * Holds excess requests in memory buffer during audience flash mob     |
+------------------------------|------------------------------------------+
                               v
+-------------------------------------------------------------------------+
|                      GKE CLUSTER: ate-demo-conf                         |
|                                                                         |
|  +-------------------------------------------------------------------+  |
|  | WorkerPool: conf-workers (2 Pods: pod-0, pod-1)                   |  |
|  |                                                                   |  |
|  |  Pod-0: [ Active Actor: attendee-14 (gVisor runsc sandbox) ]      |  |
|  |  Pod-1: [ Active Actor: attendee-29 (gVisor runsc sandbox) ]      |  |
|  +-------------------------------------------------------------------+  |
|                                ^                                        |
|         Restore (<300ms) /     |     Suspend (RAM + FS)                 |
|         Snapshot Stream        |                                        |
|                                v                                        |
|  +-------------------------------------------------------------------+  |
|  | GCS Snapshot Bucket: gs://${BUCKET_NAME}/ate-demo-conf/           |  |
|  | 50-100 Sleeping Actors (checkpoint.img.zstd, pages.img.zstd)     |  |
|  | Compute footprint: 0 vCPU, 0 MB cluster RAM                      |  |
|  +-------------------------------------------------------------------+  |
+-------------------------------------------------------------------------+
```

### 3.1 Kubernetes Resources
* **Namespace / Atespace:** `ate-demo-conf`
* **WorkerPool:** `conf-workers`
  * Replicas: `2`
  * SandboxClass: `gvisor`
* **ActorTemplate:** `conf-agent`
  * Workload Image: Built with `ko` from `demos/conference/cmd/conf-workload`
  * Storage Bucket: `gs://${BUCKET_NAME}/ate-demo-conf/`
  * Golden Snapshot: Provisioned during installation with base Python/shell execution environment.
* **Actors:**
  * Named `attendee-01` through `attendee-50` (or dynamically created via `ateapi.CreateActor`).
  * Initial State: `ACTOR_STATE_SUSPENDED`.

---

## 4. Agent Workload Runtime (`conf-workload`)

Each actor runs a unified Go binary inside the gVisor sandbox utilizing the Google GenAI Go SDK v2.

### 4.1 SDK Configuration & Model Selection
* **Package:** `google.golang.org/genai`
* **Models Supported:**
  * Default: `gemini-2.5-flash` or `gemini-2.5-flash-lite` (and configurable for Gemini 3.5+ series).
* **Dynamic Credential Injection:**
  The actor reads the API key from incoming HTTP headers:
  ```go
  apiKey := r.Header.Get("X-Gemini-API-Key")
  client, err := genai.NewClient(ctx, &genai.ClientConfig{
      APIKey: apiKey,
  })
  ```
  This guarantees that actors do not store static secrets and can support multiple tenants with different credentials or rate limits.

### 4.2 Function Calling & Tool Declarations
The agent registers 3 primary tools with the Gemini model:
1. `execute_sandbox_code(language string, code string)`
   * Executes Python scripts (`python3 -c "..."`) or shell commands inside the container's isolated `/tmp/workspace` directory.
   * Enforces a hard 5-second execution timeout via `exec.CommandContext`.
   * Captures stdout, stderr, and execution exit codes, feeding them back into the Gemini tool-response turn.
2. `fetch_research_data(topic string)`
   * Fetches data points or simulated real-time metrics (e.g. system performance logs, transaction distributions, anomaly metrics) for the agent to analyze.
3. `request_human_approval(summary string, code string, risk_level string)`
   * Instructed via system prompts to be required before any script execution or external write operation.
   * Emits an approval payload to the mobile client and marks the agent turn as paused.

### 4.3 In-Memory State Continuity (No Database Required)
* The agent stores active conversation turns, message history, and ADK session structs directly in Go process memory (`*genai.ChatSession`).
* Because Substrate snapshots the process memory pages (`pages.img.zstd`), full multi-turn conversational context is preserved across suspensions and resumptions without querying external databases (Redis, Postgres, DynamoDB).

### 4.4 Offline Mock Engine (Conference Wi-Fi Defense)
To protect against conference Wi-Fi failures, DNS drops, or Google API rate limits:
* If the Gemini API returns HTTP 429 or network errors (or if `DEMO_MOCK_LLM=true`), the runtime immediately falls back to an internal deterministic mock engine.
* The mock engine simulates realistic Gemini reasoning, emits proper `request_human_approval` calls, and executes real Python code inside gVisor.

---

## 5. Human-In-The-Loop & Request Parking Mechanics

The core demonstration unfolds in two synchronized turns:

### Turn 1: Planning & Zero-Cost Suspension
1. **Attendee Input:** The attendee types or selects a challenge prompt on mobile (e.g., *"Analyze sales telemetry, detect outliers, and produce an ASCII histogram"*).
2. **Resume & Reasoning:**
   * `atenet-router` receives the request with header `ate-target-actor: ate-demo-conf/attendee-14`.
   * Substrate restores the container from GCS in <300ms onto a free worker pod.
   * Gemini reasons, calls `fetch_research_data`, and generates the Python analysis script.
   * Gemini calls `request_human_approval(summary="Execute Outlier Analysis", code="import sys...", risk="HIGH")`.
3. **Yielding Approval Card & Instant Suspend:**
   * The actor returns HTTP 200 with `status: WAITING_FOR_APPROVAL` containing the code snippet.
   * The edge gateway invokes `kubectl-ate suspend actor attendee-14 -a ate-demo-conf`.
   * Substrate freezes the process and streams its live RAM pages to GCS.
   * **Result:** While the human reads the code on mobile for 30–60 seconds, the actor occupies **0 vCPU and 0 MB RAM** on the Kubernetes cluster.

### Turn 2: Synchronized Approval Wave & L7 Request Parking
1. **The Audience Trigger:** The speaker announces: *"Review the code your agent generated on your phone, and tap Approve now!"*
2. **The Saturation Spike:** 50+ attendees tap **[Approve]** within a 5-second window, sending `POST /api/approve` requests to `atenet-router`.
3. **L7 Parking in Action:**
   * Pod-0 and Pod-1 are immediately claimed by the first 2 requests.
   * `ateapi.ResumeActor` returns `ResourceExhausted: "no free workers available"` for the remaining 48 requests.
   * `atenet-router` **parks all 48 requests in memory** with `--parked-request-budget=60s`.
   * On the stage screen, the live parking counter surges to `48`, with zero 503 errors and zero dropped connections.
4. **Draining & Sandbox Execution:**
   * As Pod-0 finishes running its Python script (in ~250ms) and suspends, the next parked request immediately unparks and binds.
   * In under 15–20 seconds, all 50 attendees receive their live analysis results and ASCII graphs on their phones.

---

## 6. User Interface Design

### 6.1 Main Stage Presentation Dashboard (`/stage`)
Optimized for high-contrast presentation on 4K projectors and live streams:
1. **Header & Hero Banner:**
   * Prominent **QR Code** for instant audience onboarding (`Scan to launch your Agent`).
   * **Envoy L7 Parking Lot Gauge:** Live reading from `atenet-router:4040/statusz?format=json` (`.parking.active`). Pulsing pink/amber counter with progress bar during bursts.
2. **Worker Pool Hardware Cards:**
   * Two physical worker pod cards (`Worker Pod-0`, `Worker Pod-1`).
   * Displays active actor ID, execution step, and live millisecond turn timer.
3. **Audience Fleet Matrix:**
   * Visual badge grid for all 50–100 attendees (`Attendee-01` .. `Attendee-100`).
   * Color-coded states:
     * ⚪ **Suspended in GCS** (Sleeping at 0 vCPU, 0 RAM)
     * 🟡 **Parked in Envoy** (Queued in L7 buffer)
     * 🟢 **Running on Pod** (Actively executing inside gVisor)
     * 🟣 **Awaiting Approval** (Holding for attendee sign-off on mobile)
4. **Live Activity Ticker:**
   * Bottom streaming log ticker showing real-time milestones (e.g., *"Attendee #22 approved execution"*, *"Worker-1 resumed Attendee #48 in 230ms"*).

### 6.2 Attendee Mobile Web UI (`/chat`)
Optimized for mobile browsers (iOS Safari, Android Chrome):
1. **Frictionless Onboarding:**
   * Scanning QR code immediately provisions an ephemeral session token with assigned ID (`Attendee #14`).
2. **Interactive Chat & Quick Chips:**
   * One-click starter prompts:
     * `[📊 Outlier Telemetry Analysis]`
     * `[🔒 Sandbox Vulnerability Scan]`
     * `[⚡ Benchmark gVisor CPU]`
3. **Human-In-The-Loop Approval Card:**
   * Highlighting proposed script/command.
   * Clear risk assessment badge (`HIGH RISK: Sandbox Code Execution`).
   * **[Reject]** and **[Approve & Execute]** action buttons.
4. **Substrate Transparency Pill:**
   * Real-time status badge explaining Substrate operations:
     * `⚡ Actor suspended in Cloud Storage (0 MB RAM used)`
     * `⏳ Parked in ingress queue — waiting for worker slot...`
     * `🚀 Resumed in 240ms on Worker Pod 0`

---

## 7. Multi-Tenancy, Auth & Credential Injection

1. **Attendee Ephemeral JWTs:**
   * Gateway generates an HS256 JWT signed with an in-memory cluster secret upon session creation:
     ```json
     {
       "sub": "attendee-14",
       "scope": "agent:chat",
       "exp": 1789913400
     }
     ```
2. **Actor Mapping:**
   * Each attendee is mapped 1:1 to Substrate actor `attendee-XX` in Atespace `ate-demo-conf`.
3. **Secure Header Injection:**
   * The gateway injects `X-Gemini-API-Key: <secret>` when proxying to `atenet-router`. The attendee's mobile browser never receives or exposes the raw API secret.

---

## 8. Safety & Conference Guardrails

1. **Strict Execution Timeouts:**
   * User-submitted or LLM-generated code executed via `execute_sandbox_code` has a hard 5-second `exec.CommandContext` deadline. Rogue scripts (`while True: pass`) are killed cleanly without hanging worker pods.
2. **Container Sandbox Isolation:**
   * Workloads run strictly inside gVisor (`runsc`) sandboxes. Container capabilities are dropped, host filesystem mounts are restricted to emptyDir `/tmp/workspace`, and memory is capped at 256MB.
3. **Automatic Session Cleanup:**
   * Attendee sessions idle for more than 30 minutes are automatically marked for garbage collection.

---

## 9. Automated Verification (`hack/verify-demo-conference.sh`)

An end-to-end verification script to validate functionality in CI and pre-conference checks:
1. Provisions Atespace `ate-demo-conf` and 2-replica WorkerPool `conf-workers`.
2. Launches 25 concurrent headless HTTP simulated attendees.
3. Fires a synchronized prompt wave and captures approval payloads.
4. Fires a synchronized approval wave, polling `atenet-router:4040/statusz` to assert that:
   * `.parking.active > 0` during the burst.
   * 100% of requests return HTTP 200 with 0 dropped packets.
5. Verifies GCS snapshot creation and process resumption.

---

## 10. Repository File Layout

```
demos/conference/
├── README.md                           # Documentation & presentation guide
├── conference.yaml.tmpl                # Atespace, WorkerPool, ActorTemplate manifests
├── cmd/
│   ├── conf-gateway/                   # Edge gateway, mobile auth & stage UI server
│   │   ├── main.go
│   │   └── static/
│   │       ├── stage.html              # Presentation dashboard
│   │       ├── stage.js
│   │       ├── stage.css
│   │       ├── chat.html               # Mobile attendee chat UI
│   │       ├── chat.js
│   │       └── chat.css
│   └── conf-workload/                  # Container workload running in gVisor
│       ├── main.go                     # HTTP server + ADK dispatcher
│       ├── agent.go                    # Google ADK / GenAI Go SDK v2 loop
│       ├── tools.go                    # execute_sandbox_code, research tools
│       └── mock.go                     # Offline fallback reasoning engine
├── pkg/
│   └── types/                          # Shared message, approval & event types
│       └── types.go
hack/
├── install-demo-conference.sh          # Deployment & teardown helper
└── verify-demo-conference.sh           # Automated end-to-end test script
```

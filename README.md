<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Interactive Conference Agent Swarm Demo (`demos/conference`)

An interactive, conference-scale demonstration of **Agent Substrate** showcasing **50 to 100+ stateful AI agents** multiplexed onto a physical `WorkerPool` of **just 2 Kubernetes pods**.

During a live presentation, 50–100+ audience members scan a QR code from the stage projector screen using their smartphones. With zero login friction, each attendee is provisioned an isolated, stateful AI agent running inside a **gVisor sandbox** on the Kubernetes cluster.

Attendees prompt their agents to perform real-time telemetry analysis and code execution. When the agents require **Human-in-the-Loop (HITL)** approval to run privileged code, Substrate immediately snapshots and freezes them to Google Cloud Storage (GCS) at **zero idle compute cost**. When the speaker triggers a synchronized approval wave across the entire auditorium, Substrate's **L7 Request Parking** holds bursts in memory, serving all attendees with **zero dropped requests and zero HTTP 503 errors**.

---

## Architectural Highlights

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

### 1. 50x Oversubscription (50–100 Agents on 2 Physical Pods)
- **The Problem**: Traditional Kubernetes binds one pod per agent tenant. Running 50 to 100 stateful agents requires 50 to 100 dedicated pods or VMs. At 2 vCPU and 4 GB RAM per agent, that amounts to **100–200 vCPUs and 200–400 GB RAM** ($2,000–$4,000/mo), with **99% idle compute waste** because humans spend most of their time reading or typing.
- **The Substrate Solution**: Agent Substrate completely decouples agent identity from container processes. All 50–100 attendee agents multiplex dynamically onto a single `WorkerPool` of **just 2 physical pods** (4 vCPUs and 8 GB RAM total), reducing cluster compute costs by **96% to 98%**.

### 2. Zero-Cost Idle Suspension
- **Instant Freeze to Cloud Storage**: When an agent pauses to ask the attendee for approval to run an analysis script, Substrate takes an incremental snapshot of its live memory pages and filesystem (`pages.img.zstd`, `checkpoint.img.zstd`) and streams it to Google Cloud Storage (`gs://${BUCKET_NAME}/ate-demo-conf/`).
- **Zero Compute Waste**: While the human reads the code on mobile for 30–60 seconds (or 10 minutes), the actor occupies **0 vCPU and 0 MB RAM** on the Kubernetes cluster.
- **Sub-Second Wake-from-Sleep**: When the user submits a decision or next turn, Substrate restores the container into a warm gVisor worker pod in **under 300 ms**, preserving complete memory state without external databases (Redis, Postgres).

### 3. Mass L7 Request Parking
- **The Problem**: In conventional Kubernetes ingress controllers, a burst of 50 concurrent requests hitting a 2-pod service causes an immediate cascade of `503 Service Unavailable` errors and connection resets.
- **The Substrate Solution**: The Substrate router (`atenet-router`) integrates deep Envoy filters to **park excess incoming HTTP requests in memory** with `--parked-request-budget=60s`. During an audience approval wave, 48 requests are held safely in memory and drain through the 2 worker pods in rapid ~200ms increments with **zero dropped packets and zero 503s**.

### 4. Google ADK / GenAI Go SDK v2 Integration
- **Modern Gemini Orchestration**: Workloads are implemented with the official Google GenAI Go SDK v2 (`google.golang.org/genai`), supporting **Gemini 2.5 / 3.5+ Flash and Flash-Lite** models.
- **Function Calling Tools**:
  1. `execute_sandbox_code`: Executes Python or Bash scripts inside an isolated workspace with a hard 5-second timeout.
  2. `fetch_research_data`: Fetches real-time cluster telemetry and anomaly metrics.
  3. `request_human_approval`: Mandates explicit human sign-off before executing privileged code in the sandbox.
- **Dynamic Credential Injection**: The edge gateway injects `X-Gemini-API-Key` headers into upstream router requests. Mobile clients never receive or expose raw API secrets.
- **Conference Wi-Fi Defense (Mock Engine)**: If conference venue Wi-Fi drops or Gemini API rate limits (HTTP 429) are encountered, the runtime automatically falls back to an embedded deterministic mock reasoning engine (`DEMO_MOCK_LLM=true`) to guarantee 100% demo uptime.

### 5. gVisor (`runsc`) Sandbox Security Isolation
- Workloads execute within a hardened **gVisor** boundary, preventing host filesystem tampering or container escapes.
- All code runs within `/tmp/workspace` with dropped privileges and resource boundaries.

### 6. 1-Click Audience Simulation Engine (`/api/simulation`)
- **Synthetic Swarm Generation**: For presentations or rehearsals where 50 physical attendees are not present, the speaker can toggle an autonomous virtual attendee engine directly from the `/stage` header (`[▶ Simulate 25]`, `[▶ Simulate 50]`, `[■ Stop]`).
- **Autonomous Multi-Turn Workloads**: Virtual attendees cycle through realistic prompts (telemetry anomalies, security audits, gVisor benchmarks), wait with simulated human reading delays, and submit human-in-the-loop approvals to generate real burst traffic.
- **Controlled Lifecycle**: Backed by `POST /api/simulation` (`{"action":"start","count":N}` and `{"action":"stop"}`) and `GET /api/simulation` status reporting.

### 7. Interactive Actor Spotlight & Inspection Drawer (`/api/actor/{id}`)
- **Deep MicroVM/Sandbox Introspection**: Clicking any badge in the Audience Fleet Matrix slides open the Actor Spotlight Drawer, providing full transparency into the actor's current state.
- **Cloud Storage Metadata**: Shows the snapshot location in Cloud Storage (`gs://${BUCKET_NAME}/ate-demo-conf/actors/attendee-X.snap`) and exact compressed footprint (e.g., 384 KB).
- **Execution & Turn History**: Displays the actor's conversation turns, pending human approvals, proposed script code with syntax highlighting, and live sandbox execution metrics (stdout, stderr, exit code, duration in ms).

### 8. Real-Time Server-Sent Events (SSE) State Synchronization (`/api/events`)
- **Push-Based State Propagation**: The mobile client (`/chat`) connects to `/api/events?attendee_id=...` via standard browser `EventSource`, replacing client-side polling and artificial delays with authentic, server-driven lifecycle events.
- **Instant Visual Feedback**: The mobile Substrate Transparency Pill dynamically reflects authoritative cluster states in real-time (`THINKING_VERTEX_AI`, `AWAITING_APPROVAL`, `PARKED_ENVOY`, `RUNNING_GVISOR`, `SUSPENDED`).

### 9. 1-Click Cluster Reset Controller (`/api/reset`)
- **Instant Clean-Slate Recovery**: The `[⟲ Reset All]` button on `/stage` invokes `POST /api/reset` to halt active simulations, wipe pending approvals and chat histories, and issue parallel suspension sweeps across all running or awaiting actors.
- **Zero Orphaned Workers**: Ensures all actors are safely frozen to GCS (`types.StateSuspended`), freeing worker pods to idle in milliseconds and preparing the cluster for repeated presentation runs.

### 10. Real-Time Live Actor Action Stream (`/feed`, `/api/stream`, `/api/feed/history`)
- **Fleet-Wide Event Pipeline**: While `/api/events` provides per-attendee state synchronization, `/api/stream` broadcasts every attendee prompt, Gemini reasoning response, generated Python code, HITL approval decision, and gVisor terminal output across all 50–100 actors over Server-Sent Events (SSE).
- **In-Memory Ring Buffer**: `conf-gateway` maintains an in-memory ring buffer of the last 100 action events exposed via `GET /api/feed/history`, allowing newly connected dashboards to instantly hydrate full conversation and execution history.
- **Dedicated Live Streaming Dashboard**: Available at `/feed`, offering a full-page cyber-themed dashboard with real-time state filtering, instant full-text search, auto-scrolling terminal cards, and syntax-highlighted code execution logs.

### 11. Segmented Attendee Allocation (Zero Human/Swarm Collision)
- **The Challenge**: When background virtual swarms are active (e.g., 25 simulated attendees occupying `attendee-01`..`attendee-25`), real humans scanning the QR code at `/chat` could collide with simulated actors if IDs were allocated sequentially from 1. This would cause hijacked conversations, conflicting prompts, or corrupted HITL states.
- **Dynamic Range Partitioning**: `conf-gateway` (`GET /api/session`) inspects the active `SimulationController` status. When simulation is active with count $N$, newly connected human attendees are dynamically partitioned into the unreserved range from $N+1$ up to `maxActors` (e.g., `attendee-26` through `attendee-50` when `count=25`), cycling safely within this partition.
- **Fair Multi-Tenant Competition**: Human and virtual attendees share the exact same 2 physical worker pods (`conf-workers-0`, `conf-workers-1`). Humans experience authentic Envoy L7 request parking and sub-300ms container wake-ups under swarm load with zero session collisions.
- **Explicit Actor Overrides**: URL parameters (`/chat?attendee_id=attendee-07`) continue to be honored directly for reproducible testing, specific attendee demonstrations, and presenter spotlighting.
- **Graceful Lifecycle Reversion**: Halting simulation or triggering 1-click reset dynamically restores attendee session allocation to the full unpartitioned range (`1..maxActors`) without server restart.

---

## User Interface Guide

The demo provides three specialized interfaces designed for live presentations and audience participation:

### 1. Stage Presentation Dashboard (`/stage`)
Optimized for 1080p and 4K conference projector screens with a high-contrast dark theme:

| Component | Description |
|---|---|
| **QR Code Banner** | Prominently displayed QR code linking directly to `/chat` for rapid smartphone onboarding. |
| **View Switcher** | Segmented header toggle (`[⊞ Grid Matrix | ≡ Live Stream]`) to seamlessly toggle between the 50–100 badge fleet matrix and the integrated live action stream directly on stage. |
| **Simulation Controls** | `[▶ Simulate 25]`, `[▶ Simulate 50]`, and `[■ Stop]` buttons in the header to launch or halt autonomous virtual attendee traffic on demand. |
| **1-Click Cluster Reset** | `[⟲ Reset All]` button in the header triggering immediate parallel suspension of all active actors and clearing cluster state. |
| **Envoy L7 Parking Lot Gauge** | Real-time gauge polling `statusz` (`parking.active`), visualizing queued requests during audience bursts. |
| **Hardware Worker Cards** | Displays the real-time execution state of `conf-workers-0` and `conf-workers-1`, including active tenant ID, current task, and live turn timers. |
| **Audience Fleet Matrix** | Interactive 50–100 badge grid displaying each attendee's real-time lifecycle state: ⚪ **Suspended in GCS**, 🟡 **Parked in Envoy**, 🟢 **Running on Pod**, 🟣 **Awaiting Approval**. |
| **Embedded Live Stream** | Alternative stage view (`#stage-stream-view`) presenting incoming action cards in real-time; clicking any card opens the Actor Spotlight Drawer. |
| **Actor Spotlight Drawer** | Click any fleet matrix badge (`#badge-attendee-XX`) or stream action card to slide open a detailed drawer displaying live code, execution stdout/stderr, and GCS snapshot location/size (`GET /api/actor/{id}`). |
| **Streaming Activity Ticker** | Live bottom ticker logging cluster events, turn durations, and suspension snapshot completions. |

### 2. Mobile Attendee Chat App (`/chat`)
A lightweight, mobile-optimized web application for attendee smartphones:

| Feature | Description |
|---|---|
| **Zero-Friction Auth** | Scanning the QR code mints an ephemeral HS256 JWT via `GET /api/session`, assigning a unique identifier (e.g., `Attendee #14`). |
| **Quick Chip Prompts** | One-tap starter prompts: `[📊 Outlier Telemetry Analysis]`, `[🔒 Sandbox Vulnerability Scan]`, `[⚡ Benchmark gVisor CPU]`. |
| **HITL Approval Cards** | Displays proposed Python/Bash code with risk assessment badges (`HIGH RISK: Sandbox Code Execution`), allowing the user to **Approve & Execute** or **Reject**. |
| **Substrate Transparency Pill** | Real-time badge bound to Server-Sent Events (`/api/events`), informing the user of authoritative Substrate states (e.g., *"⚡ Actor suspended in Cloud Storage (0 MB RAM used)"*, *"⏳ Parked in ingress queue — waiting for worker slot..."*). |

### 3. Dedicated Live Stream Dashboard (`/feed`)
A full-screen cyberpunk-themed streaming dashboard designed for secondary stage screens, technical overflow rooms, or backstage monitoring:

| Feature | Description |
|---|---|
| **Header Status & Controls** | Displays brand identity, live Envoy L7 parking meter badge (`#parking-badge`), quick navigation back to `/stage`, and the 1-click `[⟲ Reset All]` controller (`#btn-reset-all`). |
| **State Filter Pills** | Real-time filter buttons (`All`, `Awaiting Approval`, `Running`, `Completed`) to filter cards based on actor lifecycle state. |
| **Full-Text Live Search** | Client-side search input (`#feed-search`) filtering cards in real-time across prompts, generated code, attendee IDs, and execution output. |
| **Auto-Scroll Engine** | Smooth auto-scroll toggle (`#feed-autoscroll`) that automatically keeps the viewport pinned to the latest incoming execution events. |
| **Interactive Action Cards** | Rich cards rendering prompts, Gemini reasoning responses, code blocks with one-click copy, live gVisor execution output, durations, and deep links to inspect the attendee in `/chat`. |

---

## Live Speaker Playbook

A minute-by-minute cue sheet for presenting the demo on stage:

```
+-----------------------------------------------------------------------------------------------+
| TIME   | STAGE CUE                 | SPEAKER SCRIPT                    | CLUSTER ACTION       |
+-----------------------------------------------------------------------------------------------+
| 00:00  | The Hook                  | "Look at the screen: 2 pods total. | Attendees scan QR;   |
|        | Projected /stage showing  | Scan this QR code on your phone   | 50 sessions minted   |
|        | 2 idle workers and empty  | to get your own isolated agent."  | in SUSPENDED state.  |
|        | fleet matrix.             |                                   |                      |
+-----------------------------------------------------------------------------------------------+
| 01:00  | The Virtual Swarm &       | "Need a full room? Watch this:    | POST /api/simulation |
|        | Segmented Allocation      | 25 virtual bots start prompting.  | launches swarm 01-25;|
|        | Click [▶ Simulate 25]     | If you scan now, you get assigned | /api/session assigns |
|        | in stage header.          | attendee-26+ with zero collision! | attendee-26+ cleanly.|
+-----------------------------------------------------------------------------------------------+
| 01:45  | The Prompt & Spotlight    | "Let's inspect Attendee #07. Click| Drawer fetches       |
|        | Click badge attendee-07   | the badge to open the Spotlight   | /api/actor/attendee-07|
|        | to open Spotlight drawer. | Inspector with live code and GCS  | showing prompt, code, |
|        |                           | snapshot metadata."               | and GCS snapshot.    |
+-----------------------------------------------------------------------------------------------+
| 02:45  | The Freeze                | "Look at the grid: all agents are | All actors frozen;   |
|        | Fleet turns solid purple; | purple (Awaiting Approval).       | RAM streamed to GCS; |
|        | workers return to idle.   | But our pods use 0% CPU and 0 MB! | 0 vCPU, 0 RAM used.  |
|        |                           | Take your time reading the code." |                      |
+-----------------------------------------------------------------------------------------------+
| 03:45  | The Flash Mob (Surge)     | "On count of three, everyone tap  | 50 concurrent        |
|        | Speaker counts down:      | APPROVE at the exact same instant!| approvals hit Envoy; |
|        | 3... 2... 1... TAP!       | 3... 2... 1... TAP APPROVE!"      | 48 requests park!    |
+-----------------------------------------------------------------------------------------------+
| 04:00  | Flip to Live Stream       | "Let's flip to the Live Stream    | Speaker toggles      |
|        | Click [≡ Live Stream]     | view! Watch every Python script   | [≡ Live Stream];     |
|        | in stage view switcher.   | execute inside gVisor and stream  | live action cards    |
|        |                           | terminal stdout in real-time."    | stream over SSE.     |
+-----------------------------------------------------------------------------------------------+
| 04:30  | The Unparking Drain       | "Watch the parking meter drain!   | Workers multiplex    |
|        | Parking meter counts down | Standard K8s would have 503'd.    | 200ms turns; all 50  |
|        | from 48 to 0 in real-time.| Substrate served all 50 with zero | get live ASCII plots |
|        |                           | dropped requests!"                | with HTTP 200 OK.    |
+-----------------------------------------------------------------------------------------------+
| 05:15  | 1-Click Cluster Reset     | "Clicking Reset All suspends every| POST /api/reset      |
|        | Click [⟲ Reset All]       | active actor back to GCS and frees| parallel-suspends all|
|        | in stage header.          | the cluster for the next talk."   | actors, clearing pods|
+-----------------------------------------------------------------------------------------------+
```

> **Presenter Cue — Segmented Attendee Allocation (Zero Human/Swarm Collision)**:
> When running a simulated swarm during live talks, `conf-gateway` automatically partitions newly onboarding human phones into attendee IDs beyond the simulation count (e.g., `attendee-26`..`attendee-50` when `count=25`). Both real attendees and virtual bots compete for execution turns across the same 2 physical worker pods, allowing human audience members to experience authentic L7 request parking and sub-300ms container wake-ups without collision with background bots. For controlled demonstrations of specific actors, pass explicit query parameters (e.g. `/chat?attendee_id=attendee-07`).

---

## Prerequisites

- **Kubernetes Cluster**: Local `kind` cluster with gVisor support or Google Kubernetes Engine (GKE).
- **Agent Substrate**: Installed in the cluster (`./hack/install-ate.sh --deploy-ate-system`).
- **CLI Tools**:
  - `kubectl` configured with cluster context.
  - `kubectl-ate` (`go install ./cmd/kubectl-ate` or `go run ./cmd/kubectl-ate`).
  - `ko` for building container images.
  - `curl` and `jq` installed locally.
- **Environment Variables**:
  - `BUCKET_NAME`: Google Cloud Storage bucket for actor snapshots.
  - `GEMINI_API_KEY`: (Optional) Google Gemini API key for live LLM reasoning.

---

## How to Run on Agent Substrate

### 1. Deploy the Conference Demo

Deploy the `ate-demo-conf` namespace, the 2-worker `WorkerPool`, the `conf-agent` template, and 50 pre-provisioned attendee actors:

```bash
# Ensure GCS bucket is exported
export BUCKET_NAME="my-substrate-snapshots"

# Deploy conference demo
./hack/install-demo-conference.sh --deploy
```

The script automatically:
- Adjusts `atenet-router` with `--parked-request-budget=60s`.
- Builds and deploys the `conf-workload` container using `ko`.
- Creates `WorkerPool` `conf-workers` (2 replicas with gVisor sandboxing).
- Provisions 50 sleeping actors (`attendee-01` through `attendee-50`) in `ate-demo-conf`.

### 2. Setup Port-Forwarding

In a dedicated terminal, port-forward the `atenet-router` service:

```bash
# Forward L7 router traffic (port 80 -> 8000)
kubectl port-forward -n ate-system svc/atenet-router 8000:80 &

# Forward router statusz telemetry (port 4040 -> 4040)
kubectl port-forward -n ate-system svc/atenet-router 4040:4040 &
```

### 3. Run the Demo Edge Gateway

Start the conference edge gateway locally to serve the web UIs and proxy traffic:

```bash
export ROUTER_URL="http://localhost:8000"
export STATUS_URL="http://localhost:4040"
export GEMINI_API_KEY="your-gemini-api-key"   # Optional: omit for mock reasoning mode
export PORT="8090"

go run ./demos/conference/cmd/conf-gateway
```

### 4. Access the UIs

- **Stage Presentation Dashboard**: Open [http://localhost:8090/stage](http://localhost:8090/stage) on your projector or external monitor.
- **Dedicated Live Stream Dashboard**: Open [http://localhost:8090/feed](http://localhost:8090/feed) on a secondary screen or dedicated monitoring display.
- **Mobile Attendee Chat App**: Open [http://localhost:8090/chat](http://localhost:8090/chat) on your mobile phone (or test in desktop browser responsive view).

---

## Local Development & Offline Mock Mode

For development, testing, or presentations without active Google Cloud / Gemini credentials:

### 1. Offline Reasoning
Set `DEMO_MOCK_LLM=true` to force deterministic offline reasoning:

```bash
# Start workload in standalone mock mode
PORT=8080 DEMO_MOCK_LLM=true go run ./demos/conference/cmd/conf-workload
```

### 2. Standalone Verification
You can run full end-to-end automated verification without needing a live Kubernetes cluster using `--mock-mode`:

```bash
./hack/verify-demo-conference.sh --mock-mode
```

---

## Automated Verification (`hack/verify-demo-conference.sh`)

The automated verification script validates all conference subsystems end-to-end:

```bash
# Run against a running cluster deployment
./hack/verify-demo-conference.sh --gateway-url http://localhost:8090 --concurrency 25

# Run in self-contained mock mode
./hack/verify-demo-conference.sh --mock-mode --concurrency 20
```

### Verification Steps Performed:

1. **Step 1: Health & Static UI Assets**: Asserts that `/healthz`, `/stage`, `/chat`, and all static JS/CSS assets return HTTP 200.
2. **Step 2: Ephemeral Session Minting**: Calls `GET /api/session` and validates that an ephemeral attendee ID and a valid, signed HS256 JWT are generated.
3. **Step 3: Turn 1 Chat & HITL Approval**: Sends a prompt to `/api/chat` and confirms receipt of an assistant message containing a pending `ApprovalRequest` with language, risk rating, and executable code.
4. **Step 4: Turn 2 Approval & Sandbox Execution**: Submits `POST /api/approve` and asserts execution inside the gVisor sandbox, confirming exit code 0 and stdout metrics.
5. **Step 5: Audience Burst & Request Parking**: Fires concurrent requests across multiple attendees, asserting that request parking is engaged, with **zero dropped packets and zero HTTP 503 errors**.
6. **Step 6: Telemetry Endpoint**: Verifies `GET /api/stage/telemetry`, checking physical worker slots (`conf-workers-0`, `conf-workers-1`), active actor tracking, and live cluster events.
7. **Step 7: Audience Simulation Engine**: Verifies `/api/simulation` by starting 5 virtual attendees (`POST /api/simulation` with `action: start`), asserting `GET /api/simulation` reports `running: true`, asserting segmented human attendee allocation beyond the simulation batch (`GET /api/session` assigns `attendee-06` or higher), stopping simulation (`action: stop`), confirming `running: false`, and verifying normal session allocation resumes.
8. **Step 8: Actor Spotlight Inspector API**: Verifies `GET /api/actor/attendee-01`, asserting HTTP 200 OK and validating presence of `actor_id`, `state`, `snapshot_location`, and `snapshot_size_bytes`.
9. **Step 9: 1-Click Cluster Reset Controller**: Submits `POST /api/reset`, verifies `HTTP 200 OK` with `status: ok`, and asserts `GET /api/actor/attendee-01` transitions to `SUSPENDED`.
10. **Step 10: Dedicated Live Feed Page (`/feed`)**: Verifies `GET /feed` returns HTTP 200 OK with `text/html`, asserts presence of `#action-stream`, `#feed-search`, `#feed-autoscroll`, and `#btn-reset-all`, and confirms `/static/feed.js` and `/static/feed.css` return HTTP 200.
11. **Step 11: Action Feed History API (`/api/feed/history`)**: Verifies `GET /api/feed/history` returns HTTP 200 OK with `application/json` and asserts the response is a valid JSON array (`jq 'type == "array"'`).
12. **Step 12: Real-Time Action Stream SSE (`/api/stream`)**: Verifies `GET /api/stream` returns HTTP 200 OK with `Content-Type: text/event-stream`, establishes connection handshake, and reads streamed action events.

---

## Teardown

To delete all conference demo resources and restore default router settings:

```bash
./hack/install-demo-conference.sh --delete
```

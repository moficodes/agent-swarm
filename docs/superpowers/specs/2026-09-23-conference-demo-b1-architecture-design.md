# Design: Conference Demo B1 — Actor-Owned Agent Loop, Real Substrate State, In-Cluster Gateway

Status: approved in review. Implementation plan to follow.

## 1. Problem

A review of `demos/conference` found three gaps between what the demo says and what it does.

1. **Actors do not do the agent work.** `conf-gateway/main.go` always installs an AI client, so `/api/chat` runs Gemini inside the gateway (`gateway.go`, `handleChatRequest`). Chat traffic never reaches an actor. An actor gets traffic only when an approval is proxied to it, and then it runs whatever `code` the request carries, because it never saw the turn that proposed that code.
2. **Most displayed state is invented.**
   - `PARKED_ENVOY`, `RUNNING_GVISOR`, `THINKING_VERTEX_AI` and the worker and snapshot details come from gateway timers and a `workerSlots` semaphore. None of them are read from Substrate.
   - The gateway cannot see real parking for each actor, because the router reports only an aggregate count.
3. **Credentials and defaults leak.**
   - The Gemini client falls back to a hardcoded GCP project (`mofilabs`), both in `pkg/ai` and in the workload.
   - Leaving `GEMINI_API_KEY` unset does not give mock mode. It gives Vertex in that project, and mock only if that call fails.

Phase A (commits `050f6a15`..`7e222130`) closed the security holes. B1 fixes the architecture so the demo actually shows Agent Substrate. B2, a separate spec, will deduplicate the UI.

## 2. Decisions

| Topic | Decision | Rejected alternatives |
|---|---|---|
| Who runs the agent loop | The actor: Gemini loop, history, tool calls, pending approval | The gateway runs the loop (as today) |
| Model credentials | Actors call a stateless model proxy in the gateway with a per-actor key. The proxy calls Vertex using Application Default Credentials, which is Workload Identity in-cluster. | **Workload Identity inside the actor:** it would be the shared worker-pool identity, tokens would land in snapshots, and metadata-server reachability through Substrate egress is unverified. **Egress credential injection:** it does not apply on the CONNECT leg, no in-tree `CredentialProvider` exists, and hermes found it unusable. **API key in template env:** the key would be baked into the template and golden snapshot. |
| Where the gateway runs | In-cluster Deployment | On a laptop behind port-forwards |
| How the gateway reaches ateapi | A `kubectl-ate` binary built from the same substrate commit, run as a subprocess with `--endpoint` set to the in-cluster ateapi Service | Copying `internal/ateclient` (core code). Promoting it to `pkg/` (a core API decision). A hand-written gRPC client (hermes and always-on-agent both avoided this). |
| Source of actor state | Poll `kubectl-ate get actors -o json` about once a second and push changes to the UI over SSE | Gateway-tracked state, OTLP events |

Prior art: `agent-substrate/hermes` and `agent-substrate/always-on-agent` both run their gateway in-cluster, shell out to a pinned `kubectl-ate`, and poll `get actors -o json`. hermes also uses a gateway-side Vertex proxy with Workload Identity.

## 3. Architecture

```
 phone / projector
        │  HTTP(S) via LoadBalancer
        ▼
┌──────────────── conf-gateway (Deployment, 1 replica) ─────────────────┐
│ UI, sessions, presenter auth                                          │
│ /api/chat, /api/approve ──► atenet-router (ate-target-actor header)   │
│ :8091 /model/* ◄── actors: model proxy (ADC / Workload Identity)      │
│ actor state poller: kubectl-ate get actors -o json ──► SSE            │
│ lifecycle calls: kubectl-ate suspend (Phase C adds pause, revert)     │
└───────────────────────────────────────────────────────────────────────┘
        │ via router                         ▲ via Substrate egress
        ▼                                    │
┌──────── conf-workload actor (one per attendee, gVisor) ────────┐
│ Gemini loop, chat history, pending approval (in memory)        │
│ /api/approve runs only its own pending code                    │
│ executor: minimal environment, runs as nobody                  │
└────────────────────────────────────────────────────────────────┘
```

### 3.1 Chat turn

1. The phone POSTs to `/api/chat` on the gateway, which authenticates the attendee JWT.
2. The gateway proxies the request to the router with `ate-target-actor: <atespace>/<attendee>` and the actor's model key header (§5.1). The router resumes the actor if needed, parking the request while it does.
3. The actor appends the message to its history and calls Gemini through the proxy. It returns either a reply or an approval request, and records the approval as its pending approval.
4. The gateway records the turn in the action feed from the proxied response. It keeps its own copy of the pending approval only for the Phase A binding check.

### 3.2 Approval

1. The phone POSTs to `/api/approve`. The gateway binds the request to that attendee's pending approval (Phase A: ID match, stored code substituted, no replay).
2. The actor checks the approval against **its own** pending approval. With no match it returns 409. The actor ignores any `code` field; the "no pending approval but code supplied" path in `server.go` is removed.
3. On approve, the actor runs its stored code in the sandbox and returns the output. On reject, it clears the pending approval. Phase C replaces this reject behavior with RevertActor.

### 3.3 History across suspend

History lives in actor memory, so it is part of the snapshot and survives suspend and resume. The stage can show this as a feature. The gateway's per-attendee chat copy exists only for the feed and stage spotlight; it is not the source of truth.

### 3.4 Idle suspension

The current behavior stays: the gateway suspends an actor after its turn completes. An optional delay is added (`SUSPEND_AFTER`, default `0`, meaning immediately, as today), and suspension goes through `ActorControl.Suspend`.

## 4. State model

The UI shows two things, labeled separately.

### 4.1 Substrate state (primary badge)

This is `status.state` from ateapi, using ateapi's own names.

| ateapi | Label | Detail shown |
|---|---|---|
| `SUSPENDED`, no `external_snapshot` | Never started | — |
| `RESUMING` | Resuming | worker pod, once assigned |
| `RUNNING` | Running | `status.workerAssignment` worker pod |
| `SUSPENDING` | Suspending | `in_progress_snapshot_uri` |
| `SUSPENDED` | Suspended | `external_snapshot` URI |
| `PAUSING` / `PAUSED` | Pausing / Paused | used in Phase C |
| `REVERTING` | Reverting | used in Phase C |
| `CRASHED` | Crashed (red) | — |
| poller unavailable | State unavailable | reason (local mode, or ateapi unreachable) |

### 4.2 App activity (secondary chip)

This is what the gateway sees from proxied traffic:
- **Thinking**: a chat request is in flight.
- **Awaiting approval**: the last response carried an approval that hasn't been answered.
- **Executing**: an approval is in flight.

The UI labels it as app activity. "Awaiting approval · Suspended" is a valid, intended combination.

When a reply came from the mock engine, the chat, the feed and the spotlight label it "mock". They label it "mock (Vertex unavailable)" when it was a fallback after a Vertex error.

### 4.3 Parking

The router statusz exposes only an aggregate `parking.active` (`ingress.ParkingStatus`). The stage shows it as one counter, summed across all router pods. No actor carries a per-actor "Parked" badge.

### 4.4 Timing honesty

- State lags by one poll interval plus the `kubectl-ate` call time. The implementation measures that call time and documents it.
- A RESUMING that finishes within one poll may never be shown.
- Resume latency on the stage is measured from the proxied request's round trip, not from gaps between polls.

### 4.5 Types

`pkg/types` drops `PARKED`, `PARKED_ENVOY`, `RUNNING_GVISOR`, `THINKING_VERTEX_AI` and the timer code that sets them. New types:
- `SubstrateState`, which mirrors ateapi's names;
- `Activity`;
- `ActorView{State, Activity, WorkerPod, SnapshotURI, Mock}`.

The gateway diffs successive polls and sends only changed `ActorView`s over SSE.

## 5. Model proxy and credentials

### 5.1 Per-actor key

- `key = <actor>.<base64url(HMAC-SHA256(secret, atespace + "/" + actor))>`, where `secret` is the gateway's HMAC secret (§6).
- The gateway sends the key to the actor on every proxied request in `X-Conf-Model-Key`. The actor keeps the latest key in memory.
- No key appears in the template or env.
- A key captured in a snapshot is valid only for that one actor, against the proxy. Rotating the secret revokes every key.
- The proxy strips `X-Conf-Model-Key` from anything it forwards upstream. The gateway sets it only on requests to the router, never on responses.

### 5.2 Actor side

- The genai SDK runs with the Vertex backend and `HTTPOptions.BaseURL = MODEL_PROXY_URL`.
- `Credentials` is a static token source returning the key as a bearer token. The vendored SDK exposes `ClientConfig.Credentials *auth.Credentials`.
- `VERTEX_PROJECT`, `VERTEX_LOCATION` and `GEMINI_MODEL` come from template env. They are not secret.
- `DEMO_MOCK_LLM=true` forces the mock engine.
- The first plan task verifies this client configuration end to end against a fake proxy before anything else depends on it.

### 5.3 Proxy (`modelproxy`)

1. **Authenticate:** parse `Authorization: Bearer <actor>.<mac>`, recompute the MAC and compare in constant time. Failure returns 401.
2. **Authorize:** allow only `POST /model/v1*/projects/{P}/locations/{L}/publishers/google/models/{M}:generateContent`, where P and L equal the gateway's configuration and M is in `MODEL_ALLOWLIST`. Anything else returns 403. Streaming is not allowed.
3. **Forward:**
   - remove `Authorization` and set `Authorization: Bearer <ADC token>`, with ADC from `golang.org/x/oauth2/google`, scope `cloud-platform`;
   - send the request to `https://{L}-aiplatform.googleapis.com` with the `/model` prefix stripped;
   - apply a 30s upstream timeout.
4. **Limit:** request bodies are capped at 256 KiB (413), and each actor may have at most 2 calls in flight (429).
5. **Account:** parse `usageMetadata` from the response and add prompt and candidate tokens and latency to that attendee's stats. The stage and spotlight read these stats.

The proxy listens on its own port (`MODEL_PROXY_ADDR`, default `:8091`), exposed only through a ClusterIP Service. The public Service never routes to it.

When no `VERTEX_PROJECT` is configured, the proxy is disabled (every request returns 503), the install script sets `DEMO_MOCK_LLM=true` on actors, and the stage header shows "Mock LLM".

`GEMINI_API_KEY` is removed from both binaries.

### 5.4 Remaining risk (documented in the README)

Code approved into an actor's sandbox can call the proxy as that actor. That spends the attendee's quota on the allowlisted endpoint. No credential can leave the cluster.

## 6. Deployment

### 6.1 Image

- `conf-gateway` gets a multi-stage Dockerfile. The first stage builds `kubectl-ate` from the same substrate commit; the final image is distroless and contains both binaries.
- Once the demo is standalone, the substrate commit becomes a pinned `ARG SUBSTRATE_REF`.
- `conf-workload` stays on `ko`.

### 6.2 Objects in `ate-demo-conf`

| Object | Details |
|---|---|
| ServiceAccount `conf-gateway` | `iam.gke.io/gcp-service-account` pointing at a GSA with only `roles/aiplatform.user` |
| ClusterRole and binding | `list` on `certificates.k8s.io/clustertrustbundles`, which `kubectl-ate` uses to verify ateapi |
| Role and binding in `ate-system` | `create` on `serviceaccounts/token`, `resourceNames: [ate-client]` |
| Secret `conf-gateway` | `PRESENTER_TOKEN`, `HMAC_SECRET` (also signs JWTs). Created by the install script if missing, so sessions and model keys survive gateway restarts. |
| Deployment | 1 replica. Sessions, feed and approvals are in memory, and this limit is documented. Env: `ATEAPI_ENDPOINT=api.ate-system.svc:443`, `ROUTER_URL`, a router statusz target, `VERTEX_*`, `MODEL_ALLOWLIST`. |
| Service `conf-gateway` | LoadBalancer, UI and API port only |
| Service `conf-gateway-internal` | ClusterIP, model proxy port only |
| Ingress (optional) | Created when `DEMO_DOMAIN` is set, with a Google-managed certificate. Without it, attendee traffic is plain HTTP, and the README says so. |

No port-forward permission is needed.

**To verify during implementation:**
- how the gateway enumerates router pods for statusz (a headless Service or the Endpoints API, which would need RBAC);
- that `kubectl-ate` with `--endpoint` picks up in-cluster config through client-go's deferred loading.

### 6.3 Access

- Attendees use the LoadBalancer address, encoded in the stage QR code.
- The recommended presenter path is `kubectl port-forward svc/conf-gateway 8090:80` and then `localhost:8090/stage?key=…`, which keeps the presenter key off venue Wi-Fi.

### 6.4 Install script (`hack/install-demo-conference.sh`)

- When `PROJECT_ID` is set, create the GSA and the Workload Identity binding. Otherwise deploy in mock mode and print that clearly.
- Stop patching the shared `atenet-router` by container index. Instead:
  - verify `--parked-request-budget` and `--route-timeout` are each at least 60s;
  - otherwise fail with the exact flags to set;
  - `--patch-router` opts into patching, but deletion no longer restores flags it didn't set.
- Pre-create the 50 actors as today, with template env for the proxy and Vertex.
- Deploy the gateway objects above.

## 7. Code structure

`cmd/conf-gateway/internal/`:

| Package | Responsibility |
|---|---|
| `gateway` | Options, mux, lifecycle wiring |
| `auth` | Attendee JWTs, presenter key, attendee-ID validation |
| `actorproxy` | Chat and approve proxying, approval binding, model key header, activity tracking |
| `modelproxy` | §5.3 |
| `actorstate` | `ActorControl` interface (`List`, `Suspend`; Phase C adds `Pause`, `Revert`), the `kubectl-ate` implementation, the poller and the diff |
| `feed` | Action ring buffer and SSE broker |
| `routerstatus` | statusz aggregation |
| `simulation` | Unchanged behavior. It already goes through the gateway's handlers, so after B1 it drives real actors. |
| `web` | Pages and static assets |

`cmd/conf-workload/internal/`:
- `server`: loop and approval binding;
- `gemini`: proxy-configured client;
- `engine`: executor, which now builds `cmd.Env` from scratch as `PATH`, `HOME=<workspace>`, `LANG=C.UTF-8`.

Deleted: `pkg/ai`, `pkg/engine`, the gateway AI path, `workerSlots`, the invented snapshot and worker data, `commandSuspender.getClusterRunningActors`, and both `mofilabs` defaults.

## 8. Testing

- **Gateway ↔ actor contract:** an in-process fake router dispatches on `ate-target-actor` to real `conf-workload` server instances, one per attendee. Chat, approval, history, both approval-binding layers and model-key delivery are tested without mocking either side.
- **Model proxy:** a fake Vertex server (`httptest`) and an injected token source. Cases:
  - bad or missing MAC → 401;
  - wrong project, location or model, and streaming → 403;
  - the actor token never reaches upstream and the ADC token does;
  - body cap → 413;
  - concurrency cap → 429;
  - usage is recorded against the right attendee.
- **`kubectl-ate` implementation:** the command runner is injected. Parsing is tested against a JSON fixture captured from a real cluster that covers never-started, running, suspended-with-snapshot and crashed actors.
- **Poller:** a fake `ActorControl`. Only changes emit SSE events. An error produces "State unavailable" rather than stale state.
- **Executor env:** approved code that prints its environment shows no gateway- or actor-specific variables.
- **Local mode:** `ActorControl` is unset, and `--mock-mode` verify checks routing, auth, approvals and the feed, but not Substrate states.
- **Pruning:** tests of deleted code go with it. Tests of real behavior (auth, approvals, proxy headers, feed history, SSE, reset, simulation) are ported. New and ported tests:
  - are named for the behavior they protect;
  - synchronize on channels, not on sleeps with deadlines;
  - for the HMAC check, the allowlist, both approval-binding layers and the presenter guard, fail when the guard is disabled.
- `go test -race` for all demo packages.

## 9. Implementation order

Each step keeps the tests and `--mock-mode` verify green.

1. Workload: proxy-configured Gemini client (verify the SDK credentials path first), actor-side approval binding, minimal executor env.
2. Gateway: `modelproxy`.
3. Gateway: remove the AI path; chat goes to the actor; model key header.
4. `actorstate`, the poller, the new types and the state and activity labels on the stage and feed (minimal UI changes; restyling is B2).
5. Package split and test pruning.
6. Dockerfile, manifests, RBAC, install script.
7. README and verify script; measure and document the `kubectl-ate` poll latency.

## 10. Out of scope, and upstream follow-ups

Out of scope:
- **B2:** deduplicate the stage, feed and chat JS and CSS, and restyle around the new state model.
- **Phase C:** Pause while awaiting approval, Reject → RevertActor, restore and parking metric tiles.
- **Actor EgressPolicy (allow only the gateway's internal host):** `kubectl-ate` has no egress policy commands, and B1 does not call ateapi's gRPC API directly.

Upstream follow-ups to raise with core:
1. `kubectl-ate` commands for actor EgressPolicy.
2. A watch or streaming ListActors, if polling latency proves too coarse on stage.
3. `cmd/atenet/internal/router/README.md` still says credential injection is unimplemented, but `egress/credentials.go` implements it on the TLS-intercept leg.

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

## What it shows

50 attendee actors (`attendee-01` .. `attendee-50`, one `conf-agent` actor
each) multiplexed onto a `WorkerPool` of 2 Kubernetes pods
(`demos/conference/conference.yaml.tmpl`). Each attendee's conversation
history lives in the actor's own process memory
(`demos/conference/internal/workload/server/server.go`); the actor's
`snapshotsConfig` captures full content on pause and commit
(`demos/conference/conference-template.yaml.tmpl`), so the conversation
survives Substrate suspending and resuming the actor on a different worker
pod. A stage reset, or handing an `attendee-NN` slot to a new visitor, has
the gateway clear that history through the actor's `/api/history/clear`
endpoint.

The `/stage` header's "2 Physical Worker Pods" and "50x Oversubscription"
copy (`demos/conference/cmd/conf-gateway/static/stage.html`) states the
default `WorkerPool` size and attendee count
(`demos/conference/conference.yaml.tmpl`'s `replicas: 2`,
`hack/install-demo-conference.sh`'s `DEMO_CONF_ACTORS=50`); it is fixed
header copy, not a live count read from the cluster.

## Architecture

```
browser
  |  HTTP(S)
  v
conf-gateway (in-cluster, Deployment in ate-demo-conf, 1 replica)
  |  chat/approve requests, ate-target-actor header
  v
atenet-router (ate-system)
  |
  v
actor (conf-workload, on a conf-workers pod; no egress)
  |  answers with a model_call step; the gateway posts the result
  |  back to /api/model-result through the router
  v
conf-gateway makes the model call
  |  Vertex AI call, Workload Identity token
  v
Vertex AI (generateContent)

conf-gateway  --kubectl-ate-->  ateapi (api.ate-system.svc:443)
              (polls actor state; also issues the suspend after a turn)
```

- Browser to gateway: plain HTTP through the `conf-gateway` `LoadBalancer`
  Service, unless `DEMO_DOMAIN` is set, in which case
  `gateway-ingress.yaml.tmpl` fronts it with a GKE-managed TLS Ingress.
  Its `BackendConfig` raises the load balancer's backend timeout to an hour
  so long turns and SSE streams are not cut at the 30s default.
- Gateway to actor: `conf-gateway` proxies `/api/chat` and `/api/approve`
  through `atenet-router` (`ROUTER_URL=http://atenet-router.ate-system.svc:80`
  in `demos/conference/manifests/gateway.yaml.tmpl`) with an
  `ate-target-actor` header identifying the attendee's actor
  (`demos/conference/cmd/conf-gateway/internal/actorproxy/proxy.go`).
- Actor to model: actors run with egress denied, so the actor never calls
  out. Its Gemini client hands each request to the gateway instead
  (`demos/conference/internal/workload/relay`): `/api/chat` answers with a
  `model_call` step, the gateway makes the call and posts the result to the
  actor's `/api/model-result` through the router, and repeats until the
  actor answers with its reply, at most four times per request
  (`demos/conference/cmd/conf-gateway/internal/actorproxy/proxy.go`). Call
  IDs are random and single-use, and the gateway routes no
  `/api/model-result` for attendees.
- Gateway to ateapi: the gateway shells out to `kubectl-ate` against
  `ATEAPI_ENDPOINT=api.ate-system.svc:443` to read actor state and to issue
  suspends (`demos/conference/cmd/conf-gateway/internal/actorstate`).

## Credentials

- Actors hold no model credential. `demos/conference/conference-template.yaml.tmpl`
  sets `VERTEX_PROJECT`/`VERTEX_LOCATION` on the actor only so it can build
  the request; there is no API key or service-account key in the actor's
  environment.
- On every proxied chat/approve request, the gateway strips the client's
  `Authorization`, `Cookie` and any client-supplied model-key header, then
  sets its own `X-Conf-Model-Key` header to a value scoped to that one actor:
  `<actor>.<base64 HMAC(HMAC_SECRET, "model-key/" + atespace + "/" + actor)>`
  (`demos/conference/cmd/conf-gateway/internal/actorproxy/key.go`). The
  `model-key/` prefix domain-separates it from the HMAC that signs attendee
  session tokens, so the same `HMAC_SECRET` cannot be replayed between the
  two uses.
- The actor's workload server remembers the latest key it was sent and
  accepts a history clear only with that key. The key carries no model
  access.
- The gateway checks each model call against an allowlist (the configured
  project, location and models, `generateContent` only, a body size cap and
  a per-actor in-flight limit) before making it
  (`demos/conference/cmd/conf-gateway/internal/modelproxy/proxy.go`).
- The gateway calls Vertex AI with its own Workload
  Identity: the `conf-gateway` `ServiceAccount` is annotated with a GCP
  service account that holds `roles/aiplatform.user` only
  (`hack/install-demo-conference.sh`'s `demo-conference_ensure_gsa`,
  `demos/conference/manifests/gateway.yaml.tmpl`). No provider credential
  ever reaches an actor, and none is captured in a snapshot.

## Deploy

Required environment: `BUCKET_NAME` (GCS bucket for actor snapshots),
`KO_DOCKER_REPO`. Optional: `PROJECT_ID` (enables Vertex AI; unset runs in
mock mode with `DEMO_MOCK_LLM=true`), `VERTEX_LOCATION` (default
`us-central1`), `DEMO_DOMAIN` (fronts the gateway with a TLS Ingress). See
`hack/ate-dev-env.sh.example` for how these are normally set for a dev
cluster.

```bash
hack/install-ate.sh --patch-router --deploy-demo-conference
```

`--patch-router` must come **before** `--deploy-demo-conference`:
`install-ate.sh` processes flags in the order given, so a `--patch-router`
after the deploy flag has already started has no effect
(`hack/install-demo-conference.sh`). Setting `DEMO_CONF_PATCH_ROUTER=true` in
the environment works too and is order-independent. The standalone wrapper
`hack/install-demo-conference.sh --deploy --patch-router` reorders its flags
for you before calling `install-ate.sh`, so either order works there.

`atenet-router` needs `--parked-request-budget` at least 60s, any
`--route-timeout` at least 60s, and `terminationGracePeriodSeconds` at least
120 to survive an LLM turn and drain safely; `--patch-router` applies all
three (`demo-conference_check_router` in `hack/install-demo-conference.sh`).
Without it, the deploy fails with the specific changes needed instead of
silently deploying a router that will 503 or get killed mid-drain.

Deleting the demo (`--delete-demo-conference`) does not revert these router
flags, and does not delete the `conf-gateway` GCP service account or its IAM
bindings — the install output prints the exact `gcloud` commands to remove
them (`demo-conference_delete` in `hack/install-demo-conference.sh`).

## Presenter access

```bash
kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80
open "http://localhost:8090/stage?key=$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
```

The `key` query parameter is exchanged for an `HttpOnly` cookie
(`conf_presenter`) and removed from the URL, so later visits to `/stage` and
`/feed` in the same browser need no key. Scripts and `curl` can send the same
key as an `X-Presenter-Key` header instead
(`demos/conference/cmd/conf-gateway/internal/auth/auth.go`).

## What the badges mean

Every badge comes from an `ActorView` the gateway reads from ateapi through
`kubectl-ate`. The shared mapping lives in
`demos/conference/cmd/conf-gateway/static/actorview.js`:

| View | Badge label | Sub | Class |
|---|---|---|---|
| `never_started` | Never started | No snapshot yet | `never-started` |
| SUSPENDED + `snapshot_uri` | Suspended | Snapshot saved | `suspended` |
| RESUMING | Resuming | Restoring on a worker | `resuming` |
| RUNNING | Running | `worker_pod` | `running` |
| SUSPENDING | Suspending | Writing snapshot | `suspending` |
| PAUSING / PAUSED | Pausing / Paused | | `paused` |
| CRASHED | Crashed | `state_detail` | `crashed` |
| DELETING / REVERTING | Deleting / Reverting | | `transition` |
| UNAVAILABLE | State unavailable | `state_detail` | `unavailable` |
| UNKNOWN / other | Unknown | `state_detail` | `unknown` |

"State unavailable" means the gateway could not read the actor's state from
ateapi (for example `kubectl-ate` failed, or the gateway is running with
`LOCAL_MODE=true`); it is not a claim about the actor itself.

A separate activity chip shows what the actor is doing right now: Thinking,
Awaiting approval, or Executing, with nothing shown when idle.

The stage's fleet grid draws one badge per actor the gateway reports. When
an actor has an activity, the activity is the badge's label and color and
the substrate state moves to the line below, so an actor waiting on a human
shows a pulsing purple "Awaiting approval" over "Suspended". No substrate
state says an actor is waiting for a human; while it waits, ateapi reports
it Suspended. A CRASHED or UNAVAILABLE actor shows a red badge, whatever its
activity. The legend lists only the states the demo produces: it never
pauses, reverts or deletes a running actor.

A reply may
also carry a source tag: "Mock reply" or "Mock (model error)"; a real
Gemini reply carries no tag.

## Timing honesty

- Actor state comes from polling every `STATE_POLL_INTERVAL` (default 1s)
  plus the `kubectl-ate` call time on top of that, so a short-lived
  transition such as RESUMING can be missed between two polls
  (`demos/conference/cmd/conf-gateway/main.go`,
  `demos/conference/cmd/conf-gateway/internal/actorstate`).
- Parked-request counts are summed across every ready `atenet-router` pod
  (discovered from its EndpointSlices) on each telemetry tick, not read from
  a single router instance
  (`demos/conference/cmd/conf-gateway/internal/routerstatus`).

## Limits and risks

- One gateway replica: attendee sessions, pending approvals and the feed's
  event history all live in its memory
  (`demos/conference/manifests/gateway.yaml.tmpl` runs `replicas: 1`), so a
  restart drops them all.
- Plain HTTP end to end unless `DEMO_DOMAIN` is set to front the gateway
  with a TLS Ingress.
- An actor can make the gateway spend Vertex AI quota, within the allowlist,
  up to four calls per attendee request and two in flight at a time.
- A relayed turn holds its gateway request open for every model call it
  makes, bounded by the actor's 60s turn timeout.
- `SUSPEND_AFTER` defaults to `0`: the gateway asks Substrate to suspend the
  actor immediately after each turn completes. A positive duration delays
  that suspend, and starting a new turn cancels a pending one
  (`demos/conference/cmd/conf-gateway/internal/gateway/turns.go`).
- Deleting the demo does not revert the `atenet-router` flags `--patch-router`
  applied, and does not remove the `conf-gateway` GCP service account or its
  IAM bindings; see Deploy above.

## Local development

Run the full local check, which builds the gateway and a local router and
runs all 13 verification steps against them, without a cluster:

```bash
hack/verify-demo-conference.sh --mock-mode
```

To run the pieces by hand: `demos/conference/cmd/conf-localrouter` builds
one real actor workload server per attendee behind a router that limits
concurrency to `--slots` in-flight requests (parking the rest) and serves
`/statusz`:

```bash
go run ./demos/conference/cmd/conf-localrouter --addr=127.0.0.1:18080 --slots=2 &

PORT=18090 PRESENTER_TOKEN=dev \
LOCAL_MODE=true DEMO_MOCK_LLM=true \
ROUTER_URL=http://127.0.0.1:18080 STATUS_URL=http://127.0.0.1:18080 \
ROUTER_STATUS_DISCOVERY=static \
go run ./demos/conference/cmd/conf-gateway
```

`LOCAL_MODE=true` skips ateapi entirely, so every actor shows "State
unavailable"; `DEMO_MOCK_LLM=true` forces the deterministic mock reasoning
engine instead of calling Vertex AI; `ROUTER_STATUS_DISCOVERY=static` polls
`STATUS_URL` directly instead of discovering router pods from
EndpointSlices, which only exist in a real cluster. Open
`http://localhost:8090/stage?key=dev`, `/feed` and `/chat`.

## Teardown

```bash
hack/install-ate.sh --delete-demo-conference
```

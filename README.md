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

# Interactive Conference Agent Swarm Demo

## Prerequisites

- A GKE cluster running [Substrate](https://github.com/agent-substrate/substrate)
  at the version pinned in `go.mod` (currently the `v0.4.0` release).
  `hack/deploy.sh` refuses to deploy against a cluster running any other
  version; see "Bumping Substrate" below to move the pin. Substrate needs
  GKE 1.37 or later, which some zones offer only in the RAPID channel.
  `hack/create-cluster.sh` creates one:

      PROJECT_ID=my-project CLUSTER_NAME=substrate-poc hack/create-cluster.sh

  It takes the newest 1.37 release in RAPID and adds a maintenance exclusion
  against node and minor upgrades until the version's end of support, since a
  node upgrade recreates the worker pods mid-demo. Control-plane patches still
  apply. It reads the variable names Substrate's `tools/setup-gcp` writes to
  `.ate-dev-env.sh` (`CLUSTER_LOCATION`, `NODE_MACHINE_TYPE`, ...), so you
  can source that file instead; `--help` lists them and `--dry-run` prints
  the gcloud commands. Run on an existing cluster, it adds the exclusion if
  missing.
- One-time project setup: APIs, a snapshot bucket, an image registry and
  IAM bindings. These belong to the project, not the cluster, so they
  survive recreating it. Nothing in this repo creates them. From a
  substrate checkout, with `PROJECT_ID` and `BUCKET_NAME` exported:

      go run ./tools/setup-gcp enable apis     # before hack/create-cluster.sh
      go run ./tools/setup-gcp create bucket   # the snapshot bucket, BUCKET_NAME
      go run ./tools/setup-gcp create iam      # after the cluster exists

  Don't run `setup-gcp bootstrap`: it also creates a cluster, and it can't
  choose GKE 1.37. `create iam` grants the node service account
  `roles/artifactregistry.reader` and `roles/storage.objectViewer`, and
  gives atelet and the ate-api-server snapshot bucket access through
  Workload Identity (atelet also gets registry access). Those principals
  live in the cluster's Workload Identity pool, which is why it runs after
  the cluster. The table in substrate's `tools/setup-gcp/README.md` lists
  every binding.

  setup-gcp enables the Artifact Registry API but creates no repository.
  `KO_DOCKER_REPO` must name one in the project, so the node service
  account can pull from it: either `gcr.io/PROJECT_ID/<prefix>`, which
  pushes to the project's `gcr.io` repository (GCP creates it on the first
  push if your account may create repositories), or one you create:

      gcloud artifacts repositories create ate-images --project PROJECT_ID \
        --repository-format docker --location us-west1
      gcloud auth configure-docker us-west1-docker.pkg.dev
      export KO_DOCKER_REPO=us-west1-docker.pkg.dev/PROJECT_ID/ate-images

  For Vertex AI replies (`PROJECT_ID` set for `hack/deploy.sh`), also run
  `gcloud services enable aiplatform.googleapis.com`; setup-gcp doesn't
  enable it. `hack/deploy.sh` creates the `conf-gateway` service account
  and its Vertex AI binding itself.
- `go` 1.27, `kubectl`, `ko`, `docker buildx`, `jq`, `shellcheck` (for
  `make verify`), and `gcloud` (only needed for Vertex AI).
- Environment variables: `KO_DOCKER_REPO` and `BUCKET_NAME` are required.
  `PROJECT_ID` is optional and enables Vertex AI; leave it unset to run with
  mock replies. Also optional: `VERTEX_LOCATION`, `DEMO_DOMAIN`,
  `DEMO_CONF_ACTORS` (default 50), `DEMO_CONF_MIN_WORKERS` (default 2) and
  `DEMO_CONF_MAX_WORKERS` (default 5), the `conf-workers` autoscaling range,
  `DEMO_CONF_ACTORS_PER_WORKER` (default 4, the actors each worker hosts at
  once),
  `SUBSTRATE_REPO` (a clean substrate checkout
  at the pin; `hack/deploy.sh` installs Substrate from it when the
  `ate-system` namespace is missing), `KUBECTL_CONTEXT` (which kube context
  to target; defaults to the current one), `DEMO_CONF_ALLOW_DIRTY_SUBSTRATE`
  (accept a cluster labeled `<pin>-dirty`; see "Bumping Substrate"), and
  `KO_DEFAULTPLATFORMS=linux/amd64` (skip the arm64 build `ko` does by
  default). `hack/deploy.sh` sources no dev-env file, so export these
  yourself.

## What it shows

50 attendee actors (`attendee-01` .. `attendee-50`, one `conf-agent` actor
each) multiplexed onto a `WorkerPool` of 2 to 5 Kubernetes pods
(`manifests/workerpool.yaml.tmpl`). Each attendee's conversation
history lives in the actor's own process memory
(`internal/workload/server/server.go`); the actor's
`snapshotConfig` captures full content on commit
(`manifests/actortemplate.yaml.tmpl`), so the conversation
survives Substrate suspending and resuming the actor on a different worker
pod. A stage reset, or handing an `attendee-NN` slot to a new visitor, has
the gateway clear that history through the actor's `/api/history/clear`
endpoint.

The `/stage` header is live. The pod count is the `conf-workers` HPA's
current replicas ("3 of 5"), or the WorkerPool's replicas when there is no
HPA, and reads "scaling N → M" while the HPA wants a different size. Each
pod hosts `DEMO_CONF_ACTORS_PER_WORKER` actors at once (4 by default), so
the header also shows the seat count, pods × seats per pod: 5 pods give 20
seats. The oversubscription ratio is the attendee count (`ACTOR_COUNT`, set
from `DEMO_CONF_ACTORS`) divided by the seat count (one decimal below 10x),
next to the number of turns parked at the router. Each worker card lists
its seats and the actor in each. The gateway reads these every 2s
(`cmd/conf-gateway/internal/workerscale`); when it can't, the header shows
`--`.

## Autoscaling

`conf-workers` scales between `DEMO_CONF_MIN_WORKERS` and
`DEMO_CONF_MAX_WORKERS` (2 and 5 by default) on worker demand: workers
hosting at least one actor, plus one worker per `DEMO_CONF_ACTORS_PER_WORKER`
requests parked at the router. A worker counts as busy even with a free
seat, because Substrate reports how many workers are partly or fully
occupied but not how many actors each hosts. A Prometheus in
`ate-demo-conf` scrapes `atenet-router` and `ate-api-server` every 5s,
prometheus-adapter serves the sum as the external metric
`conf_worker_demand`, and an HPA writes the WorkerPool's replicas
(`manifests/autoscaling.yaml.tmpl`). The pool grows within about 20s of a
burst and shrinks 5 minutes after it ends. At the maximum, 5 workers run
up to 20 actors at once. Five workers fit on two c3-standard-4 nodes, so no
node autoscaling is involved.

A worker's seats come from its CPU limit, which `hack/deploy.sh` sets to
`DEMO_CONF_ACTORS_PER_WORKER` CPUs plus 500m, so it fits that many 1-CPU
actors and not one more. The limit is not a reservation: the sandboxes
share the node's CPU, and 4 busy actors on one worker are slower than 4 on
separate workers.

Watch it with:

    kubectl -n ate-demo-conf get hpa conf-workers -w
    kubectl get --raw /apis/external.metrics.k8s.io/v1beta1/namespaces/ate-demo-conf/conf_worker_demand

The install owns the cluster's `v1beta1.external.metrics.k8s.io` APIService
and refuses to run if another adapter (for example the Stackdriver adapter)
already serves it.

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
  in `manifests/gateway.yaml.tmpl`) with an
  `ate-target-actor` header identifying the attendee's actor
  (`cmd/conf-gateway/internal/actorproxy/proxy.go`).
- Actor to model: actors run with egress denied, so the actor never calls
  out. Its Gemini client hands each request to the gateway instead
  (`internal/workload/relay`): `/api/chat` answers with a
  `model_call` step, the gateway makes the call and posts the result to the
  actor's `/api/model-result` through the router, and repeats until the
  actor answers with its reply, at most four times per request
  (`cmd/conf-gateway/internal/actorproxy/proxy.go`). Call
  IDs are random and single-use, and the gateway routes no
  `/api/model-result` for attendees.
- Gateway to ateapi: the gateway shells out to `kubectl-ate` against
  `ATEAPI_ENDPOINT=api.ate-system.svc:443` to read actor state and to issue
  suspends (`cmd/conf-gateway/internal/actorstate`).

## Credentials

- Actors hold no model credential. `manifests/actortemplate.yaml.tmpl`
  sets `VERTEX_PROJECT`/`VERTEX_LOCATION` on the actor only so it can build
  the request; there is no API key or service-account key in the actor's
  environment.
- On every proxied chat/approve request, the gateway strips the client's
  `Authorization`, `Cookie` and any client-supplied model-key header, then
  sets its own `X-Conf-Model-Key` header to a value scoped to that one actor:
  `<actor>.<base64 HMAC(HMAC_SECRET, "model-key/" + atespace + "/" + actor)>`
  (`cmd/conf-gateway/internal/actorproxy/key.go`). The
  `model-key/` prefix domain-separates it from the HMAC that signs attendee
  session tokens, so the same `HMAC_SECRET` cannot be replayed between the
  two uses.
- The actor's workload server remembers the latest key it was sent and
  accepts a history clear only with that key. The key carries no model
  access.
- The gateway checks each model call against an allowlist (the configured
  project, location and models, `generateContent` only, a body size cap and
  a per-actor in-flight limit) before making it
  (`cmd/conf-gateway/internal/modelproxy/proxy.go`).
- The gateway calls Vertex AI with its own Workload
  Identity: the `conf-gateway` `ServiceAccount` is annotated with a GCP
  service account that holds `roles/aiplatform.user` only
  (`hack/deploy.sh`'s `conf_ensure_gsa`,
  `manifests/gateway.yaml.tmpl`). No provider credential
  ever reaches an actor, and none is captured in a snapshot.

## Deploy

Required environment: `BUCKET_NAME` (GCS bucket for actor snapshots),
`KO_DOCKER_REPO`. Optional: `PROJECT_ID` (enables Vertex AI; unset runs in
mock mode with `DEMO_MOCK_LLM=true`), `VERTEX_LOCATION` (default
`us-central1`), `DEMO_DOMAIN` (fronts the gateway with a TLS Ingress). See
"Prerequisites" above for the full list.

```bash
make deploy DEPLOY_FLAGS=--patch-router
```

`make deploy` runs `hack/deploy.sh up $(DEPLOY_FLAGS)`; leave `DEPLOY_FLAGS`
unset to deploy without patching the router, or set
`DEMO_CONF_PATCH_ROUTER=true` in the environment instead, which has the same
effect regardless of any flags passed.

`hack/deploy.sh` sources no dev-env file: it targets `KUBECTL_CONTEXT` if
set, otherwise the current kube context, and logs which one at the start of
`up` and `down`. `ko` builds for both `linux/amd64` and `linux/arm64` by
default; set `KO_DEFAULTPLATFORMS=linux/amd64` to skip the arm64 build.

`atenet-router` needs `--parked-request-budget` at least 60s, any
`--route-timeout` at least 60s, and `terminationGracePeriodSeconds` at least
120 to survive an LLM turn and drain safely; `--patch-router` sets
`--parked-request-budget=60s`, `--route-timeout=5m` and the grace period to
120 (`conf_check_router` in `hack/deploy.sh`).
Without it, the deploy fails with the specific changes needed instead of
silently deploying a router that will 503 or get killed mid-drain.

`make undeploy` (`hack/deploy.sh down`) does not revert these router
flags, and does not delete the `conf-gateway` GCP service account or its IAM
bindings — its output prints the exact `gcloud` commands to remove
them (`conf_delete` in `hack/deploy.sh`).

## Presenter access

```bash
kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80
open "http://localhost:8090/stage?key=$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
```

The `key` query parameter is exchanged for an `HttpOnly` cookie
(`conf_presenter`) and removed from the URL, so later visits to `/stage` and
`/feed` in the same browser need no key. Scripts and `curl` can send the same
key as an `X-Presenter-Key` header instead
(`cmd/conf-gateway/internal/auth/auth.go`).

## What the badges mean

Every badge comes from an `ActorView` the gateway reads from ateapi through
`kubectl-ate`. The shared mapping lives in
`cmd/conf-gateway/static/actorview.js`:

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
  (`cmd/conf-gateway/main.go`,
  `cmd/conf-gateway/internal/actorstate`).
- Parked-request counts are summed across every ready `atenet-router` pod
  (discovered from its EndpointSlices) on each telemetry tick, not read from
  a single router instance
  (`cmd/conf-gateway/internal/routerstatus`).

## Limits and risks

- One gateway replica: attendee sessions, pending approvals and the feed's
  event history all live in its memory
  (`manifests/gateway.yaml.tmpl` runs `replicas: 1`), so a
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
  (`cmd/conf-gateway/internal/gateway/turns.go`).
- Deleting the demo does not revert the `atenet-router` flags `--patch-router`
  applied, and does not remove the `conf-gateway` GCP service account or its
  IAM bindings; see Deploy above.
- The router's parking gauge has no pool label, and `atenet-router` is
  shared with every pool on the cluster. Parked requests for other demos
  (`ate-demo-swarm`, `workshop`) also scale `conf-workers`, capped at
  `DEMO_CONF_MAX_WORKERS`.

See also `substrate-gaps.md` for the Substrate limitations this demo works
around.

## Development

```bash
make test        # go test ./... plus the deploy-script unit tests
make verify      # gofmt, go vet, lint, mod tidy diff, boilerplate, manifests, shellcheck
make run-local   # gateway + local router, mock LLM replies, no cluster needed
make verify-local # builds the gateway and router and runs the full check against them
make e2e         # the same check against a real deployed cluster
```

`make run-local` builds one real actor workload server per attendee behind
`cmd/conf-localrouter`, a local router that limits concurrency to `--slots`
in-flight requests (parking the rest) and serves `/statusz`, then starts the
gateway against it with `LOCAL_MODE=true` (skips ateapi entirely, so every
actor shows "State unavailable") and `DEMO_MOCK_LLM=true` (the deterministic
mock reasoning engine instead of a Vertex AI call). Open
`http://127.0.0.1:18090/chat`; the presenter URL, with its key, is printed in
the gateway's log output.

`make verify-local` runs `hack/verify.sh --mock-mode`, which builds the same
two pieces and runs all 13 verification steps against them without a
cluster. `make e2e` runs the identical `hack/verify.sh` against a real
deployed cluster; it needs a port-forward to `svc/conf-gateway` and a
`PRESENTER_TOKEN` from the gateway's Secret first:

```bash
kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80 &
export PRESENTER_TOKEN="$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
```

## Bumping Substrate

`go.mod` pins substrate at a release tag, or at a commit (a pseudo-version)
between releases. `hack/deploy.sh` compares every cluster node's
`ate.dev/substrate-version` label against that pin and refuses to deploy on a
mismatch. Substrate stamps that label with `git describe --tags` of the
checkout it was installed from, so install from a clean checkout of exactly
the pinned version: a tag pin needs a label equal to the tag (`v0.4.0`), and
a commit pin accepts the commit or the describe form after a tag
(`v0.4.0-5-g1a2b3c4d`). To move the pin, for example to the next release:

```bash
go get -tool github.com/agent-substrate/substrate/cmd/kubectl-ate@<tag-or-commit>
go get -tool github.com/agent-substrate/substrate/cmd/ateom-gvisor@<tag-or-commit>
```

then copy substrate's `go` directive and its `k8s.io/apimachinery` `replace`
into this repo's `go.mod`, run `go mod tidy`, and update the `substrate_pin`
assertion in `hack/test-deploy.sh`. Check the release's
ActorTemplate and WorkerPool API changes against `manifests/` (bump
`DEMO_CONF_TEMPLATE_REVISION` if the template changes), then reinstall
Substrate on the cluster from a checkout at the new pin.

## Teardown

```bash
make undeploy
```

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

# Substrate gaps this demo works around

This demo runs against upstream Substrate as-is, but it has to work around a
few gaps to do it. Each is listed here with its symptom, the workaround in
this repo, and where the real fix belongs.

## `atenet-router`'s defaults are too short for LLM turns

`atenet-router`'s default `--parked-request-budget`, `--route-timeout` and
termination grace period are sized for short-lived requests, not multi-second
LLM turns; deployed as-is, the router either 503s a parked request or gets
SIGKILLed mid-drain during a rollout. `hack/deploy.sh` checks these values
before deploying and, with `--patch-router`, raises them; see the thresholds
and exact values in the README's "Deploy" section. The fix belongs in
`atenet-router`'s own defaults, or in a Substrate-level profile for
long-running backends, so a deploy that fronts an LLM does not need an
external patch step at all.

## WorkerPools cannot cap how many actors a worker hosts

A worker hosts actors until its capacity runs out or it reaches ateworker's
`--max-actors` (default 1000), and a WorkerPool cannot set that flag. The
capacity comes from the ateworker container's `limits.cpu` and `limits.memory`;
without limits it is the node's allocatable CPU and memory, so a worker on a
4-vCPU node would take as many of this demo's 1-CPU actors as the node has
CPUs. The demo wants exactly 4 actors per worker, so `hack/deploy.sh` sets
the `conf-workers` CPU limit to 4500m, which fits four actors but not five,
and leaves memory unlimited so a sandbox cannot OOM-kill its worker
(`manifests/workerpool.yaml.tmpl`). The seat count is then a side effect of
the actor template's CPU limit: raising an actor to 2 CPUs would quietly
halve it. The fix belongs in the WorkerPool API: a field for the actors per
worker, passed to ateworker as `--max-actors`.

## Worker metrics do not count actors

`ate_workerpool_workers` counts workers by occupancy (`idle`, `partial`,
`at_capacity`, `unschedulable`), and no metric reports how many actors a
worker or pool hosts. The `conf-workers` HPA therefore counts a worker with
one of its 4 seats taken as fully busy
(`manifests/autoscaling.yaml.tmpl`), and scales up earlier than seat usage
calls for. A per-pool gauge of hosted actors, next to the pool's seat
capacity, would let the demand be actors over seats.

## WorkerPools cannot spread their workers across nodes

`WorkerPoolPodTemplate` has no `topologySpreadConstraints` field, so a pool
cannot require its workers to land on different nodes. The demo sets
resource requests so the scheduler prefers the emptier node, which is a
preference, not a guarantee. The fix belongs in the WorkerPool API and its
controller, which should pass topology spread constraints through to the
pod template.

## The router's parking gauge has no pool label

`atenet.router.parking.active` counts parked requests per router replica
with no WorkerPool, template, or atespace label, so on a shared router every
consumer sees the whole cluster's queue. `conf-gateway` bounds its reported
`parked_requests` (and the `conf_gateway_parked_requests` gauge it exports on
`:9090/metrics` for the HPA) by the number of initial HTTP requests it
currently has awaiting `atenet-router`, keeping an idle namespace's stage UI
and autoscaler unaffected by bursts in sibling namespaces. Two namespaces
bursting simultaneously can still over-count up to each gateway's own
in-flight requests (capped by `DEMO_CONF_MAX_WORKERS` in
`manifests/autoscaling.yaml.tmpl`). The real fix belongs in `atenet-router`'s
metrics, which should label parked requests with the target atespace/template
or pool.

## Interrupted `SuspendActor` calls leave actors wedged in `SUSPENDING`

`ate-api-server` executes `SuspendActor` on the caller's gRPC request context
even after committing `ACTOR_STATE_SUSPENDING` to PostgreSQL and starting
`atelet.Checkpoint`. If the caller disconnects mid-checkpoint (for example
during a `conf-gateway` rollout), `atelet` finishes checkpointing and tears
down the sandbox, while `ate-api-server` aborts before detaching volumes,
releasing the worker seat, or finalizing `ACTOR_STATE_SUSPENDED` — and neither
`ResumeActor` nor `ReportActorSnapshotState` accepts an actor in `SUSPENDING`.
`conf-gateway` works around this in three ways: draining in-flight `Suspend`
calls before canceling them on shutdown, including `SUSPENDING` actors in
`POST /api/reset`, and automatically re-driving Substrate's idempotent
`SuspendActor` when an idle actor remains in `SUSPENDING` for more than 20s.
The fix belongs in `ate-api-server`, which should detach its post-commit
suspend workflow from the caller's context or reconcile orphaned `SUSPENDING`
actors in `ate-controller`.

## Canceled `ResumeActor` calls orphan `RUNNING` actors that `WorkerPool` scale-down crashes

When a client disconnects from `atenet-router` while an actor is still waking
(~3s `ResumeActor` restore), `atenet-router` aborts the HTTP request (`request
for actor ... canceled by client`), and `conf-gateway`'s immediate post-turn
`SuspendActor` call fails because `ate-api-server` still holds the actor's
operation lease in `ACTOR_STATE_RESUMING`. Moments later, `ate-api-server`
finishes `ResumeActor` anyway and leaves the actor stranded in
`ACTOR_STATE_RUNNING` with no turn running. When the `WorkerPool` HPA later
scales down, Kubernetes deletes a `ReplicaSet` worker pod without draining or
suspending hosted actors first, and `ate-api-server`'s `DeleteWorker` workflow
marks any still-bound `RUNNING` actor `ACTOR_STATE_CRASHED` (`"worker pod went
away while hosting the actor"`). `conf-gateway` works around this by
automatically suspending any actor that remains in `RUNNING` for more than 20s
with no local turn, auto-suspend timer, history clear, or suspend in flight.
The real fixes belong in `ate-api-server` / `atenet-router` (propagating client
cancellation or idle-timeout auto-suspend for woken actors) and in the
`WorkerPool` controller (preferring empty workers on scale-down or suspending
hosted actors in a `preStop` / finalizer drain before deleting a worker).

## Per-checkpoint STS token exchanges hit the 100 QPS project quota

In `ate-api-server`, `workflow_suspend.go` and `workflow_resume.go` call
`gcsboundary.MintCredentials` (`sts.googleapis.com/v1/token`) on every actor
checkpoint upload (`SuspendActor`) and every restore of a checkpointed actor
(`ResumeActor` when `owner == 1` under `/actors/<uid>/snapshots/<snap_uuid>`).
First-wake restores of `NeverStarted` actors (`owner == 2` under `/tags/...`)
hit the node-local `/var/lib/ate/image-cache` and do not call STS after the
first pull per node, but every per-actor checkpoint upload and restore makes a
fresh synchronous `ExchangeToken` RPC. On Google Cloud, `sts.googleapis.com`
enforces a project-wide `Token exchange requests` quota of 6,000 RPM (100 QPS),
shared with `gke-metadata-server` Workload Identity across all nodes in the
project. Exceeding ~85 combined checkpoint uploads and restores per second returns
`HTTP 429 RATE_LIMIT_EXCEEDED` from STS, which aborts `SuspendActor` and
`ResumeActor` with `codes.Internal: preparing storage credentials` and can
starve Workload Identity token refreshes on `conf-gateway`. `conf-gateway`
works around this in hyper mode by pacing `SuspendActor` RPCs to ~66.7 QPS,
pacing checkpointed `ResumeActor` wakes to <=8 QPS, and pre-warming + caching
its own Google Cloud OAuth2 token source with retry backoff. The fix belongs in
`ate-api-server`'s `gcsboundary` / `objectstore-plugin`, which should either
sign GCS V4 URLs locally using the control plane's service account key or cache
and reuse prefix-scoped Credential Access Boundary tokens per worker or bucket
prefix across their 1-hour lifetime instead of calling `sts.googleapis.com` on
every actor state transition.

## `gcsboundary` Credential Access Boundaries fail on Hierarchical Namespace buckets

When the snapshot GCS bucket has Hierarchical Namespace (`HNS`) enabled,
uploading a snapshot tar object to
`gs://<bucket>/actors/<uid>/snapshots/<snap_uuid>` implicitly creates or
updates managed folder resources (`projects/_/buckets/<bucket>/folders/...`).
`gcsboundary` mints an STS Credential Access Boundary (CAB) rule that grants
`roles/storage.objectAdmin` conditioned on the resource prefix
`projects/_/buckets/<bucket>/objects/actors/<uid>/snapshots/`, which does not
match `.../folders/...` resources. `atelet`'s `SuspendActor` upload then fails
with `HTTP 403 PermissionDenied` from GCS. `hack/create-cluster.sh` and this
demo's bucket setup use flat (non-HNS) regional buckets as a workaround. The
fix belongs in `ate-setup` (refusing or warning on HNS buckets during setup)
and in `gcsboundary` (adding folder resource prefixes or switching to V4 signed
URLs).

## `ListActors` and `ListWorkerActorAssignments` do not scale to $10^7$ actors

`ListActors` caps `page_size` at 1,000 rows (`maxPageSize = 1000`), so listing
a 16,777,216-actor atespace requires 16,778 sequential paginated RPCs (~40–60s
per sweep) and allocates tens of millions of protobuf objects. At >262,144
actors (`HyperFleetThreshold`), `conf-gateway` instead polls `ListWorkers` (5
pages across 4,096 workers) followed by 4,096 bounded-concurrency
`ListWorkerActorAssignments` RPCs (~620ms per sweep) to observe which actors
are seated on worker pods. However, `WorkerActorAssignment` only returns the
actor's `ObjectRef` (`atespace`, `name`, `uid`) without its `ActorState`
(`RESUMING`, `RUNNING`, `SUSPENDING`, `CRASHED`) or `latest_snapshot_generation`.
`conf-gateway` therefore has to track in-flight `RESUMING` and `SUSPENDING`
transitions and checkpoint existence in its own 16 MiB compact byte board
(`BeginHyperResume`, `BeginHyperSuspend`, `FinishHyperSuspend`). The fix
belongs in `ateapipb`: including `ActorState` and snapshot metadata in
`WorkerActorAssignment`, exposing an `Atespace` summary RPC for aggregate state
and snapshot counts, and providing a watch/delta stream (`WatchActors`) for
state transitions since a revision or timestamp.

## Provisioning $10^7$ actors requires $10^7$ unary `CreateActor` RPCs

`ateapipb` only provides a unary `CreateActor` RPC, which runs a separate
database transaction per actor. Provisioning 16,777,216 actors therefore
requires 16,777,216 unary RPCs (~28 minutes across a 64-shard Indexed Job with
3,072 concurrent gRPC streams in `cmd/conf-provision`). A `BatchCreateActors`
RPC accepting up to 500–1,000 actors per call and executing a single batched
PostgreSQL `INSERT` / `COPY` would reduce 16.78M actor creation to ~16,778
RPCs (<90 seconds).

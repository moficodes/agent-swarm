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

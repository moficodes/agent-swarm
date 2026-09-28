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

## The actor rootfs overlay is not traversable by a non-root user

Substrate creates the actor rootfs overlay 0700, so a container that drops to
a non-root user cannot traverse it. The `conf-workload` container needs a
non-root workload user for the model-generated code it executes, but cannot
have one; instead it runs as root inside gVisor and grants itself
`CHOWN`, `SETUID`, `SETGID` and `DAC_OVERRIDE` so its executor can `setuid`
down to `nobody` per sandboxed run
(`manifests/actortemplate.yaml.tmpl`). The fix belongs in substrate's atelet
([`cmd/atelet/oci.go`](https://github.com/agent-substrate/substrate/blob/v0.2.0/cmd/atelet/oci.go),
which builds the overlay) and ateom
([`internal/imagecache/bundle_linux.go`](https://github.com/agent-substrate/substrate/blob/v0.2.0/internal/imagecache/bundle_linux.go),
which lays out the bundle), so the overlay is traversable by whatever user
the image specifies.

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

A worker hosts actors until its capacity runs out or it reaches ateom's
`--max-actors` (default 1000), and a WorkerPool cannot set that flag. The
capacity comes from the ateom container's `limits.cpu` and `limits.memory`;
without limits it is the node's allocatable CPU and memory, so a worker on a
4-vCPU node would take as many of this demo's 1-CPU actors as the node has
CPUs. The demo wants exactly 4 actors per worker, so `hack/deploy.sh` sets
the `conf-workers` CPU limit to 4500m, which fits four actors but not five,
and leaves memory unlimited so a sandbox cannot OOM-kill its worker
(`manifests/workerpool.yaml.tmpl`). The seat count is then a side effect of
the actor template's CPU limit: raising an actor to 2 CPUs would quietly
halve it. The fix belongs in the WorkerPool API: a field for the actors per
worker, passed to ateom as `--max-actors`.

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
with no WorkerPool or template label, so on a shared router one pool's
autoscaler sees every pool's queue. The demo accepts this and caps the
effect with `DEMO_CONF_MAX_WORKERS` (`manifests/autoscaling.yaml.tmpl`).
The fix belongs in atenet-router's metrics, which should label parked
requests with the target pool.

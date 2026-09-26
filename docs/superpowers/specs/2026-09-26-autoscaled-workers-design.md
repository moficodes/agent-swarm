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

# Autoscaling the worker pool and live fleet counts

## Goal

With 50 attendee actors on 2 worker pods, the demo shows oversubscription
well but turns slow once the room joins in: only 2 turns can run at once
and everyone else waits in the router's parking lot. Let the `conf-workers`
pool grow from 2 to 5 pods while the audience is active and shrink back
when it goes quiet, and make the `/stage` header show it happening.

This also delivers roadmap item 2, "Live fleet counts", from
`2026-09-24-demo-script-failures-design.md`: the header's "2 Physical
Worker Pods" and "50x Oversubscription" stop being fixed copy.

The pattern follows Substrate's
[`demos/autoscaled-workerpool`](https://github.com/agent-substrate/substrate/tree/main/demos/autoscaled-workerpool)
(Prometheus, prometheus-adapter, an HPA writing `WorkerPool.spec.replicas`),
adapted for GKE and for a demand signal that suits this demo.

## Current behavior

- `manifests/workerpool.yaml.tmpl` fixes `conf-workers` at `replicas: 2`
  with no pod template. `hack/deploy.sh` applies it with client-side
  `kubectl apply` on every run.
- Each worker holds one actor. The gateway suspends an actor as soon as
  its turn ends (`SUSPEND_AFTER` defaults to 0), so a worker is busy only
  for the resume, the model round trip and the suspend.
- Metrics leave the cluster only through OTLP to `gke-managed-otel` and
  land in Cloud Monitoring 1-2+ minutes later. There is no in-cluster
  Prometheus and no external metrics API (`substrate-poc` has only
  `v1beta1.metrics.k8s.io` from metrics-server).
- `atenet-router` and `ate-api-server` already serve Prometheus text on
  `:9090/metrics`, with `prometheus.io/scrape` annotations. On the live
  cluster that includes `atenet_router_parking_active` (gauge) and
  `ate_workerpool_workers{ate_worker_state="idle"|"assigned", ...}`.
- The gateway already polls the router's `/statusz` and puts
  `parked_requests` in the stage telemetry
  (`cmd/conf-gateway/internal/gateway/telemetry.go`); the stage shows it
  in the parking meter. Worker cards list only workers holding an actor.
- `substrate-poc` runs 2 × c3-standard-4 with no node autoscaling.

### Verified on `substrate-poc` (2026-09-26)

Scaling `conf-workers` to 3 put the third pod on a node that already had
a worker. It was Ready in about 1s, showed up in `ate_workerpool_workers`
as idle, and took an actor while another actor ran on the other worker on
the same node; both answered through the router in about 0.25s. Several
workers per node work, so a ceiling of 5 needs no node autoscaling. Each
worker reports the whole node's CPU and memory as its capacity (see
"Substrate gap" below).

## Design

### Scaling signal

The HPA scales on worker demand: turns running plus turns waiting.

```
conf_worker_demand =
    max(ate_workerpool_workers{ate_workerpool_namespace="ate-demo-conf",
                               ate_workerpool_name="conf-workers",
                               ate_worker_state="assigned"})
  + sum(atenet_router_parking_active)
```

Parked requests alone do not work here. An External `AverageValue` HPA
sets `replicas = ceil(metric / target)`, so as soon as new pods drain the
queue, parked drops to 0 and the HPA asks for the minimum again; the pool
would swing up and down during a burst. Busy workers alone saturate at
the current replica count and cannot say how many are waiting. The sum
counts both, falls only when turns stop, and cannot stick high because
workers free up after every turn.

The busy term uses `max` because every `ate-api-server` replica (two on
`substrate-poc`) reports the same pool-wide count; the parked term uses
`sum` because each router replica counts only its own parked requests.

`atenet_router_parking_active` carries no pool label. That is acceptable
because only this demo sends traffic through the router on these
clusters.

### Monitoring stack

New file `manifests/autoscaling.yaml.tmpl`, applied by `hack/deploy.sh`
after the WorkerPool:

- **Prometheus** (Deployment `conf-prometheus`, 1 replica, namespace
  `ate-demo-conf`, `emptyDir` storage, `--storage.tsdb.retention.time=2h`).
  One scrape job with pod discovery in `ate-system`, keeping pods labeled
  `app=atenet-router` or `app=ate-api-server` and scraping port 9090 every
  5s. A ServiceAccount with a Role in `ate-system` allowing `get`, `list`
  and `watch` on pods. A ClusterIP Service for the adapter.
- **prometheus-adapter** (Deployment `conf-prometheus-adapter`, namespace
  `ate-demo-conf`) registered as `APIService v1beta1.external.metrics.k8s.io`,
  with the standard auth-delegator and extension-apiserver-authentication
  bindings. One external rule publishing `conf_worker_demand` from the
  query above.
- **HPA** `conf-workers` in `ate-demo-conf`, `scaleTargetRef` the
  WorkerPool (`apiVersion: ate.dev/v1alpha1`, `kind: WorkerPool`),
  `minReplicas: ${MIN_WORKERS}`, `maxReplicas: ${MAX_WORKERS}`, one
  External metric `conf_worker_demand` with `target.averageValue: 1`.
  `behavior`: scale-up with no stabilization window, `selectPolicy: Max`,
  policies Percent 100 and Pods 4 per 15s; scale-down with a 300s
  stabilization window.

Image versions for Prometheus and prometheus-adapter are pinned by tag
in the template.

Reaction time: 5s scrape plus up to 15s HPA sync (not configurable on
GKE), so about 20s from a burst to a new desired count. A new worker was
Ready within about 1s in the experiment.

The APIService is cluster-scoped. `deploy.sh` refuses to install when
`v1beta1.external.metrics.k8s.io` already exists and points at something
other than `ate-demo-conf/conf-prometheus-adapter`, rather than taking
over another adapter.

### WorkerPool changes

- `replicas` becomes `${WORKER_REPLICAS}`. `deploy.sh` sets it to the
  pool's current `spec.replicas` if the pool exists and to `MIN_WORKERS`
  otherwise, so a re-run of `make deploy` does not reset what the HPA
  chose.
- Add a pod template with a topology spread constraint on
  `kubernetes.io/hostname`, `maxSkew: 1`, `whenUnsatisfiable:
  ScheduleAnyway`, so workers alternate across nodes instead of piling
  onto one (the experiment's third pod landed next to an existing one).

New `deploy.sh` settings, documented in the README's prerequisites:
`DEMO_CONF_MIN_WORKERS` (default 2) and `DEMO_CONF_MAX_WORKERS`
(default 5). `deploy.sh` rejects a minimum below 1 or a maximum below the
minimum. `hack/deploy.sh`'s delete path removes the autoscaling manifest
before the WorkerPool.

### Gateway: live pod counts

New package `cmd/conf-gateway/internal/workerscale`, next to
`routerstatus`:

- `Snapshot{Current, Desired, Max int; Available bool}`.
- A poller that every 2s reads `HorizontalPodAutoscaler/conf-workers`
  (`autoscaling/v2`) in the gateway's namespace: `status.currentReplicas`,
  `status.desiredReplicas`, `spec.maxReplicas`. If the HPA does not
  exist, it falls back to `WorkerPool/conf-workers` `spec.replicas`, with
  Desired = Current = Max = that value. On any other error the snapshot is
  unavailable.
- It uses the client-go client `main.go` already builds for EndpointSlice
  discovery, plus a dynamic client for the WorkerPool. In local mode
  (no cluster), it reports unavailable.
- RBAC in `manifests/gateway.yaml.tmpl`: a Role in `ate-demo-conf`
  allowing `get` on `horizontalpodautoscalers` (`autoscaling`) and
  `workerpools` (`ate.dev`).

The telemetry payload gains:

```json
"worker_pods": {"current": 3, "desired": 4, "max": 5, "available": true}
```

### Stage header

`cmd/conf-gateway/static/stage.html` and `stage.js`:

- "**N** Physical Worker Pods" shows `worker_pods.current`, with
  "of MAX" in smaller type.
- "**X**x Oversubscription" shows `ACTOR_COUNT ÷ current`, rounded to a
  whole number (25x at 2 pods, 10x at 5). The actor count comes from the
  existing fleet data.
- When `desired ≠ current`, a line under the pod count reads
  "scaling N → M" (up or down).
- "K waiting" reuses `parked_requests` next to the ratio.
- When `worker_pods.available` is false, the pod count and ratio show
  `--`, as the parking meter already does.

The README's "What it shows" paragraph about fixed header copy is
replaced with how the header is computed, and a short "Autoscaling"
section explains the signal, the 2-5 range and how to watch it
(`kubectl -n ate-demo-conf get hpa conf-workers -w`).

### Substrate gap

Add to `substrate-gaps.md`: each gVisor worker reports the whole node's
allocatable CPU and memory as its capacity, so several workers on one
node each believe they own it. It is harmless at 5 workers with 1-CPU
actors, but Substrate should report the pod's own limits.

## Error handling

- Prometheus or the adapter down: the HPA keeps its last replica count
  and reports `ScalingActive=False`. The demo keeps working at that size.
  The stage header is unaffected because it reads the HPA, not
  Prometheus.
- HPA missing (e.g. an older deploy): the header falls back to the
  WorkerPool's replicas.
- Kubernetes API errors in the gateway: the header shows `--`; nothing
  else on the stage changes.

## Testing

- Go unit tests for `workerscale`: HPA present, HPA missing with the
  WorkerPool fallback, both missing, API error. Telemetry test covering
  `worker_pods` in the payload.
- `hack/test-deploy.sh`: the autoscaling manifest renders with the pool
  name, namespace, min and max; the WorkerPool renders the replicas
  `deploy.sh` chose; invalid min/max are rejected.
- `hack/verify.sh` (boilerplate and manifest checks) passes on the new
  template.
- Live on `substrate-poc`:
  1. `kubectl get --raw` on the external metric returns a value.
  2. Run the audience simulation at 50. The HPA reaches 5 within about
     30s and the header reads "5 of 5 pods" and "10x".
  3. Stop the simulation. The pool returns to 2 about 5 minutes later,
     with "scaling 5 → 2" shown on the way down.
  4. Measure how long a turn holds a worker (from
     `ate_workerpool_workers` and the parking gauge in Prometheus) to
     replace the ~5s estimate used when sizing the ceiling.
  5. Re-run `make deploy` while scaled up; replicas are unchanged.

## Out of scope

- Node autoscaling. Five workers fit on the existing two nodes.
- Scaling past 5, or scaling other pools.
- Sending the demand metric to Cloud Monitoring or adding it to the
  dashboard.
- A presenter control for turning autoscaling on and off.

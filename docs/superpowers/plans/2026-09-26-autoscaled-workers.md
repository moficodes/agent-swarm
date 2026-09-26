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

# Autoscaled Workers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the `conf-workers` pool scale from 2 to 5 pods on busy + parked turns, and have the `/stage` header show the live pod count, ratio, scaling and waiting counts.

**Architecture:** An in-cluster Prometheus in `ate-demo-conf` scrapes `atenet-router` and `ate-api-server` every 5s. prometheus-adapter serves one external metric, `conf_worker_demand`, to an HPA that writes `WorkerPool.spec.replicas`. The gateway polls the HPA (falling back to the WorkerPool) every 2s and adds `worker_pods` and `actor_count` to stage telemetry. `stage.js` renders them.

**Tech Stack:** Go 1.x, client-go v0.37.0 (`kubernetes`, `dynamic`, their fakes, `autoscaling/v2`), bash + jq, Prometheus `prom/prometheus:v3.5.0`, `registry.k8s.io/prometheus-adapter/prometheus-adapter:v0.12.0`, vanilla JS.

**Spec:** `docs/superpowers/specs/2026-09-26-autoscaled-workers-design.md`

## Global Constraints

- Every new file carries the Apache 2.0 "Copyright 2026 Google LLC" header in the file's comment style (`make verify` runs `verify-boilerplate`).
- `make verify` (gofmt, vet, golangci-lint, `go mod tidy -diff`, boilerplate, `hack/verify-manifests.sh`, shellcheck) and `make test` (`go test ./...`, `bash hack/test-deploy.sh`) pass after every task.
- Commits are signed (`git commit -S`) and carry NO `Co-Authored-By` trailer. Subagents must be told this.
- Pool: `ate-demo-conf/conf-workers`. Defaults `DEMO_CONF_MIN_WORKERS=2`, `DEMO_CONF_MAX_WORKERS=5`; reject min < 1 or max < min.
- Demand metric: `(max(ate_workerpool_workers{ate_workerpool_namespace="ate-demo-conf",ate_workerpool_name="conf-workers",ate_worker_state="assigned"}) or vector(0)) + (sum(atenet_router_parking_active) or vector(0))`, published as `conf_worker_demand`, HPA target `AverageValue "1"`.
- HPA behavior: scale-up stabilization 0, `selectPolicy: Max`, Percent 100 / 15s and Pods 4 / 15s; scale-down stabilization 300s.
- WorkerPool pod template requests `cpu: 500m`, `memory: 768Mi`, no limits.
- Telemetry JSON: `"worker_pods": {"current","desired","max","available"}` and `"actor_count"`.
- Header shows `--` for pods and ratio when `worker_pods.available` is false or `current` is 0.
- Anything that touches `substrate-poc` (context `gke_mofilabs_us-west1-c_substrate-poc`) needs the user's go-ahead first.

## Review Focus

1. **A just-created HPA has an empty status** (`currentReplicas` 0). The header should show the WorkerPool's replicas, not `0` or `--`. Pinned by Task 1's "fresh HPA" case.
2. **Zero current pods** (pool at 0, or HPA reports 0 and the pool can't be read). The ratio should read `--`, never `Infinity` or `NaN`. Pinned by Task 4's local-mode browser check and a static-test guard.
3. **Re-running `make deploy` after the HPA scaled up, or lowering `DEMO_CONF_MAX_WORKERS` below the current size.** The pool should keep its size, clamped to [min, max]. Pinned by Task 5's `conf_worker_replicas` cases.
4. **`v1beta1.external.metrics.k8s.io` owned by another adapter.** Deploy should refuse before changing anything, and delete should leave it alone. Pinned by Task 5's `conf_apiservice_ok` cases and the separate APIService manifest.
5. **Gateway RBAC denied or API errors.** The header shows `--` and recovers when reads succeed again; nothing else on the stage changes. Pinned by Task 1's forbidden case and Task 2's available → unavailable transition.

---

### Task 1: `types.WorkerPods` and the `workerscale` reader

**Files:**
- Modify: `pkg/types/types.go:201-211` (StageTelemetry)
- Modify: `pkg/types/types_test.go:281-315` (StageTelemetry round trip)
- Create: `cmd/conf-gateway/internal/workerscale/workerscale.go`
- Test: `cmd/conf-gateway/internal/workerscale/workerscale_test.go`

**Interfaces:**
- Produces:
  - `types.WorkerPods{Current, Desired, Max int; Available bool}` (JSON `current`, `desired`, `max`, `available`)
  - `types.StageTelemetry.WorkerPods types.WorkerPods` (`worker_pods`) and `.ActorCount int` (`actor_count`)
  - `workerscale.New(client kubernetes.Interface, dyn dynamic.Interface, namespace, name string) *workerscale.Reader`
  - `(*workerscale.Reader).Fetch(ctx context.Context) types.WorkerPods`

- [ ] **Step 1: Extend the types round-trip test**

In `pkg/types/types_test.go`, in the `StageTelemetry` subtest, add two fields to the `telemetry` literal after `StatePollMs: 40,`:

```go
			WorkerPods:  types.WorkerPods{Current: 3, Desired: 5, Max: 5, Available: true},
			ActorCount:  50,
```

After the `cmp.Diff` check in the same subtest, add a check on the wire names:

```go
		for _, key := range []string{`"worker_pods":{"current":3,"desired":5,"max":5,"available":true}`, `"actor_count":50`} {
			if !strings.Contains(string(data), key) {
				t.Errorf("StageTelemetry JSON lacks %s: %s", key, data)
			}
		}
```

(`strings` is already imported.)

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./pkg/types/ -run 'TestAuxiliaryTypesSerialization/StageTelemetry' -v`
Expected: compile error `unknown field WorkerPods`.

- [ ] **Step 3: Add the types**

In `pkg/types/types.go`, replace the `StageTelemetry` struct with:

```go
// StageTelemetry is the stage view of the router, workers and actors.
type StageTelemetry struct {
	ParkedRequests   int                  `json:"parked_requests"`
	ParkingAvailable bool                 `json:"parking_available"`
	Workers          []WorkerStatus       `json:"workers"`
	Actors           map[string]ActorView `json:"actors"`
	RecentEvents     []string             `json:"recent_events"`
	MockLLM          bool                 `json:"mock_llm"`
	ModelUsage       ModelUsage           `json:"model_usage"`
	StatePollMs      int64                `json:"state_poll_ms"`
	WorkerPods       WorkerPods           `json:"worker_pods"`
	// ActorCount is how many attendee actors the install created.
	ActorCount int `json:"actor_count"`
}

// WorkerPods is the worker pool's size as its autoscaler sees it.
type WorkerPods struct {
	// Current is how many worker pods the pool has.
	Current int `json:"current"`
	// Desired is how many the autoscaler wants; it equals Current when the
	// pool is not scaling.
	Desired int `json:"desired"`
	// Max is the autoscaler's ceiling.
	Max int `json:"max"`
	// Available is false when the counts could not be read.
	Available bool `json:"available"`
}
```

- [ ] **Step 4: Run the types test and confirm it passes**

Run: `go test ./pkg/types/ -v`
Expected: PASS.

- [ ] **Step 5: Write the failing `workerscale` tests**

Create `cmd/conf-gateway/internal/workerscale/workerscale_test.go`:

```go
// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workerscale_test

import (
	"context"
	"errors"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/workerscale"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const (
	ns   = "ate-demo-conf"
	name = "conf-workers"
)

var poolGVR = schema.GroupVersionResource{Group: "ate.dev", Version: "v1alpha1", Resource: "workerpools"}

func hpa(current, desired, max int32) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: max},
		Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: current, DesiredReplicas: desired},
	}
}

// pool returns a WorkerPool; a negative replicas leaves spec.replicas unset.
func pool(replicas int64) *unstructured.Unstructured {
	spec := map[string]any{}
	if replicas >= 0 {
		spec["replicas"] = replicas
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ate.dev/v1alpha1",
		"kind":       "WorkerPool",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	}}
}

func TestFetch(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "autoscaling", Resource: "horizontalpodautoscalers"}, name, errors.New("denied"))
	tests := []struct {
		name     string
		hpa      *autoscalingv2.HorizontalPodAutoscaler
		pool     *unstructured.Unstructured
		hpaError error
		want     types.WorkerPods
	}{
		{name: "hpa reconciled", hpa: hpa(3, 4, 5), pool: pool(3), want: types.WorkerPods{Current: 3, Desired: 4, Max: 5, Available: true}},
		{name: "hpa steady", hpa: hpa(2, 2, 5), want: types.WorkerPods{Current: 2, Desired: 2, Max: 5, Available: true}},
		{name: "fresh hpa uses pool replicas", hpa: hpa(0, 0, 5), pool: pool(2), want: types.WorkerPods{Current: 2, Desired: 2, Max: 5, Available: true}},
		{name: "fresh hpa without pool", hpa: hpa(0, 0, 5), want: types.WorkerPods{Current: 0, Desired: 0, Max: 5, Available: true}},
		{name: "no hpa falls back to pool", pool: pool(2), want: types.WorkerPods{Current: 2, Desired: 2, Max: 2, Available: true}},
		{name: "no hpa, pool at zero", pool: pool(0), want: types.WorkerPods{Available: true}},
		{name: "no hpa, pool without replicas", pool: pool(-1), want: types.WorkerPods{}},
		{name: "neither exists", want: types.WorkerPods{}},
		{name: "hpa forbidden", pool: pool(2), hpaError: forbidden, want: types.WorkerPods{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			if tt.hpa != nil {
				objs = append(objs, tt.hpa)
			}
			client := fake.NewClientset(objs...)
			if tt.hpaError != nil {
				client.PrependReactor("get", "horizontalpodautoscalers", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.hpaError
				})
			}
			var dynObjs []runtime.Object
			if tt.pool != nil {
				dynObjs = append(dynObjs, tt.pool)
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{poolGVR: "WorkerPoolList"}, dynObjs...)

			got := workerscale.New(client, dyn, ns, name).Fetch(context.Background())
			if got != tt.want {
				t.Errorf("Fetch() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 6: Run it and watch it fail**

Run: `go test ./cmd/conf-gateway/internal/workerscale/ -v`
Expected: compile error, package `workerscale` has no non-test files / `workerscale.New` undefined.

- [ ] **Step 7: Implement the reader**

Create `cmd/conf-gateway/internal/workerscale/workerscale.go`:

```go
// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package workerscale reads a worker pool's size from its
// HorizontalPodAutoscaler, falling back to the WorkerPool when there is no
// HPA.
package workerscale

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

var workerPoolGVR = schema.GroupVersionResource{Group: "ate.dev", Version: "v1alpha1", Resource: "workerpools"}

// fetchTimeout bounds one Fetch, so a slow API server cannot stall the
// gateway's poll loop.
const fetchTimeout = 2 * time.Second

// Reader reads one worker pool's size. The HPA and the WorkerPool share the
// pool's name.
type Reader struct {
	client    kubernetes.Interface
	dyn       dynamic.Interface
	namespace string
	name      string
}

// New returns a Reader for the pool name in namespace.
func New(client kubernetes.Interface, dyn dynamic.Interface, namespace, name string) *Reader {
	return &Reader{client: client, dyn: dyn, namespace: namespace, name: name}
}

// Fetch returns the pool's size. Without an HPA, Current, Desired and Max
// all equal the WorkerPool's spec.replicas. Any other failure returns a
// WorkerPods with Available false.
func (r *Reader) Fetch(ctx context.Context) types.WorkerPods {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	hpa, err := r.client.AutoscalingV2().HorizontalPodAutoscalers(r.namespace).Get(ctx, r.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		n, ok := r.poolReplicas(ctx)
		if !ok {
			return types.WorkerPods{}
		}
		return types.WorkerPods{Current: n, Desired: n, Max: n, Available: true}
	}
	if err != nil {
		return types.WorkerPods{}
	}

	current := int(hpa.Status.CurrentReplicas)
	if current == 0 {
		// A new HPA has no status until its first sync; the pool's own
		// replicas are what is running meanwhile.
		if n, ok := r.poolReplicas(ctx); ok {
			current = n
		}
	}
	desired := int(hpa.Status.DesiredReplicas)
	if desired == 0 {
		desired = current
	}
	return types.WorkerPods{Current: current, Desired: desired, Max: int(hpa.Spec.MaxReplicas), Available: true}
}

// poolReplicas returns the WorkerPool's spec.replicas.
func (r *Reader) poolReplicas(ctx context.Context) (int, bool) {
	pool, err := r.dyn.Resource(workerPoolGVR).Namespace(r.namespace).Get(ctx, r.name, metav1.GetOptions{})
	if err != nil {
		return 0, false
	}
	n, found, err := unstructured.NestedInt64(pool.Object, "spec", "replicas")
	if err != nil || !found {
		return 0, false
	}
	return int(n), true
}
```

- [ ] **Step 8: Run the tests and confirm they pass**

Run: `go test ./cmd/conf-gateway/internal/workerscale/ ./pkg/types/ -v && go mod tidy -diff`
Expected: PASS; `go mod tidy -diff` prints nothing (client-go is already a direct dependency).

- [ ] **Step 9: Commit**

```bash
git add pkg/types/types.go pkg/types/types_test.go cmd/conf-gateway/internal/workerscale/
git commit -S -m "Read the worker pool's size from its HPA"
```

---

### Task 2: Gateway polls worker pods into telemetry

**Files:**
- Modify: `cmd/conf-gateway/internal/gateway/gateway.go` (options, struct fields, `NewGateway`)
- Modify: `cmd/conf-gateway/internal/gateway/telemetry.go` (`handleTelemetry`, new `pollWorkerScale`)
- Test: `cmd/conf-gateway/internal/gateway/workerscale_test.go` (new)

**Interfaces:**
- Consumes: `types.WorkerPods`, `types.StageTelemetry.WorkerPods`, `.ActorCount` (Task 1).
- Produces:
  - `gateway.WorkerScaleSource` interface `{ Fetch(ctx context.Context) types.WorkerPods }` (satisfied by `*workerscale.Reader`)
  - `gateway.WithWorkerScale(s WorkerScaleSource) Option`
  - `gateway.WithWorkerScaleInterval(d time.Duration) Option` (default 2s)

- [ ] **Step 1: Write the failing test**

Create `cmd/conf-gateway/internal/gateway/workerscale_test.go`:

```go
// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gateway_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// fakeWorkerScale reports whatever WorkerPods the test last stored.
type fakeWorkerScale struct{ pods atomic.Pointer[types.WorkerPods] }

func (f *fakeWorkerScale) Fetch(context.Context) types.WorkerPods { return *f.pods.Load() }

// waitWorkerPods polls telemetry until worker_pods equals want.
func waitWorkerPods(t *testing.T, gw *gateway.Gateway, want types.WorkerPods) types.StageTelemetry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tel := getTelemetry(t, gw)
		if tel.WorkerPods == want {
			return tel
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker_pods = %+v, want %+v", tel.WorkerPods, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStageTelemetry_WorkerPods(t *testing.T) {
	src := &fakeWorkerScale{}
	scaling := types.WorkerPods{Current: 3, Desired: 5, Max: 5, Available: true}
	src.pods.Store(&scaling)
	gw := newTestGateway(t, "http://localhost:8000",
		gateway.WithMaxActors(40),
		gateway.WithWorkerScale(src),
		gateway.WithWorkerScaleInterval(10*time.Millisecond),
	)

	tel := waitWorkerPods(t, gw, scaling)
	if tel.ActorCount != 40 {
		t.Errorf("actor_count = %d, want 40", tel.ActorCount)
	}

	// A failed read shows as unavailable, and a later good read recovers.
	src.pods.Store(&types.WorkerPods{})
	waitWorkerPods(t, gw, types.WorkerPods{})
	steady := types.WorkerPods{Current: 5, Desired: 5, Max: 5, Available: true}
	src.pods.Store(&steady)
	waitWorkerPods(t, gw, steady)
}

func TestStageTelemetry_WorkerPodsWithoutSource(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000")
	tel := getTelemetry(t, gw)
	if tel.WorkerPods != (types.WorkerPods{}) {
		t.Errorf("worker_pods = %+v, want unavailable zero value", tel.WorkerPods)
	}
	if tel.ActorCount != 50 {
		t.Errorf("actor_count = %d, want the default 50", tel.ActorCount)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/conf-gateway/internal/gateway/ -run 'TestStageTelemetry_WorkerPods' -v`
Expected: compile error `undefined: gateway.WithWorkerScale`.

- [ ] **Step 3: Add the options and fields**

In `gateway.go`, after `WithRouterStatus`, add:

```go
// WorkerScaleSource reports the worker pool's size.
type WorkerScaleSource interface {
	Fetch(ctx context.Context) types.WorkerPods
}

// WithWorkerScale polls s every workerScaleInterval and reports the worker
// pool's size through telemetry. Without this option, telemetry reports
// worker_pods.available: false.
func WithWorkerScale(s WorkerScaleSource) Option {
	return func(g *Gateway) { g.workerScale = s }
}

// WithWorkerScaleInterval sets how often the worker pool's size is read.
func WithWorkerScaleInterval(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.workerScaleInterval = d
		}
	}
}
```

In the `Gateway` struct, after `statusInterval time.Duration`, add:

```go
	workerScale         WorkerScaleSource
	workerScaleInterval time.Duration
```

and after `parking atomic.Pointer[routerstatus.Snapshot]`, add:

```go
	workerPods      atomic.Pointer[types.WorkerPods]
```

(run `gofmt -w` afterwards to realign the field block).

In `NewGateway`, add `workerScaleInterval: 2 * time.Second,` to the struct literal next to `statusInterval`, and after `go gw.pollStatus(ctx)` add:

```go
	if gw.workerScale != nil {
		gw.wg.Add(1)
		go gw.pollWorkerScale(ctx)
	}
```

- [ ] **Step 4: Add the poller and telemetry fields**

In `telemetry.go`, after `pollStatus`, add:

```go
// pollWorkerScale reads the worker pool's size once right away, so the stage
// header fills in without waiting a full interval, then every
// workerScaleInterval.
func (g *Gateway) pollWorkerScale(ctx context.Context) {
	defer g.wg.Done()

	ticker := time.NewTicker(g.workerScaleInterval)
	defer ticker.Stop()

	for {
		pods := g.workerScale.Fetch(ctx)
		g.workerPods.Store(&pods)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
```

In `handleTelemetry`, after the parking block, add:

```go
	var pods types.WorkerPods
	if p := g.workerPods.Load(); p != nil {
		pods = *p
	}
```

and add to the `types.StageTelemetry{...}` literal:

```go
		WorkerPods:       pods,
		ActorCount:       g.maxActors,
```

- [ ] **Step 5: Run the gateway tests and confirm they pass**

Run: `go test ./cmd/conf-gateway/... -race`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/conf-gateway/internal/gateway/
git commit -S -m "Report the worker pool's size in stage telemetry"
```

---

### Task 3: Wire the reader into `main.go` with RBAC

**Files:**
- Modify: `cmd/conf-gateway/main.go` (constants, `newWorkerScaleReader`, `main`)
- Modify: `cmd/conf-gateway/main_test.go` (new test)
- Modify: `manifests/gateway.yaml.tmpl` (Role and RoleBinding in `ate-demo-conf`, inserted after the `conf-gateway-trust-bundles` ClusterRoleBinding)
- Modify: `hack/verify-manifests.sh` (wanted strings)

**Interfaces:**
- Consumes: `workerscale.New`, `gateway.WithWorkerScale` (Tasks 1-2).
- Produces: `newWorkerScaleReader() (*workerscale.Reader, error)` in package `main`.

- [ ] **Step 1: Write the failing test**

Append to `cmd/conf-gateway/main_test.go`:

```go
func TestNewWorkerScaleReader_OutsideCluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if _, err := newWorkerScaleReader(); err == nil {
		t.Fatal("newWorkerScaleReader() outside a cluster returned no error")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/conf-gateway/ -run TestNewWorkerScaleReader -v`
Expected: compile error `undefined: newWorkerScaleReader`.

- [ ] **Step 3: Implement**

In `main.go`, add below the `routerStatus*` constants:

```go
// workerPoolNamespace and workerPoolName identify the WorkerPool, and the
// HPA of the same name, whose size the stage header shows.
const (
	workerPoolNamespace = "ate-demo-conf"
	workerPoolName      = "conf-workers"
)
```

Add `"k8s.io/client-go/dynamic"` and `"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/workerscale"` to the imports, and after `newRouterStatusSource`:

```go
// newWorkerScaleReader builds a reader for the conf-workers pool's size from
// the in-cluster Kubernetes API.
func newWorkerScaleReader() (*workerscale.Reader, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("loading in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building Kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	return workerscale.New(client, dyn, workerPoolNamespace, workerPoolName), nil
}
```

In `main`, replace the `else` branch of `if cfg.LocalMode {` with:

```go
	} else {
		opts = append(opts, gateway.WithActorControl(actorstate.NewKubectlATE(cfg.KubectlATE, cfg.ATEAPIEndpoint, cfg.Atespace)))
		if r, err := newWorkerScaleReader(); err != nil {
			log.Printf("Worker pod counts are unavailable: %v", err)
		} else {
			opts = append(opts, gateway.WithWorkerScale(r))
		}
	}
```

- [ ] **Step 4: Add the RBAC**

In `manifests/gateway.yaml.tmpl`, after the `conf-gateway-trust-bundles` ClusterRoleBinding and before the Deployment, insert:

```yaml
---
# The stage header reads the worker pool's size from its HPA, or from the
# WorkerPool when there is no HPA.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: conf-gateway-worker-scale
  namespace: ate-demo-conf
rules:
- apiGroups: ["autoscaling"]
  resources: ["horizontalpodautoscalers"]
  resourceNames:
  - conf-workers
  verbs: ["get"]
- apiGroups: ["ate.dev"]
  resources: ["workerpools"]
  resourceNames:
  - conf-workers
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: conf-gateway-worker-scale
  namespace: ate-demo-conf
subjects:
- kind: ServiceAccount
  name: conf-gateway
  namespace: ate-demo-conf
roleRef:
  kind: Role
  name: conf-gateway-worker-scale
  apiGroup: rbac.authorization.k8s.io
```

In `hack/verify-manifests.sh`, extend the gateway `for want in ...` list with `"horizontalpodautoscalers" "workerpools" "- conf-workers"`.

- [ ] **Step 5: Run the checks and confirm they pass**

Run: `go test ./cmd/conf-gateway/... && bash hack/verify-manifests.sh`
Expected: PASS, exit 0.

- [ ] **Step 6: Commit**

```bash
git add cmd/conf-gateway/main.go cmd/conf-gateway/main_test.go manifests/gateway.yaml.tmpl hack/verify-manifests.sh
git commit -S -m "Let the gateway read the conf-workers HPA"
```

---

### Task 4: Live stage header

**Files:**
- Modify: `cmd/conf-gateway/static/stage.html:47-55` (first two spec cards)
- Modify: `cmd/conf-gateway/static/stage.js` (`el` map, `updateDashboard`, new `updateWorkerPods`)
- Modify: `cmd/conf-gateway/static/stage.css` (after `.spec-value`, ~line 159)
- Test: `cmd/conf-gateway/internal/gateway/static_test.go`

**Interfaces:**
- Consumes: telemetry `worker_pods {current, desired, max, available}`, `actor_count`, `parked_requests`, `parking_available`.
- Produces: element IDs `worker-pods-current`, `worker-pods-max`, `worker-pods-scaling`, `oversub-ratio`, `oversub-waiting`.

- [ ] **Step 1: Write the failing static test**

Append to `static_test.go`:

```go
// TestStatic_StageHeaderIsLive checks the header's pod count and ratio come
// from telemetry, not fixed copy.
func TestStatic_StageHeaderIsLive(t *testing.T) {
	html := readStatic(t, "stage.html")
	for _, fixed := range []string{"2 Physical Worker Pods", "50x Oversubscription"} {
		if strings.Contains(html, fixed) {
			t.Errorf("stage.html still has fixed copy %q", fixed)
		}
	}
	for _, id := range []string{"worker-pods-current", "worker-pods-max", "worker-pods-scaling", "oversub-ratio", "oversub-waiting"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("stage.html lacks #%s", id)
		}
	}
	js := readStatic(t, "stage.js")
	for _, want := range []string{"data.worker_pods", "data.actor_count", "p.current > 0"} {
		if !strings.Contains(js, want) {
			t.Errorf("stage.js lacks %q", want)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/conf-gateway/internal/gateway/ -run TestStatic_StageHeaderIsLive -v`
Expected: FAIL, "still has fixed copy" and missing IDs.

- [ ] **Step 3: Replace the two spec cards**

In `stage.html`, replace the HARDWARE CAPACITY and OVERSUBSCRIPTION cards with:

```html
        <div class="spec-card">
          <span class="spec-label">HARDWARE CAPACITY</span>
          <span class="spec-value highlight-cyan"><span id="worker-pods-current">--</span> Physical Worker Pods <span id="worker-pods-max" class="spec-sub"></span></span>
          <span id="worker-pods-scaling" class="spec-note" hidden></span>
        </div>
        <div class="spec-card">
          <span class="spec-label">OVERSUBSCRIPTION</span>
          <span class="spec-value highlight-magenta"><span id="oversub-ratio">--</span> Oversubscription</span>
          <span id="oversub-waiting" class="spec-note" hidden></span>
        </div>
```

- [ ] **Step 4: Render them in `stage.js`**

Add to the `el` map after `mockBanner`:

```js
    workerPodsCurrent: document.getElementById('worker-pods-current'),
    workerPodsMax: document.getElementById('worker-pods-max'),
    workerPodsScaling: document.getElementById('worker-pods-scaling'),
    oversubRatio: document.getElementById('oversub-ratio'),
    oversubWaiting: document.getElementById('oversub-waiting'),
```

In `updateDashboard`, after the `updateMockBanner` call, add:

```js

    // 6. Update the live pod count and oversubscription header
    updateWorkerPods(data.worker_pods, data.actor_count || 0, data.parked_requests || 0, data.parking_available);
```

After `updateParkingMeter`, add:

```js
  // updateWorkerPods fills the header's pod count and ratio. Both show '--'
  // when the pool's size is unknown or zero, so the ratio never divides by 0.
  function updateWorkerPods(pods, actorCount, parkedCount, parkingAvailable) {
    if (!el.workerPodsCurrent) return;
    const p = pods || {};
    const ok = !!p.available && p.current > 0;

    el.workerPodsCurrent.textContent = ok ? String(p.current) : '--';
    el.workerPodsMax.textContent = ok && p.max > 0 ? 'of ' + p.max : '';
    el.oversubRatio.textContent = ok && actorCount > 0 ? Math.round(actorCount / p.current) + 'x' : '--';

    const scaling = ok && p.desired > 0 && p.desired !== p.current;
    el.workerPodsScaling.hidden = !scaling;
    el.workerPodsScaling.textContent = scaling ? 'scaling ' + p.current + ' → ' + p.desired : '';

    const waiting = !!parkingAvailable && parkedCount > 0;
    el.oversubWaiting.hidden = !waiting;
    el.oversubWaiting.textContent = waiting ? parkedCount + ' waiting' : '';
  }
```

- [ ] **Step 5: Style the new spans**

In `stage.css`, after the `.spec-value` rule, add:

```css
.spec-sub {
  font-size: 0.7rem;
  color: var(--text-secondary);
  text-shadow: none;
}

.spec-note {
  font-size: 0.65rem;
  font-weight: 700;
  color: var(--neon-amber);
  letter-spacing: 0.04em;
}
```

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `go test ./cmd/conf-gateway/... -run 'TestStatic' -v`
Expected: PASS, including `TestStatic_NoInventedStateLabels`.

- [ ] **Step 7: Check the unavailable path in a browser**

Run `make run-local` in the background (LOCAL_MODE, so `worker_pods.available` is false). Open `http://localhost:8090/stage?key=<key from the log>` with the Playwright MCP tools. Expected: the header reads "-- Physical Worker Pods" and "-- Oversubscription", the scaling and waiting lines are hidden, and the browser console has no errors.

`updateDashboard` is private to `stage.js`, but `fetchTelemetry` calls the global `fetch` on every poll, so stub it with `browser_evaluate`:

```js
window.__pods = {current: 2, desired: 5, max: 5, available: true};
const realFetch = window.fetch.bind(window);
window.fetch = (url, opts) => String(url).includes('/api/stage/telemetry')
  ? Promise.resolve(new Response(JSON.stringify({worker_pods: window.__pods, actor_count: 50,
      parked_requests: 3, parking_available: true, actors: {}, workers: [], recent_events: []})))
  : realFetch(url, opts);
```

Wait one poll. Expected: "2 Physical Worker Pods of 5", "scaling 2 → 5", "25x Oversubscription", "3 waiting". Then run `window.__pods = {current: 0, desired: 0, max: 5, available: true}` and expect `--` for both values, with no "Infinity" or "NaN" anywhere on the page. Stop `make run-local`.

- [ ] **Step 8: Commit**

```bash
git add cmd/conf-gateway/static/ cmd/conf-gateway/internal/gateway/static_test.go
git commit -S -m "Show live pod counts and oversubscription on the stage header"
```

---

### Task 5: Autoscaling manifests and deploy script

**Files:**
- Create: `manifests/autoscaling.yaml.tmpl` (Prometheus, prometheus-adapter, HPA)
- Create: `manifests/autoscaling-apiservice.yaml.tmpl` (the cluster-wide APIService alone, so delete can skip it)
- Modify: `manifests/workerpool.yaml.tmpl`
- Modify: `hack/deploy.sh` (defaults, new helpers, `conf_render`, `conf_deploy`, `conf_delete`, `conf_main` usage)
- Modify: `hack/test-deploy.sh`
- Modify: `hack/verify-manifests.sh`

**Interfaces:**
- Produces (bash, in `hack/deploy.sh`):
  - `conf_workers_range_ok MIN MAX`, which returns 0/1
  - `conf_worker_replicas CURRENT MIN MAX`, which prints a number
  - `conf_apiservice_ok APISERVICE_JSON`, which returns 0/1
  - `conf_check_apiservice`, which exits on a foreign owner
  - New placeholders `${WORKER_REPLICAS}`, `${MIN_WORKERS}` and `${MAX_WORKERS}`

- [ ] **Step 1: Write the failing shell tests**

In `hack/test-deploy.sh`, before `# expect_cluster checks conf_gke_cluster_name.`, add:

```bash
# expect_range checks conf_workers_range_ok.
expect_range() {
  local want="$1" min="$2" max="$3" got=0
  conf_workers_range_ok "${min}" "${max}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: conf_workers_range_ok ${min} ${max}: want ${want}" >&2
    fail=1
  fi
}
expect_range ok  2 5
expect_range ok  1 1
expect_range ok  08 09   # leading zeros are decimal, not octal
expect_range bad 0 5
expect_range bad 3 2
expect_range bad "" 5
expect_range bad 2 ""
expect_range bad abc 5
expect_range bad -1 5

# expect_replicas checks conf_worker_replicas: a re-deploy keeps the pool's
# current size, clamped to the range; a new pool starts at the minimum.
expect_replicas() {
  local want="$1" got
  got="$(conf_worker_replicas "$2" "$3" "$4")"
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: conf_worker_replicas '$2' $3 $4 = '${got}', want ${want}" >&2
    fail=1
  fi
}
expect_replicas 2 ""  2 5
expect_replicas 4 4   2 5
expect_replicas 2 1   2 5
expect_replicas 3 7   2 3
expect_replicas 2 abc 2 5

# expect_apiservice checks conf_apiservice_ok against an APIService JSON.
expect_apiservice() {
  local want="$1" json="$2" got=0
  conf_apiservice_ok "${json}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: conf_apiservice_ok ${json}: want ${want}" >&2
    fail=1
  fi
}
expect_apiservice ok  ''
expect_apiservice ok  '{"spec":{"service":{"namespace":"ate-demo-conf","name":"conf-prometheus-adapter"}}}'
expect_apiservice bad '{"spec":{"service":{"namespace":"custom-metrics","name":"custom-metrics-stackdriver-adapter"}}}'
expect_apiservice bad '{"spec":{}}'   # served locally by kube-apiserver

# The autoscaling manifest carries the configured range; the WorkerPool
# carries the replicas deploy.sh chose and the pod requests.
auto="$(DEMO_CONF_MIN_WORKERS=3 DEMO_CONF_MAX_WORKERS=4 conf_render manifests/autoscaling.yaml.tmpl)"
for want in "minReplicas: 3" "maxReplicas: 4" "name: conf-workers" "kind: WorkerPool" 'averageValue: "1"' \
            'ate_worker_state="assigned"' "atenet_router_parking_active" "conf_worker_demand"; do
  if ! grep -qF -- "${want}" <<<"${auto}"; then
    echo "FAIL: rendered autoscaling manifest lacks ${want}" >&2
    fail=1
  fi
done
pool_yaml="$(DEMO_CONF_WORKER_REPLICAS=4 conf_render manifests/workerpool.yaml.tmpl)"
for want in "replicas: 4" "cpu: 500m" "memory: 768Mi"; do
  if ! grep -qF -- "${want}" <<<"${pool_yaml}"; then
    echo "FAIL: rendered workerpool manifest lacks ${want}" >&2
    fail=1
  fi
done
if grep -qF "limits:" <<<"${pool_yaml}"; then
  echo "FAIL: workerpool manifest sets limits; ateom would report them as capacity" >&2
  fail=1
fi
# Without a chosen replica count (the delete path), the pool renders at the minimum.
if ! DEMO_CONF_MIN_WORKERS=3 conf_render manifests/workerpool.yaml.tmpl | grep -qF "replicas: 3"; then
  echo "FAIL: workerpool manifest does not default to DEMO_CONF_MIN_WORKERS" >&2
  fail=1
fi
```

- [ ] **Step 2: Run it and watch it fail**

Run: `bash hack/test-deploy.sh`
Expected: exits 1 with `conf_workers_range_ok: command not found` and FAIL lines.

- [ ] **Step 3: Add the deploy helpers**

In `hack/deploy.sh`, after `DEMO_CONF_ACTORS="${DEMO_CONF_ACTORS:-50}"`, add:

```bash
DEMO_CONF_MIN_WORKERS="${DEMO_CONF_MIN_WORKERS:-2}"
DEMO_CONF_MAX_WORKERS="${DEMO_CONF_MAX_WORKERS:-5}"
```

After `conf_check_router` (before `conf_iam_retry`), add:

```bash
# conf_workers_range_ok returns 0 if MIN and MAX are whole numbers with
# 1 <= MIN <= MAX.
conf_workers_range_ok() {
  local min="$1" max="$2"
  [[ "${min}" =~ ^[0-9]+$ && "${max}" =~ ^[0-9]+$ ]] || return 1
  ((10#${min} >= 1 && 10#${min} <= 10#${max}))
}

# conf_worker_replicas prints the replicas to apply to conf-workers: CURRENT
# (the pool's spec.replicas, empty for a new pool) clamped to [MIN, MAX], so
# a re-deploy keeps the size the HPA chose.
conf_worker_replicas() {
  local current="$1" min="$2" max="$3"
  if [[ ! "${current}" =~ ^[0-9]+$ ]] || ((10#${current} < 10#${min})); then
    echo "$((10#${min}))"
  elif ((10#${current} > 10#${max})); then
    echo "$((10#${max}))"
  else
    echo "$((10#${current}))"
  fi
}

# conf_apiservice_ok returns 0 if APISERVICE_JSON (the
# v1beta1.external.metrics.k8s.io APIService, empty when it does not exist)
# is absent or already served by this demo's adapter.
conf_apiservice_ok() {
  local json="$1"
  [[ -z "${json}" ]] && return 0
  jq -e '(.spec.service // {}) | .namespace == "ate-demo-conf" and .name == "conf-prometheus-adapter"' \
    <<<"${json}" >/dev/null
}

# conf_check_apiservice refuses to take over the cluster's external metrics
# API from another adapter.
conf_check_apiservice() {
  local json
  if ! json="$(run_kubectl get apiservice v1beta1.external.metrics.k8s.io -o json --ignore-not-found)"; then
    echo "error: cannot read APIService v1beta1.external.metrics.k8s.io" >&2
    exit 1
  fi
  if ! conf_apiservice_ok "${json}"; then
    echo "error: v1beta1.external.metrics.k8s.io is served by $(jq -r '(.spec.service // {}) | "\(.namespace // "kube-apiserver")/\(.name // "local")"' <<<"${json}")." >&2
    echo "  The conference demo installs its own prometheus-adapter there; remove the other adapter first." >&2
    exit 1
  fi
}
```

In `conf_render`, add three substitutions before the final `"$1"` line:

```bash
      -e "s|\${WORKER_REPLICAS}|${DEMO_CONF_WORKER_REPLICAS:-${DEMO_CONF_MIN_WORKERS}}|g" \
      -e "s|\${MIN_WORKERS}|${DEMO_CONF_MIN_WORKERS}|g" \
      -e "s|\${MAX_WORKERS}|${DEMO_CONF_MAX_WORKERS}|g" \
```

- [ ] **Step 4: Update the WorkerPool template**

Replace the `spec:` block of `manifests/workerpool.yaml.tmpl` with:

```yaml
spec:
  # hack/deploy.sh keeps the size the conf-workers HPA chose on a re-deploy
  # (manifests/autoscaling.yaml.tmpl).
  replicas: ${WORKER_REPLICAS}
  workerImage: ko://github.com/agent-substrate/substrate/cmd/ateom-gvisor
  template:
    # Requests let the scheduler spread workers across nodes. No limits:
    # ateom reports its limits as the worker's capacity, and a limit below
    # an actor's 1 CPU would leave actors unschedulable.
    resources:
      requests:
        cpu: 500m
        memory: 768Mi
```

(`WorkerPoolSpec.Template` is `json:"template"` and `WorkerPoolPodTemplate.Resources` is `json:"resources"` at the pinned Substrate, `pkg/api/v1alpha1/workerpool_types.go`.)

- [ ] **Step 5: Write the autoscaling manifests**

Create `manifests/autoscaling.yaml.tmpl`:

```yaml
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Autoscales conf-workers on worker demand: turns running plus turns parked.
# Adapted from Substrate's demos/autoscaled-workerpool.
#
# Flow: atenet-router and ate-api-server :9090/metrics --5s scrape-->
#       conf-prometheus --query--> conf-prometheus-adapter
#       --external.metrics.k8s.io--> HPA --/scale--> WorkerPool conf-workers.
# The APIService lives in manifests/autoscaling-apiservice.yaml.tmpl.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: conf-prometheus
  namespace: ate-demo-conf
---
# Pod discovery for the scrape targets in ate-system.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: conf-prometheus
  namespace: ate-system
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: conf-prometheus
  namespace: ate-system
subjects:
- kind: ServiceAccount
  name: conf-prometheus
  namespace: ate-demo-conf
roleRef:
  kind: Role
  name: conf-prometheus
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: conf-prometheus-config
  namespace: ate-demo-conf
data:
  prometheus.yml: |
    global:
      scrape_interval: 5s
      scrape_timeout: 4s
    scrape_configs:
    - job_name: ate-system
      kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: [ate-system]
      relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        regex: atenet-router|ate-api-server
        action: keep
      - source_labels: [__meta_kubernetes_pod_phase]
        regex: Running
        action: keep
      - source_labels: [__meta_kubernetes_pod_ip]
        regex: (.+)
        target_label: __address__
        replacement: $1:9090
      - source_labels: [__meta_kubernetes_pod_name]
        target_label: pod
      - source_labels: [__meta_kubernetes_pod_label_app]
        target_label: app
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: conf-prometheus
  namespace: ate-demo-conf
  labels:
    app: conf-prometheus
spec:
  replicas: 1
  selector:
    matchLabels:
      app: conf-prometheus
  template:
    metadata:
      labels:
        app: conf-prometheus
    spec:
      serviceAccountName: conf-prometheus
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        fsGroup: 65534
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: prometheus
        image: prom/prometheus:v3.5.0
        args:
        - --config.file=/etc/prometheus/prometheus.yml
        - --storage.tsdb.path=/prometheus
        - --storage.tsdb.retention.time=2h
        ports:
        - name: http
          containerPort: 9090
        readinessProbe:
          httpGet:
            path: /-/ready
            port: http
          periodSeconds: 5
        resources:
          requests:
            cpu: 100m
            memory: 256Mi
          limits:
            memory: 512Mi
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: true
          capabilities:
            drop:
            - ALL
        volumeMounts:
        - name: config
          mountPath: /etc/prometheus
          readOnly: true
        - name: data
          mountPath: /prometheus
      volumes:
      - name: config
        configMap:
          name: conf-prometheus-config
      - name: data
        emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: conf-prometheus
  namespace: ate-demo-conf
spec:
  type: ClusterIP
  selector:
    app: conf-prometheus
  ports:
  - name: http
    port: 9090
    targetPort: http
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: conf-prometheus-adapter
  namespace: ate-demo-conf
---
# conf_worker_demand = busy conf-workers + parked requests. max() because
# every ate-api-server replica reports the same pool-wide count; sum()
# because each router replica counts only its own parked requests. Each term
# falls back to 0 when its series is missing. The series query keys on
# ate_workerpool_workers, which exists whenever the pool does, so the metric
# stays listed while the router reports nothing. The router is shared with
# other pools; see the spec's shared-router note.
apiVersion: v1
kind: ConfigMap
metadata:
  name: conf-prometheus-adapter-config
  namespace: ate-demo-conf
data:
  config.yaml: |
    externalRules:
    - seriesQuery: '{__name__="ate_workerpool_workers",ate_workerpool_namespace="ate-demo-conf",ate_workerpool_name="conf-workers"}'
      resources:
        namespaced: false
      name:
        matches: "^ate_workerpool_workers$"
        as: "conf_worker_demand"
      metricsQuery: '(max(ate_workerpool_workers{ate_workerpool_namespace="ate-demo-conf",ate_workerpool_name="conf-workers",ate_worker_state="assigned"}) or vector(0)) + (sum(atenet_router_parking_active) or vector(0))'
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: conf-prometheus-adapter
  namespace: ate-demo-conf
  labels:
    app: conf-prometheus-adapter
spec:
  replicas: 1
  selector:
    matchLabels:
      app: conf-prometheus-adapter
  template:
    metadata:
      labels:
        app: conf-prometheus-adapter
    spec:
      serviceAccountName: conf-prometheus-adapter
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: prometheus-adapter
        image: registry.k8s.io/prometheus-adapter/prometheus-adapter:v0.12.0
        args:
        - --secure-port=6443
        - --cert-dir=/tmp/cert
        - --prometheus-url=http://conf-prometheus.ate-demo-conf.svc:9090/
        - --metrics-relist-interval=30s
        - --config=/etc/adapter/config.yaml
        ports:
        - name: https
          containerPort: 6443
        readinessProbe:
          httpGet:
            path: /readyz
            port: https
            scheme: HTTPS
          initialDelaySeconds: 10
          periodSeconds: 5
          failureThreshold: 5
        livenessProbe:
          httpGet:
            path: /livez
            port: https
            scheme: HTTPS
          initialDelaySeconds: 10
          periodSeconds: 15
        resources:
          requests:
            cpu: 50m
            memory: 64Mi
          limits:
            memory: 256Mi
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: true
          capabilities:
            drop:
            - ALL
        volumeMounts:
        - name: config
          mountPath: /etc/adapter/
          readOnly: true
        - name: tmp
          mountPath: /tmp
      volumes:
      - name: config
        configMap:
          name: conf-prometheus-adapter-config
      - name: tmp
        emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: conf-prometheus-adapter
  namespace: ate-demo-conf
spec:
  type: ClusterIP
  selector:
    app: conf-prometheus-adapter
  ports:
  - name: https
    port: 443
    targetPort: https
---
# Let the adapter authenticate and authorize aggregated-API requests.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: conf-prometheus-adapter-auth-delegator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:auth-delegator
subjects:
- kind: ServiceAccount
  name: conf-prometheus-adapter
  namespace: ate-demo-conf
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: conf-prometheus-adapter-auth-reader
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: extension-apiserver-authentication-reader
subjects:
- kind: ServiceAccount
  name: conf-prometheus-adapter
  namespace: ate-demo-conf
---
# The adapter resolves namespaces and pods at startup even for external
# metrics.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: conf-prometheus-adapter-resource-reader
rules:
- apiGroups: [""]
  resources: ["namespaces", "pods", "services", "configmaps"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: conf-prometheus-adapter-resource-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: conf-prometheus-adapter-resource-reader
subjects:
- kind: ServiceAccount
  name: conf-prometheus-adapter
  namespace: ate-demo-conf
---
# GKE's HPA controller can already read external metrics and scale any
# /scale subresource, so no extra binding is needed for it.
# replicas = ceil(conf_worker_demand / 1): one worker per running or waiting
# turn, between MIN_WORKERS and MAX_WORKERS.
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: conf-workers
  namespace: ate-demo-conf
spec:
  scaleTargetRef:
    apiVersion: ate.dev/v1alpha1
    kind: WorkerPool
    name: conf-workers
  minReplicas: ${MIN_WORKERS}
  maxReplicas: ${MAX_WORKERS}
  metrics:
  - type: External
    external:
      metric:
        name: conf_worker_demand
      target:
        type: AverageValue
        averageValue: "1"
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 0
      selectPolicy: Max
      policies:
      - type: Percent
        value: 100
        periodSeconds: 15
      - type: Pods
        value: 4
        periodSeconds: 15
    scaleDown:
      stabilizationWindowSeconds: 300
```

Create `manifests/autoscaling-apiservice.yaml.tmpl`:

```yaml
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Registers conf-prometheus-adapter as the cluster's external metrics API.
# Only one adapter can own this APIService per cluster, so it lives in its
# own file: hack/deploy.sh applies and deletes it only when it is absent or
# already this demo's. insecureSkipTLSVerify because the adapter self-signs
# its serving certificate (--cert-dir).
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1beta1.external.metrics.k8s.io
spec:
  service:
    name: conf-prometheus-adapter
    namespace: ate-demo-conf
    port: 443
  group: external.metrics.k8s.io
  version: v1beta1
  insecureSkipTLSVerify: true
  groupPriorityMinimum: 100
  versionPriority: 100
```

- [ ] **Step 6: Wire them into `conf_deploy`, `conf_delete` and the usage text**

In `conf_deploy`, right after `conf_check_substrate`, add:

```bash
  if ! conf_workers_range_ok "${DEMO_CONF_MIN_WORKERS}" "${DEMO_CONF_MAX_WORKERS}"; then
    echo "error: need 1 <= DEMO_CONF_MIN_WORKERS (${DEMO_CONF_MIN_WORKERS}) <= DEMO_CONF_MAX_WORKERS (${DEMO_CONF_MAX_WORKERS})" >&2
    exit 1
  fi
```

After `conf_check_template` and before `conf_check_router` (read-only checks run before the router patch), add:

```bash
  conf_check_apiservice
```

Replace the WorkerPool apply and rollout lines with:

```bash
  local current_workers
  if ! current_workers="$(run_kubectl get workerpools.ate.dev conf-workers -n ate-demo-conf \
      -o jsonpath='{.spec.replicas}' --ignore-not-found)"; then
    echo "error: cannot read WorkerPool conf-workers" >&2
    exit 1
  fi
  DEMO_CONF_WORKER_REPLICAS="$(conf_worker_replicas "${current_workers}" \
    "${DEMO_CONF_MIN_WORKERS}" "${DEMO_CONF_MAX_WORKERS}")"
  conf_render manifests/workerpool.yaml.tmpl | ko resolve -f - | run_kubectl_fatal apply -f -
  log_step "Waiting for the conf-workers worker pool rollout..."
  wait_for_pool_rollout_fatal conf-workers ate-demo-conf

  log_step "Installing Prometheus, prometheus-adapter and the conf-workers HPA..."
  conf_render manifests/autoscaling.yaml.tmpl | run_kubectl_fatal apply -f -
  conf_render manifests/autoscaling-apiservice.yaml.tmpl | run_kubectl_fatal apply -f -
  run_kubectl_fatal rollout status deployment/conf-prometheus -n ate-demo-conf --timeout=180s
  run_kubectl_fatal rollout status deployment/conf-prometheus-adapter -n ate-demo-conf --timeout=180s
```

In `conf_delete`, just before the line `conf_render manifests/workerpool.yaml.tmpl | run_kubectl delete --ignore-not-found -f -`, add:

```bash
  local apiservice
  apiservice="$(run_kubectl get apiservice v1beta1.external.metrics.k8s.io -o json --ignore-not-found 2>/dev/null)" || apiservice=""
  if conf_apiservice_ok "${apiservice}"; then
    run_kubectl delete -f <(conf_render manifests/autoscaling-apiservice.yaml.tmpl) --ignore-not-found
  else
    log_step "v1beta1.external.metrics.k8s.io belongs to another adapter; leaving it."
  fi
  run_kubectl delete -f <(conf_render manifests/autoscaling.yaml.tmpl) --ignore-not-found
```

In `conf_main`'s usage, change the `DEMO_CONF_ACTORS` line to:

```bash
      echo "                DEMO_DOMAIN, DEMO_CONF_ACTORS (default 50), SUBSTRATE_REPO,"
      echo "                DEMO_CONF_MIN_WORKERS (default 2), DEMO_CONF_MAX_WORKERS (default 5),"
```

- [ ] **Step 7: Cover the new templates in `verify-manifests.sh`**

Add the three placeholders to its `render()`:

```bash
      -e "s|\${WORKER_REPLICAS}|2|g" \
      -e "s|\${MIN_WORKERS}|2|g" \
      -e "s|\${MAX_WORKERS}|5|g" \
```

Add `WORKERPOOL_MANIFEST="manifests/workerpool.yaml.tmpl"`, `AUTOSCALING_MANIFEST="manifests/autoscaling.yaml.tmpl"`, `APISERVICE_MANIFEST="manifests/autoscaling-apiservice.yaml.tmpl"` next to the other names, add all three to the `for f in ...` placeholder loop, and before `exit "${status}"`, add:

```bash
for f in "${WORKERPOOL_MANIFEST}" "${AUTOSCALING_MANIFEST}" "${APISERVICE_MANIFEST}"; do
  if ! check_objects "$(render "${f}" "")"; then
    echo "error: ${f} failed offline object validation" >&2
    status=1
  fi
done
```

- [ ] **Step 8: Run all checks and confirm they pass**

Run: `bash hack/test-deploy.sh && bash hack/verify-manifests.sh && make verify`
Expected: all exit 0; shellcheck is clean.

- [ ] **Step 9: Commit**

```bash
git add manifests/ hack/
git commit -S -m "Autoscale conf-workers from Prometheus with an HPA"
```

---

### Task 6: Docs

**Files:**
- Modify: `README.md` ("Prerequisites" env list near line 30, "What it shows" lines 52-60, new "Autoscaling" section before "## Architecture", "Limits and risks")
- Modify: `substrate-gaps.md` (three new H2 entries at the end)

**Interfaces:** none.

- [ ] **Step 1: README**

In "Prerequisites", after the `DEMO_CONF_ACTORS` item, add: "`DEMO_CONF_MIN_WORKERS` (default 2) and `DEMO_CONF_MAX_WORKERS` (default 5), the `conf-workers` autoscaling range."

In "What it shows", change "a `WorkerPool` of 2 Kubernetes pods" to "a `WorkerPool` of 2 to 5 Kubernetes pods". Replace the paragraph starting "The `/stage` header's" with:

```markdown
The `/stage` header is live. The pod count is the `conf-workers` HPA's
current replicas ("3 of 5"), or the WorkerPool's replicas when there is no
HPA, and reads "scaling N → M" while the HPA wants a different size. The
oversubscription ratio is the attendee count (`ACTOR_COUNT`, set from
`DEMO_CONF_ACTORS`) divided by the current pod count, rounded, next to the
number of turns parked at the router. The gateway reads these every 2s
(`cmd/conf-gateway/internal/workerscale`); when it can't, the header shows
`--`.
```

Before `## Architecture`, add:

```markdown
## Autoscaling

`conf-workers` scales between `DEMO_CONF_MIN_WORKERS` and
`DEMO_CONF_MAX_WORKERS` (2 and 5 by default) on worker demand: busy workers
plus requests parked at the router, one worker per turn. A Prometheus in
`ate-demo-conf` scrapes `atenet-router` and `ate-api-server` every 5s,
prometheus-adapter serves the sum as the external metric
`conf_worker_demand`, and an HPA writes the WorkerPool's replicas
(`manifests/autoscaling.yaml.tmpl`). The pool grows within about 20s of a
burst and shrinks 5 minutes after it ends. Five workers fit on two
c3-standard-4 nodes, so no node autoscaling is involved.

Watch it with:

    kubectl -n ate-demo-conf get hpa conf-workers -w
    kubectl get --raw /apis/external.metrics.k8s.io/v1beta1/namespaces/ate-demo-conf/conf_worker_demand

The install owns the cluster's `v1beta1.external.metrics.k8s.io` APIService
and refuses to run if another adapter (for example the Stackdriver adapter)
already serves it.
```

In "Limits and risks", add a bullet:

```markdown
- The router's parking gauge has no pool label, and `atenet-router` is
  shared with every pool on the cluster. Parked requests for other demos
  (`ate-demo-swarm`, `workshop`) also scale `conf-workers`, capped at
  `DEMO_CONF_MAX_WORKERS`.
```

- [ ] **Step 2: substrate-gaps.md**

Append, matching the file's symptom / workaround / fix shape:

```markdown
## Workers without limits each report the whole node as capacity

ateom sets a worker's capacity from its container's `limits.cpu` and
`limits.memory`. Without limits it reports the node's allocatable CPU and
memory, so several workers on one node each believe they own it. The
autoscaled `conf-workers` pool puts up to 5 workers on 2 nodes and sets
requests but no limits (`manifests/workerpool.yaml.tmpl`), which is harmless
while each worker holds one 1-CPU actor. The fix belongs in ateom's capacity
reporting, which should use requests, or the node's share, when no limit is
set.

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
```

- [ ] **Step 3: Verify and commit**

Run: `make verify`
Expected: PASS.

```bash
git add README.md substrate-gaps.md
git commit -S -m "Document conf-workers autoscaling and its Substrate gaps"
```

---

### Task 7: Live verification on `substrate-poc`

**Ask the user before starting this task.** It deploys to the demo cluster and changes cluster-wide objects (the external metrics APIService). All commands use `KUBECTL_CONTEXT=gke_mofilabs_us-west1-c_substrate-poc` or `--context gke_mofilabs_us-west1-c_substrate-poc`. Ask the user for the deploy environment (`BUCKET_NAME`, `KO_DOCKER_REPO`, `PROJECT_ID`) rather than guessing it.

**Files:** none, unless a check fails. A failure goes back to the owning task.

- [ ] **Step 1: Confirm the APIService is free**

Run: `kubectl get apiservice v1beta1.external.metrics.k8s.io --context gke_mofilabs_us-west1-c_substrate-poc`
Expected: NotFound. If it exists, stop and ask the user.

- [ ] **Step 2: Deploy**

Run: `KUBECTL_CONTEXT=gke_mofilabs_us-west1-c_substrate-poc make deploy` with the user's environment.
Expected: finishes. The worker pods restart once because of the new pod template, and the new objects roll out.

- [ ] **Step 3: Check the metric path**

```bash
C=gke_mofilabs_us-west1-c_substrate-poc
kubectl -n ate-demo-conf port-forward svc/conf-prometheus 9091:9090 --context $C &
curl -s 'localhost:9091/api/v1/targets' | jq '.data.activeTargets[] | {pod: .labels.pod, health}'
kubectl get --raw /apis/external.metrics.k8s.io/v1beta1/namespaces/ate-demo-conf/conf_worker_demand --context $C | jq .
kubectl -n ate-demo-conf get hpa conf-workers --context $C
```

Expected:
- Every router and api-server target is `up`. If they are down, check whether a NetworkPolicy in `ate-system` blocks port 9090 from `ate-demo-conf`.
- The metric returns a value.
- The HPA shows `TARGETS 0/1` and 2 replicas.

- [ ] **Step 4: Burst**

Open `/stage` (see the deploy output for the port-forward and key), press "Simulate 50", and in another shell run `kubectl -n ate-demo-conf get hpa conf-workers -w --context $C`.

Expected:
- The HPA reaches 5 within about 30s.
- The header goes through "scaling 2 → 5" to "5 Physical Worker Pods of 5" and "10x Oversubscription".
- The "K waiting" count rises, then falls.

While the burst runs, query `max(ate_workerpool_workers{ate_workerpool_name="conf-workers",ate_worker_state="assigned"})` and `sum(atenet_router_parking_active)` in Prometheus. From those, estimate how long a turn holds a worker, and record it for the user.

- [ ] **Step 5: Quiet**

Stop the simulation.

Expected: about 5 minutes later the header shows "scaling 5 → 2" and then "2 of 5".

- [ ] **Step 6: Re-deploy while scaled up**

Start "Simulate 50" again. Once at 5, re-run `make deploy`.

Expected: `kubectl -n ate-demo-conf get workerpool conf-workers -o jsonpath='{.spec.replicas}'` stays 5 throughout.

- [ ] **Step 7: Report**

Tell the user the measured reaction times, the turn duration, and anything that failed. Do not commit anything in this task unless a fix was needed, and any fix goes through its owning task's steps.

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
	"github.com/google/go-cmp/cmp"
)

// fakeWorkerScale reports whatever WorkerPods the test last stored.
type fakeWorkerScale struct {
	pods atomic.Pointer[types.WorkerPods]
}

func (f *fakeWorkerScale) Fetch(context.Context) types.WorkerPods { return *f.pods.Load() }

// waitWorkerPods polls telemetry until worker_pods equals want.
func waitWorkerPods(t *testing.T, gw *gateway.Gateway, want types.WorkerPods) types.StageTelemetry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tel := getTelemetry(t, gw)
		if cmp.Equal(tel.WorkerPods, want) {
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
	if !cmp.Equal(tel.WorkerPods, types.WorkerPods{}) {
		t.Errorf("worker_pods = %+v, want unavailable zero value", tel.WorkerPods)
	}
	if tel.ActorCount != 50 {
		t.Errorf("actor_count = %d, want the default 50", tel.ActorCount)
	}
}

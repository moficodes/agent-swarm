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

package routerstatus_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/routerstatus"
)

func statusz(t *testing.T, parked int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/statusz" || r.URL.Query().Get("format") != "json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"parking":{"active":%d},"other":"ignored"}`, parked)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func metricsServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAggregator_SumsAcrossPods(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{statusz(t, 2), statusz(t, 3)}, http.DefaultClient)
	got := a.Fetch(context.Background())
	if got != (routerstatus.Snapshot{Parked: 5, Available: true, Targets: 2}) {
		t.Errorf("Fetch = %+v", got)
	}
}

func TestAggregator_SumsPrometheusMetricsAcrossPods(t *testing.T) {
	pod1 := metricsServer(t, "# HELP atenet_router_parking_active parked\n# TYPE atenet_router_parking_active gauge\natenet_router_parking_active{otel_scope_name=\"atenet-router\"} 3\n")
	// A freshly started router pod exports Go/OTel metrics before any request is parked.
	pod2 := metricsServer(t, "# HELP target_info Target metadata\n# TYPE target_info gauge\ntarget_info{service_name=\"atenet-router\"} 1\n")
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{pod1, pod2}, http.DefaultClient)
	got := a.Fetch(context.Background())
	if got != (routerstatus.Snapshot{Parked: 3, Available: true, Targets: 2}) {
		t.Errorf("Fetch = %+v", got)
	}
}

func TestAggregator_PartialFailureStillAvailable(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{statusz(t, 4), "http://127.0.0.1:1"}, http.DefaultClient)
	got := a.Fetch(context.Background())
	if !got.Available || got.Parked != 4 || got.Failed != 1 {
		t.Errorf("Fetch = %+v", got)
	}
}

func TestAggregator_AllFailedIsUnavailable(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{"http://127.0.0.1:1"}, http.DefaultClient)
	if got := a.Fetch(context.Background()); got.Available {
		t.Errorf("Fetch = %+v, want unavailable", got)
	}
}

func TestAggregator_NoTargetsIsUnavailable(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{}, http.DefaultClient)
	if got := a.Fetch(context.Background()); got.Available {
		t.Errorf("Fetch = %+v, want unavailable", got)
	}
}

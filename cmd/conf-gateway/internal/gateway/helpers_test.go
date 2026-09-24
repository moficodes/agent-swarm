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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// newTestGateway builds a gateway that never polls a real statusz endpoint.
// An empty statusURL gets a fake statusz server reporting zero parked
// requests. Without a WithActorControl option the gateway runs in local mode
// and never shells out to kubectl-ate.
func newTestGateway(t *testing.T, routerURL, statusURL string, opts ...gateway.Option) *gateway.Gateway {
	t.Helper()
	if statusURL == "" {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"parking":{"active":0}}`))
		}))
		t.Cleanup(srv.Close)
		statusURL = srv.URL
	}
	all := append([]gateway.Option{
		gateway.WithPresenterKey(testPresenterKey),
	}, opts...)
	gw := gateway.NewGateway(routerURL, statusURL, all...)
	t.Cleanup(gw.Close)
	return gw
}

// asPresenter wraps the gateway handler so every request carries the
// presenter key, for tests that exercise presenter-only features.
func asPresenter(gw *gateway.Gateway) http.Handler {
	h := gw.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(gateway.PresenterHeaderName, testPresenterKey)
		h.ServeHTTP(w, r)
	})
}

// presenterRequest sets the presenter key on req.
func presenterRequest(gw *gateway.Gateway, req *http.Request) *http.Request {
	req.Header.Set(gateway.PresenterHeaderName, gw.PresenterKey())
	return req
}

// postJSON sends body as attendee to path and returns the recorded response.
func postJSON(gw *gateway.Gateway, attendee, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gw.MintToken(attendee))
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)
	return rec
}

// getTelemetry fetches the stage telemetry as the presenter.
func getTelemetry(t *testing.T, gw *gateway.Gateway) types.StageTelemetry {
	t.Helper()
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, presenterRequest(gw, httptest.NewRequest(http.MethodGet, "/api/stage/telemetry", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("telemetry status = %d: %s", rec.Code, rec.Body.String())
	}
	var tel types.StageTelemetry
	if err := json.Unmarshal(rec.Body.Bytes(), &tel); err != nil {
		t.Fatalf("decoding telemetry: %v", err)
	}
	return tel
}

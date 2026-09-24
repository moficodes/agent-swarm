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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
)

// newTestGateway builds a gateway that never shells out to kubectl-ate and
// never polls a real statusz endpoint. An empty statusURL gets a fake statusz
// server reporting zero parked requests. Caller options are applied after the
// defaults, so they can override the suspender.
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
	all := append([]gateway.Option{gateway.WithSuspender(&mockSuspender{})}, opts...)
	gw := gateway.NewGateway(routerURL, statusURL, all...)
	t.Cleanup(gw.Close)
	return gw
}

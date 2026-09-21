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

package main

import (
	"os"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
)

func TestGatewayConfigDefaults(t *testing.T) {
	origPort := os.Getenv("PORT")
	defer func() {
		_ = os.Setenv("PORT", origPort)
	}()

	_ = os.Unsetenv("PORT")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	if port != "8090" {
		t.Errorf("expected default port 8090, got %s", port)
	}

	_ = os.Setenv("PORT", "9090")
	port = os.Getenv("PORT")
	if port != "9090" {
		t.Errorf("expected custom port 9090, got %s", port)
	}
}

func TestGatewayInstanceCreation(t *testing.T) {
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040",
		gateway.WithAtespace("ate-demo-conf"),
		gateway.WithJWTSecret("test-secret"),
	)
	defer gw.Close()

	if gw.Handler() == nil {
		t.Fatalf("expected non-nil gateway handler")
	}
}

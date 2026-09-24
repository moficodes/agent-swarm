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
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
)

var configEnvVars = []string{"PORT", "ROUTER_URL", "STATUS_URL", "JWT_SECRET", "PRESENTER_TOKEN", "GEMINI_API_KEY", "ATESPACE", "SUSPEND_MODE"}

func TestLoadConfig_Defaults(t *testing.T) {
	for _, env := range configEnvVars {
		t.Setenv(env, "")
	}

	cfg := LoadConfig()

	if cfg.Port != "8090" {
		t.Errorf("expected default Port '8090', got %q", cfg.Port)
	}
	if cfg.RouterURL != "http://localhost:8000" {
		t.Errorf("expected default RouterURL 'http://localhost:8000', got %q", cfg.RouterURL)
	}
	if cfg.StatusURL != "http://localhost:4040" {
		t.Errorf("expected default StatusURL 'http://localhost:4040', got %q", cfg.StatusURL)
	}
	if cfg.JWTSecret != "" {
		t.Errorf("expected empty default JWTSecret so the gateway generates one, got %q", cfg.JWTSecret)
	}
	if cfg.SuspendMode != "" {
		t.Errorf("expected empty default SuspendMode, got %q", cfg.SuspendMode)
	}
	if cfg.PresenterKey != "" {
		t.Errorf("expected empty default PresenterKey so the gateway generates one, got %q", cfg.PresenterKey)
	}
	if cfg.Atespace != "ate-demo-conf" {
		t.Errorf("expected default Atespace 'ate-demo-conf', got %q", cfg.Atespace)
	}
	if cfg.GeminiAPIKey != "" {
		t.Errorf("expected default GeminiAPIKey '', got %q", cfg.GeminiAPIKey)
	}
}

func TestLoadConfig_CustomEnv(t *testing.T) {
	t.Setenv("PORT", "9099")
	t.Setenv("ROUTER_URL", "http://router.example.com")
	t.Setenv("STATUS_URL", "http://status.example.com")
	t.Setenv("JWT_SECRET", "custom-secret")
	t.Setenv("PRESENTER_TOKEN", "custom-presenter")
	t.Setenv("GEMINI_API_KEY", "custom-key")
	t.Setenv("ATESPACE", "custom-ns")
	t.Setenv("SUSPEND_MODE", "noop")

	cfg := LoadConfig()

	if cfg.Port != "9099" {
		t.Errorf("expected Port '9099', got %q", cfg.Port)
	}
	if cfg.RouterURL != "http://router.example.com" {
		t.Errorf("expected RouterURL 'http://router.example.com', got %q", cfg.RouterURL)
	}
	if cfg.StatusURL != "http://status.example.com" {
		t.Errorf("expected StatusURL 'http://status.example.com', got %q", cfg.StatusURL)
	}
	if cfg.JWTSecret != "custom-secret" {
		t.Errorf("expected JWTSecret 'custom-secret', got %q", cfg.JWTSecret)
	}
	if cfg.PresenterKey != "custom-presenter" {
		t.Errorf("expected PresenterKey 'custom-presenter', got %q", cfg.PresenterKey)
	}
	if cfg.GeminiAPIKey != "custom-key" {
		t.Errorf("expected GeminiAPIKey 'custom-key', got %q", cfg.GeminiAPIKey)
	}
	if cfg.Atespace != "custom-ns" {
		t.Errorf("expected Atespace 'custom-ns', got %q", cfg.Atespace)
	}
	if cfg.SuspendMode != "noop" {
		t.Errorf("expected SuspendMode 'noop', got %q", cfg.SuspendMode)
	}
}

func TestSuspenderOptions(t *testing.T) {
	if opts, err := suspenderOptions(""); err != nil || len(opts) != 0 {
		t.Errorf("default mode: got %d options, err %v; want the gateway's own suspender", len(opts), err)
	}
	if opts, err := suspenderOptions("noop"); err != nil || len(opts) != 1 {
		t.Errorf("noop mode: got %d options, err %v; want one WithSuspender option", len(opts), err)
	}
	if _, err := suspenderOptions("bogus"); err == nil {
		t.Errorf("unknown mode: want error")
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

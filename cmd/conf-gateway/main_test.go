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

func TestLoadConfig_Defaults(t *testing.T) {
	envVars := []string{"PORT", "ROUTER_URL", "STATUS_URL", "JWT_SECRET", "GEMINI_API_KEY", "ATESPACE"}
	saved := make(map[string]string)
	for _, env := range envVars {
		saved[env] = os.Getenv(env)
		_ = os.Unsetenv(env)
	}
	defer func() {
		for env, val := range saved {
			if val != "" {
				_ = os.Setenv(env, val)
			} else {
				_ = os.Unsetenv(env)
			}
		}
	}()

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
	if cfg.JWTSecret != "conf-demo-secret-key" {
		t.Errorf("expected default JWTSecret 'conf-demo-secret-key', got %q", cfg.JWTSecret)
	}
	if cfg.Atespace != "ate-demo-conf" {
		t.Errorf("expected default Atespace 'ate-demo-conf', got %q", cfg.Atespace)
	}
	if cfg.GeminiAPIKey != "" {
		t.Errorf("expected default GeminiAPIKey '', got %q", cfg.GeminiAPIKey)
	}
}

func TestLoadConfig_CustomEnv(t *testing.T) {
	envVars := map[string]string{
		"PORT":           "9099",
		"ROUTER_URL":     "http://router.example.com",
		"STATUS_URL":     "http://status.example.com",
		"JWT_SECRET":     "custom-secret",
		"GEMINI_API_KEY": "custom-key",
		"ATESPACE":       "custom-ns",
	}
	saved := make(map[string]string)
	for env, val := range envVars {
		saved[env] = os.Getenv(env)
		_ = os.Setenv(env, val)
	}
	defer func() {
		for env := range envVars {
			val := saved[env]
			if val != "" {
				_ = os.Setenv(env, val)
			} else {
				_ = os.Unsetenv(env)
			}
		}
	}()

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
	if cfg.GeminiAPIKey != "custom-key" {
		t.Errorf("expected GeminiAPIKey 'custom-key', got %q", cfg.GeminiAPIKey)
	}
	if cfg.Atespace != "custom-ns" {
		t.Errorf("expected Atespace 'custom-ns', got %q", cfg.Atespace)
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

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
	"bytes"
	"context"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/gateway"
)

var configEnvVars = []string{"PORT", "ROUTER_URL", "STATUS_URL", "HMAC_SECRET", "PRESENTER_TOKEN", "ATESPACE", "ATEAPI_ENDPOINT", "KUBECTL_ATE", "STATE_POLL_INTERVAL", "SUSPEND_AFTER", "DEMO_MOCK_LLM", "LOCAL_MODE", "VERTEX_PROJECT", "VERTEX_LOCATION", "MODEL_ALLOWLIST", "ROUTER_STATUS_DISCOVERY", "ACTOR_COUNT"}

// clearConfigEnv clears every environment variable LoadConfig reads, so tests
// start from a clean slate regardless of the host environment.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, env := range configEnvVars {
		t.Setenv(env, "")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	clearConfigEnv(t)

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
	if cfg.HMACSecret != "" {
		t.Errorf("expected empty default HMACSecret so the gateway generates one, got %q", cfg.HMACSecret)
	}
	if cfg.StatePollInterval != time.Second {
		t.Errorf("expected default StatePollInterval 1s, got %s", cfg.StatePollInterval)
	}
	if cfg.SuspendAfter != 0 {
		t.Errorf("expected default SuspendAfter 0, got %s", cfg.SuspendAfter)
	}
	if cfg.KubectlATE != "kubectl-ate" || cfg.ATEAPIEndpoint != "" {
		t.Errorf("expected kubectl-ate with its default endpoint, got %q %q", cfg.KubectlATE, cfg.ATEAPIEndpoint)
	}
	if cfg.LocalMode || cfg.MockLLM {
		t.Errorf("expected LocalMode and MockLLM off by default, got %v %v", cfg.LocalMode, cfg.MockLLM)
	}
	if cfg.PresenterKey != "" {
		t.Errorf("expected empty default PresenterKey so the gateway generates one, got %q", cfg.PresenterKey)
	}
	if cfg.Atespace != "ate-demo-conf" {
		t.Errorf("expected default Atespace 'ate-demo-conf', got %q", cfg.Atespace)
	}
	if cfg.RouterStatusDiscovery != "static" {
		t.Errorf("expected default RouterStatusDiscovery 'static', got %q", cfg.RouterStatusDiscovery)
	}
	if cfg.ActorCount != 50 {
		t.Errorf("expected default ActorCount 50, got %d", cfg.ActorCount)
	}
}

func TestLoadConfig_CustomEnv(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PORT", "9099")
	t.Setenv("ROUTER_URL", "http://router.example.com")
	t.Setenv("STATUS_URL", "http://status.example.com")
	t.Setenv("HMAC_SECRET", "custom-secret")
	t.Setenv("PRESENTER_TOKEN", "custom-presenter")
	t.Setenv("ATESPACE", "custom-ns")
	t.Setenv("ATEAPI_ENDPOINT", "ateapi.example:443")
	t.Setenv("KUBECTL_ATE", "/bin/kubectl-ate")
	t.Setenv("STATE_POLL_INTERVAL", "250ms")
	t.Setenv("SUSPEND_AFTER", "30s")
	t.Setenv("DEMO_MOCK_LLM", "true")

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
	if cfg.HMACSecret != "custom-secret" {
		t.Errorf("expected HMACSecret 'custom-secret', got %q", cfg.HMACSecret)
	}
	if cfg.PresenterKey != "custom-presenter" {
		t.Errorf("expected PresenterKey 'custom-presenter', got %q", cfg.PresenterKey)
	}
	if cfg.Atespace != "custom-ns" {
		t.Errorf("expected Atespace 'custom-ns', got %q", cfg.Atespace)
	}
	if cfg.ATEAPIEndpoint != "ateapi.example:443" || cfg.KubectlATE != "/bin/kubectl-ate" {
		t.Errorf("kubectl-ate config = %q %q", cfg.ATEAPIEndpoint, cfg.KubectlATE)
	}
	if cfg.StatePollInterval != 250*time.Millisecond || cfg.SuspendAfter != 30*time.Second {
		t.Errorf("durations = %s %s", cfg.StatePollInterval, cfg.SuspendAfter)
	}
	if !cfg.MockLLM {
		t.Errorf("expected MockLLM with DEMO_MOCK_LLM=true")
	}
}

func TestLoadConfig_InvalidPollIntervalFallsBack(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("STATE_POLL_INTERVAL", "soon")
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	if got := LoadConfig().StatePollInterval; got != time.Second {
		t.Errorf("StatePollInterval = %s, want 1s", got)
	}
	if !strings.Contains(buf.String(), "STATE_POLL_INTERVAL") {
		t.Errorf("log = %q, want a line naming STATE_POLL_INTERVAL", buf.String())
	}
}

func TestLoadConfig_ActorCount(t *testing.T) {
	for _, c := range []struct {
		env  string
		want int
	}{
		{"12", 12},
		{"0", 50},
		{"-3", 50},
		{"many", 50},
	} {
		clearConfigEnv(t)
		t.Setenv("ACTOR_COUNT", c.env)
		if got := LoadConfig().ActorCount; got != c.want {
			t.Errorf("ACTOR_COUNT=%q: ActorCount = %d, want %d", c.env, got, c.want)
		}
	}
}

func TestLoadConfig_LocalMode(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("LOCAL_MODE", "true")
	if !LoadConfig().LocalMode {
		t.Error("LOCAL_MODE=true did not set LocalMode")
	}
}

func TestGatewayInstanceCreation(t *testing.T) {
	gw := gateway.NewGateway("http://localhost:8000",
		gateway.WithAtespace("ate-demo-conf"),
		gateway.WithSecret("test-secret"),
	)
	defer gw.Close()

	if gw.Handler() == nil {
		t.Fatalf("expected non-nil gateway handler")
	}
}

func TestLoadConfig_ModelDefaults(t *testing.T) {
	clearConfigEnv(t)
	cfg := LoadConfig()
	if cfg.VertexLocation != "us-central1" {
		t.Errorf("VertexLocation = %q", cfg.VertexLocation)
	}
	if !slices.Equal(cfg.ModelAllowlist, []string{"gemini-2.5-flash"}) {
		t.Errorf("ModelAllowlist = %v", cfg.ModelAllowlist)
	}
}

func TestLoadConfig_ModelAllowlistTrimsEntries(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MODEL_ALLOWLIST", " gemini-2.5-flash , gemini-2.5-pro ,")
	if got := LoadConfig().ModelAllowlist; !slices.Equal(got, []string{"gemini-2.5-flash", "gemini-2.5-pro"}) {
		t.Errorf("ModelAllowlist = %v", got)
	}
}

func TestNewModelProxy_DisabledWithoutProject(t *testing.T) {
	clearConfigEnv(t)
	p, err := newModelProxy(context.Background(), LoadConfig())
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() {
		t.Error("proxy enabled with no VERTEX_PROJECT")
	}
}

func TestNewRouterStatusSource_Static(t *testing.T) {
	cfg := Config{RouterStatusDiscovery: "static", StatusURL: "http://localhost:4040"}
	src, err := newRouterStatusSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := src.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(targets, []string{"http://localhost:4040"}) {
		t.Errorf("Targets = %v", targets)
	}
}

func TestNewRouterStatusSource_InvalidValueIsFatal(t *testing.T) {
	cfg := Config{RouterStatusDiscovery: "bogus"}
	if _, err := newRouterStatusSource(cfg); err == nil {
		t.Error("expected an error for an invalid ROUTER_STATUS_DISCOVERY value, got nil")
	}
}

func TestResolveSecret(t *testing.T) {
	if s, gen := resolveSecret("given"); s != "given" || gen {
		t.Errorf("resolveSecret(given) = %q, %v", s, gen)
	}
	a, gen := resolveSecret("")
	b, _ := resolveSecret("")
	if !gen || len(a) < 32 || a == b {
		t.Errorf("generated secrets %q %q", a, b)
	}
}

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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/pkg/ai"
	"github.com/agent-substrate/substrate/demos/conference/pkg/engine"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func TestProxy_DoesNotForwardSecretsToActor(t *testing.T) {
	var mu sync.Mutex
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{ID: "m", Sender: types.SenderAssistant, Content: "hi"})
	}))
	t.Cleanup(upstream.Close)

	gw := newTestGateway(t, upstream.URL, "", gateway.WithGeminiAPIKey("server-side-gemini-key"))
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gw.MintToken("attendee-01"))
	req.Header.Set(gateway.PresenterHeaderName, testPresenterKey)
	req.AddCookie(presenterCookie())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.Get("ate-target-actor") != "ate-demo-conf/attendee-01" {
		t.Errorf("ate-target-actor = %q, want ate-demo-conf/attendee-01", got.Get("ate-target-actor"))
	}
	for _, h := range []string{"X-Gemini-API-Key", "Authorization", "Cookie", gateway.PresenterHeaderName} {
		if v := got.Get(h); v != "" {
			t.Errorf("actor received %s: %q", h, v)
		}
	}
}

func TestPost_RejectsOversizedBody(t *testing.T) {
	forEachMode(t, func(t *testing.T, h *approvalHarness) {
		token := h.gw.MintToken("attendee-01")
		big := `{"message":"` + strings.Repeat("a", 70*1024) + `"}`
		if status, _ := h.post(t, "/api/chat", token, big); status != http.StatusRequestEntityTooLarge {
			t.Errorf("chat: status = %d, want 413", status)
		}
		bigApproval := `{"approval_id":"app-1","decision":"APPROVE","code":"` + strings.Repeat("a", 70*1024) + `"}`
		if status, _ := h.post(t, "/api/approve", token, bigApproval); status != http.StatusRequestEntityTooLarge {
			t.Errorf("approve: status = %d, want 413", status)
		}
	})
}

func TestPost_RequiresJSONContentType(t *testing.T) {
	forEachMode(t, func(t *testing.T, h *approvalHarness) {
		token := h.gw.MintToken("attendee-01")
		for _, path := range []string{"/api/chat", "/api/approve"} {
			req, _ := http.NewRequest(http.MethodPost, h.url+path, strings.NewReader(`{"message":"hi"}`))
			req.Header.Set("Content-Type", "text/plain")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Errorf("%s: status = %d, want 415", path, resp.StatusCode)
			}
		}
	})
}

// TestAIChat_ConcurrentWithTelemetry is meaningful under -race: gateway-side
// AI turns update shared state while the stage polls it.
func TestAIChat_ConcurrentWithTelemetry(t *testing.T) {
	aiClient := ai.NewGeminiClient(ai.WithProject(""), ai.WithGeminiMockEngine(engine.NewMockEngine()))
	gw := newTestGateway(t, "http://127.0.0.1:1", "", gateway.WithAIClient(aiClient))
	ts := httptest.NewServer(asPresenter(gw))
	t.Cleanup(ts.Close)

	var wg sync.WaitGroup
	for i := 1; i <= 4; i++ {
		wg.Add(2)
		go func(id string) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+gw.MintToken(id))
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}(fmt.Sprintf("attendee-%02d", i))
		go func() {
			defer wg.Done()
			if resp, err := http.Get(ts.URL + "/api/stage/telemetry"); err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}

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
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/internal/localactors"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/gemini"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/geminitest"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/server"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const contractSecret = "contract-secret-contract-secret-00"

// contractEnv wires a gateway to real workload servers behind a local
// router, with the real model proxy in front of a fake Vertex.
type contractEnv struct {
	gw        *gateway.Gateway
	routerURL string
	vertex    *geminitest.Server

	mu     sync.Mutex
	actors map[string]*server.WorkloadServer
}

func newContractEnv(t *testing.T, vertex *geminitest.Server, opts ...gateway.Option) *contractEnv {
	t.Helper()
	proxy, err := modelproxy.New(modelproxy.Config{
		Secret: []byte(contractSecret), Atespace: "ate-demo-conf",
		Project: "proj", Location: "loc", Models: []string{"gemini-2.5-flash"},
		Upstream:    vertex.URL,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(proxy)
	t.Cleanup(proxySrv.Close)

	env := &contractEnv{vertex: vertex, actors: map[string]*server.WorkloadServer{}}
	router := localactors.New("ate-demo-conf", func(name string) http.Handler {
		ws := server.NewWorkloadServer(
			server.WithWorkspaceDir(t.TempDir()),
			server.WithGeminiConfig(gemini.Config{ProxyURL: proxySrv.URL, Project: "proj", Location: "loc", Model: "gemini-2.5-flash"}),
		)
		env.mu.Lock()
		env.actors[name] = ws
		env.mu.Unlock()
		return ws.Handler()
	})
	routerSrv := httptest.NewServer(router)
	t.Cleanup(routerSrv.Close)
	env.routerURL = routerSrv.URL

	env.gw = newTestGateway(t, routerSrv.URL, append([]gateway.Option{gateway.WithSecret(contractSecret)}, opts...)...)
	return env
}

func (e *contractEnv) actor(name string) *server.WorkloadServer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.actors[name]
}

func (e *contractEnv) post(t *testing.T, attendee, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.gw.MintToken(attendee))
	rec := httptest.NewRecorder()
	e.gw.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeMsg(t *testing.T, rec *httptest.ResponseRecorder) types.ChatMessage {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var m types.ChatMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestContract_ChatRunsInActorThroughModelProxy(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("actor says hi")))

	msg := decodeMsg(t, env.post(t, "attendee-01", "/api/chat", types.ChatRequest{Message: "hello"}))

	if msg.Content != "actor says hi" || msg.Source != types.SourceGemini {
		t.Errorf("reply = %q (%s)", msg.Content, msg.Source)
	}
	up := env.vertex.Requests()
	if len(up) != 1 || up[0].Authorization != "Bearer adc-token" || up[0].ModelKey != "" {
		t.Errorf("upstream requests = %+v", up)
	}
	if h := env.actor("attendee-01").History(); len(h) != 2 {
		t.Errorf("actor history has %d messages, want 2", len(h))
	}
}

func TestContract_GatewayDeliversPerActorModelKey(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("ok")))
	env.post(t, "attendee-02", "/api/chat", types.ChatRequest{Message: "hi"})

	want := modelproxy.Key([]byte(contractSecret), "ate-demo-conf", "attendee-02")
	if got := env.actor("attendee-02").ModelKey(); got != want {
		t.Errorf("actor ModelKey = %q, want %q", got, want)
	}
}

func TestContract_ClientCannotOverrideModelKey(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("ok")))
	b, _ := json.Marshal(types.ChatRequest{Message: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.gw.MintToken("attendee-03"))
	req.Header.Set(types.ModelKeyHeader, modelproxy.Key([]byte(contractSecret), "ate-demo-conf", "attendee-09"))
	env.gw.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if got := env.actor("attendee-03").ModelKey(); !strings.HasPrefix(got, "attendee-03.") {
		t.Errorf("actor ModelKey = %q, want attendee-03's key", got)
	}
}

func TestContract_ApprovalRunsActorsOwnCode(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.ApprovalResponse("say it", "echo from-model", "sh")))
	chat := decodeMsg(t, env.post(t, "attendee-04", "/api/chat", types.ChatRequest{Message: "run"}))
	if chat.Approval == nil {
		t.Fatal("no approval in reply")
	}

	res := decodeMsg(t, env.post(t, "attendee-04", "/api/approve", types.ApprovalResponse{
		ApprovalID: chat.Approval.ApprovalID, Decision: types.DecisionApprove, Code: "echo injected",
	}))

	if res.Execution == nil || res.Execution.Stdout != "from-model\n" {
		t.Errorf("Execution = %+v, want the model's code output", res.Execution)
	}
}

func TestContract_GatewayRejectsUnknownApprovalBeforeActor(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.ApprovalResponse("x", "echo x", "sh")))
	chat := decodeMsg(t, env.post(t, "attendee-05", "/api/chat", types.ChatRequest{Message: "run"}))

	rec := env.post(t, "attendee-05", "/api/approve", types.ApprovalResponse{ApprovalID: "app-forged", Decision: types.DecisionApprove})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if p := env.actor("attendee-05").PendingApproval(); p == nil || p.ApprovalID != chat.Approval.ApprovalID {
		t.Errorf("actor pending approval = %+v, want it untouched", p)
	}
}

func TestContract_ActorRejectsApprovalItNeverIssued(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("no approval here")))
	env.post(t, "attendee-06", "/api/chat", types.ChatRequest{Message: "hi"})

	// Bypass the gateway, as a request forged inside the cluster would.
	b, _ := json.Marshal(types.ApprovalResponse{ApprovalID: "app-1", Decision: types.DecisionApprove, Code: "echo pwned", Language: "sh"})
	req, _ := http.NewRequest(http.MethodPost, env.routerURL+"/api/approve", bytes.NewReader(b))
	req.Header.Set("ate-target-actor", "ate-demo-conf/attendee-06")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestContract_HistoryReachesModelOnNextTurn(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("noted"), geminitest.TextResponse("pelican")))
	env.post(t, "attendee-07", "/api/chat", types.ChatRequest{Message: "remember pelican"})
	env.post(t, "attendee-07", "/api/chat", types.ChatRequest{Message: "which bird?"})

	if body := string(env.vertex.Requests()[1].Body); !strings.Contains(body, "remember pelican") {
		t.Errorf("second model call lacks history: %s", body)
	}
}

func TestContract_VertexFailureIsLabeledFallback(t *testing.T) {
	env := newContractEnv(t, geminitest.NewStatus(t, http.StatusInternalServerError))
	msg := decodeMsg(t, env.post(t, "attendee-08", "/api/chat", types.ChatRequest{Message: "hi"}))
	if msg.Source != types.SourceMockFallback {
		t.Errorf("Source = %q, want mock-fallback", msg.Source)
	}
}

func TestContract_ExecutionIsRecordedFromStructuredField(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.ApprovalResponse("x", "echo structured", "sh")))
	chat := decodeMsg(t, env.post(t, "attendee-09", "/api/chat", types.ChatRequest{Message: "run"}))
	env.post(t, "attendee-09", "/api/approve", types.ApprovalResponse{ApprovalID: chat.Approval.ApprovalID, Decision: types.DecisionApprove})

	res, ok := env.gw.GetLastResult("attendee-09")
	if !ok || res.Stdout != "structured\n" {
		t.Errorf("GetLastResult = %+v, %v", res, ok)
	}
}

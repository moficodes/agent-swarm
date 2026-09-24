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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/pkg/ai"
	"github.com/agent-substrate/substrate/demos/conference/pkg/engine"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// mockSuspender records suspension invocations for assertions.
type mockSuspender struct {
	mu           sync.Mutex
	invocations  []suspendCall
	suspendDelay time.Duration
}

type suspendCall struct {
	Atespace string
	ActorID  string
}

func (m *mockSuspender) SuspendActor(ctx context.Context, atespace, actorID string) error {
	if m.suspendDelay > 0 {
		time.Sleep(m.suspendDelay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invocations = append(m.invocations, suspendCall{
		Atespace: atespace,
		ActorID:  actorID,
	})
	return nil
}

func (m *mockSuspender) getCalls() []suspendCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]suspendCall, len(m.invocations))
	copy(res, m.invocations)
	return res
}

func (m *mockSuspender) waitForCalls(n int, timeout time.Duration) []suspendCall {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		calls := m.getCalls()
		if len(calls) >= n {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
	return m.getCalls()
}

type mockSimulationController struct {
	stopped atomic.Bool
	running atomic.Bool
	count   atomic.Int64
}

func (m *mockSimulationController) Start(count int) error {
	m.stopped.Store(false)
	m.running.Store(true)
	m.count.Store(int64(count))
	return nil
}

func (m *mockSimulationController) Stop() {
	m.stopped.Store(true)
	m.running.Store(false)
}

func (m *mockSimulationController) Status() (bool, int) {
	if m.stopped.Load() {
		return false, 0
	}
	return m.running.Load(), int(m.count.Load())
}

func (m *mockSimulationController) SetStatus(running bool, count int) {
	m.stopped.Store(!running)
	m.running.Store(running)
	m.count.Store(int64(count))
}

func TestSessionMinting(t *testing.T) {
	secret := "test-secret-12345"
	gw := newTestGateway(t, "http://localhost:8000", "",
		gateway.WithJWTSecret(secret),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// 1. Verify session generation and sequential IDs
	resp1, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to request /api/session: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp1.StatusCode)
	}

	var sess1 struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	if err := json.NewDecoder(resp1.Body).Decode(&sess1); err != nil {
		t.Fatalf("failed to decode session response: %v", err)
	}

	if sess1.AttendeeID != "attendee-01" {
		t.Errorf("expected attendee_id 'attendee-01', got %q", sess1.AttendeeID)
	}
	if sess1.Token == "" {
		t.Fatalf("expected non-empty token")
	}

	// Verify token validity with gateway verification method
	claims, err := gw.VerifyToken(sess1.Token)
	if err != nil {
		t.Fatalf("token verification failed: %v", err)
	}
	if claims.Sub != "attendee-01" {
		t.Errorf("expected claims.Sub 'attendee-01', got %q", claims.Sub)
	}
	if claims.Exp <= time.Now().Unix() {
		t.Errorf("expected future expiration, got %d", claims.Exp)
	}

	// 2. Verify second session produces sequential ID attendee-02
	resp2, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to request second session: %v", err)
	}
	defer resp2.Body.Close()

	var sess2 struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&sess2); err != nil {
		t.Fatalf("failed to decode second session: %v", err)
	}
	if sess2.AttendeeID != "attendee-02" {
		t.Errorf("expected attendee_id 'attendee-02', got %q", sess2.AttendeeID)
	}

	// 3. Verify tampered token fails verification
	tampered := sess1.Token + "extra"
	if _, err := gw.VerifyToken(tampered); err == nil {
		t.Errorf("expected error verifying tampered token, got nil")
	}

	// 4. Verify token signed with different secret fails
	gwOther := newTestGateway(t, "http://localhost:8000", "",
		gateway.WithJWTSecret("wrong-secret"),
	)
	defer gwOther.Close()
	if _, err := gwOther.VerifyToken(sess1.Token); err == nil {
		t.Errorf("expected error verifying token with wrong secret, got nil")
	}
}

func TestStageTelemetry(t *testing.T) {
	// Mock statusz server
	var parkingActive atomic.Int64
	parkingActive.Store(7)

	statuszMux := http.NewServeMux()
	statuszMux.HandleFunc("/statusz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"parking": map[string]any{
				"active": parkingActive.Load(),
			},
		})
	})
	statusSrv := httptest.NewServer(statuszMux)
	defer statusSrv.Close()

	gw := newTestGateway(t, "http://localhost:8000", statusSrv.URL,
		gateway.WithStatusInterval(20*time.Millisecond),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Wait for poller to run at least one tick
	deadline := time.Now().Add(2 * time.Second)
	var telem types.StageTelemetry
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/stage/telemetry")
		if err != nil {
			t.Fatalf("failed to get telemetry: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&telem); err != nil {
			t.Fatalf("failed to decode telemetry: %v", err)
		}
		_ = resp.Body.Close()

		if telem.ParkedRequests == 7 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if telem.ParkedRequests != 7 {
		t.Fatalf("expected parked_requests=7, got %d", telem.ParkedRequests)
	}

	if len(telem.Workers) == 0 {
		t.Errorf("expected worker status entries in telemetry")
	}
	if telem.Actors == nil {
		t.Errorf("expected non-nil actors map")
	}
	if telem.RecentEvents == nil {
		t.Errorf("expected non-nil recent_events slice")
	}

	// Update parking active and ensure polling reflects it
	parkingActive.Store(23)
	updated := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/stage/telemetry")
		if err != nil {
			t.Fatalf("failed to get telemetry: %v", err)
		}
		if err := json.NewDecoder(resp.Body).Decode(&telem); err != nil {
			t.Fatalf("failed to decode telemetry: %v", err)
		}
		_ = resp.Body.Close()

		if telem.ParkedRequests == 23 {
			updated = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !updated {
		t.Errorf("expected telemetry parked_requests to update to 23, got %d", telem.ParkedRequests)
	}
}

func TestProxyRouting_HeadersAndSuspension(t *testing.T) {
	suspender := &mockSuspender{}
	secretKey := "gemini-api-key-xyz"
	atespace := "ate-demo-conf"

	var receivedTargetActor atomic.Pointer[string]
	var receivedAPIKey atomic.Pointer[string]
	var receivedBody atomic.Pointer[string]

	// Upstream router/workload mock
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		hdrActor := r.Header.Get("ate-target-actor")
		hdrKey := r.Header.Get("X-Gemini-API-Key")
		bodyBytes, _ := io.ReadAll(r.Body)

		sActor := hdrActor
		sKey := hdrKey
		sBody := string(bodyBytes)
		receivedTargetActor.Store(&sActor)
		receivedAPIKey.Store(&sKey)
		receivedBody.Store(&sBody)

		// Check if request is asking for approval
		if strings.Contains(sBody, "need-approval") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ChatMessage{
				ID:        "msg-wait-1",
				Sender:    types.SenderAssistant,
				Content:   "WAITING_FOR_APPROVAL: review proposed script",
				Timestamp: time.Now().Unix(),
				Approval: &types.ApprovalRequest{
					ApprovalID: "app-123",
					Summary:    "Run telemetry analysis",
					Code:       "print(42)",
					Language:   "python",
					RiskLevel:  types.RiskHigh,
				},
			})
			return
		}

		// Normal chat response
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-normal-1",
			Sender:    types.SenderAssistant,
			Content:   "Normal analysis complete.",
			Timestamp: time.Now().Unix(),
		})
	})

	upstreamMux.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		hdrActor := r.Header.Get("ate-target-actor")
		hdrKey := r.Header.Get("X-Gemini-API-Key")
		bodyBytes, _ := io.ReadAll(r.Body)

		sActor := hdrActor
		sKey := hdrKey
		sBody := string(bodyBytes)
		receivedTargetActor.Store(&sActor)
		receivedAPIKey.Store(&sKey)
		receivedBody.Store(&sBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-approved-1",
			Sender:    types.SenderAssistant,
			Content:   "Execution completed in 24ms (exit code 0):\n42",
			Timestamp: time.Now().Unix(),
		})
	})

	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithAtespace(atespace),
		gateway.WithGeminiAPIKey(secretKey),
		gateway.WithSuspender(suspender),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Provision session for attendee-01
	sessResp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	defer sessResp.Body.Close()
	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sess)

	// Case 1: Chat request triggering WAITING_FOR_APPROVAL
	chatReqBody := `{"message":"need-approval for calculation"}`
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", bytes.NewBufferString(chatReqBody))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer "+sess.Token)

	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("failed to send chat request: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp1.StatusCode)
	}

	var chatResp types.ChatMessage
	if err := json.NewDecoder(resp1.Body).Decode(&chatResp); err != nil {
		t.Fatalf("failed to decode chat response: %v", err)
	}
	if chatResp.Approval == nil || chatResp.Approval.ApprovalID != "app-123" {
		t.Errorf("expected approval payload with ID app-123, got %+v", chatResp.Approval)
	}

	// Verify headers injected to upstream
	if receivedTargetActor.Load() == nil || *receivedTargetActor.Load() != "ate-demo-conf/attendee-01" {
		t.Errorf("expected ate-target-actor 'ate-demo-conf/attendee-01', got %v", receivedTargetActor.Load())
	}
	if receivedAPIKey.Load() == nil || *receivedAPIKey.Load() != secretKey {
		t.Errorf("expected X-Gemini-API-Key %q, got %v", secretKey, receivedAPIKey.Load())
	}

	// Verify suspension hook was called for attendee-01
	calls := suspender.waitForCalls(1, 2*time.Second)
	if len(calls) == 0 {
		t.Fatalf("expected suspension hook to be called on WAITING_FOR_APPROVAL")
	}
	lastCall := calls[len(calls)-1]
	if lastCall.Atespace != atespace || lastCall.ActorID != "attendee-01" {
		t.Errorf("expected suspendCall{%q, 'attendee-01'}, got %+v", atespace, lastCall)
	}

	// Verify actor state is StateAwaitingApproval
	telemResp, err := http.Get(ts.URL + "/api/stage/telemetry")
	if err != nil {
		t.Fatalf("failed to get telemetry: %v", err)
	}
	var telem types.StageTelemetry
	_ = json.NewDecoder(telemResp.Body).Decode(&telem)
	_ = telemResp.Body.Close()

	if state, ok := telem.Actors["attendee-01"]; !ok || state != types.StateAwaitingApproval {
		t.Errorf("expected actor attendee-01 state %s, got %s (present: %v)", types.StateAwaitingApproval, state, ok)
	}

	// Case 2: Approve request completion
	approveReqBody := `{"approval_id":"app-123","decision":"APPROVE"}`
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/approve", bytes.NewBufferString(approveReqBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+sess.Token)

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("failed to send approve request: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp2.StatusCode)
	}

	// Verify suspension called upon completion
	callsAfterApprove := suspender.waitForCalls(2, 2*time.Second)
	if len(callsAfterApprove) < 2 {
		t.Fatalf("expected suspension hook to be called after approval completion")
	}
	lastApproveCall := callsAfterApprove[len(callsAfterApprove)-1]
	if lastApproveCall.ActorID != "attendee-01" {
		t.Errorf("expected suspend call for attendee-01, got %+v", lastApproveCall)
	}

	// Verify actor state transitioned to StateSuspended
	telemResp2, err := http.Get(ts.URL + "/api/stage/telemetry")
	if err != nil {
		t.Fatalf("failed to get telemetry after approve: %v", err)
	}
	var telem2 types.StageTelemetry
	_ = json.NewDecoder(telemResp2.Body).Decode(&telem2)
	_ = telemResp2.Body.Close()

	if state, ok := telem2.Actors["attendee-01"]; !ok || state != types.StateSuspended {
		t.Errorf("expected actor attendee-01 state %s, got %s", types.StateSuspended, state)
	}
}

func TestProxyRouting_Authentication(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "",
		gateway.WithJWTSecret("secret-auth-test"),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// 1. Missing Authorization returns 401
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing auth, got %d", resp1.StatusCode)
	}

	// 2. Invalid/Malformed Bearer token returns 401
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer invalid.jwt.token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid token, got %d", resp2.StatusCode)
	}

	// 3. Token with mismatched X-Attendee-ID returns 401
	sessResp, _ := http.Get(ts.URL + "/api/session")
	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sess)
	_ = sessResp.Body.Close()

	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Authorization", "Bearer "+sess.Token)
	req3.Header.Set("X-Attendee-ID", "attendee-99") // mismatch!
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for mismatched X-Attendee-ID, got %d", resp3.StatusCode)
	}
}

func TestGateway_ConcurrencySafety(t *testing.T) {
	statuszMux := http.NewServeMux()
	statuszMux.HandleFunc("/statusz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"parking": map[string]any{"active": 3},
		})
	})
	statusSrv := httptest.NewServer(statuszMux)
	defer statusSrv.Close()

	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-concurrent",
			Sender:    types.SenderAssistant,
			Content:   "OK",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamMux.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-concurrent-app",
			Sender:    types.SenderAssistant,
			Content:   "Approved OK",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	suspender := &mockSuspender{}
	gw := newTestGateway(t, upstreamSrv.URL, statusSrv.URL,
		gateway.WithStatusInterval(10*time.Millisecond),
		gateway.WithSuspender(suspender),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	const numWorkers = 10
	const numIterations = 20

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			defer wg.Done()
			client := &http.Client{Timeout: 5 * time.Second}

			for j := 0; j < numIterations; j++ {
				// 1. Session mint
				sResp, err := client.Get(ts.URL + "/api/session")
				if err != nil {
					t.Errorf("worker %d session request failed: %v", workerID, err)
					return
				}
				var sess struct {
					AttendeeID string `json:"attendee_id"`
					Token      string `json:"token"`
				}
				_ = json.NewDecoder(sResp.Body).Decode(&sess)
				_ = sResp.Body.Close()

				// 2. Chat proxy
				reqChat, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"ping"}`))
				reqChat.Header.Set("Content-Type", "application/json")
				reqChat.Header.Set("Authorization", "Bearer "+sess.Token)
				cResp, err := client.Do(reqChat)
				if err != nil {
					t.Errorf("worker %d chat proxy failed: %v", workerID, err)
					return
				}
				_ = cResp.Body.Close()

				// 3. Approve proxy
				reqApp, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/approve", strings.NewReader(`{"approval_id":"app-1","decision":"approve"}`))
				reqApp.Header.Set("Content-Type", "application/json")
				reqApp.Header.Set("Authorization", "Bearer "+sess.Token)
				aResp, err := client.Do(reqApp)
				if err != nil {
					t.Errorf("worker %d approve proxy failed: %v", workerID, err)
					return
				}
				_ = aResp.Body.Close()

				// 4. Telemetry
				tResp, err := client.Get(ts.URL + "/api/stage/telemetry")
				if err != nil {
					t.Errorf("worker %d telemetry failed: %v", workerID, err)
					return
				}
				var telem types.StageTelemetry
				_ = json.NewDecoder(tResp.Body).Decode(&telem)
				_ = tResp.Body.Close()
			}
		}(i)
	}

	wg.Wait()
}

func TestProxyRouting_ExpiredToken(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "",
		gateway.WithJWTSecret("secret-expired-test"),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Mint token and manually construct expired token
	header := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
	// Expired payload in past: {"sub":"attendee-01","exp":1000}
	payload := "eyJzdWIiOiJhdHRlbmRlZS0wMSIsImV4cCI6MTAwMH0"
	tamperedToken := header + "." + payload + "." + "invalid-sig"

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tamperedToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for expired/invalid token, got %d", resp.StatusCode)
	}
}

func TestHTTPMethodValidation(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// 1. POST to /api/session should return 405 Method Not Allowed
	resp1, err := http.Post(ts.URL+"/api/session", "application/json", nil)
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST /api/session, got %d", resp1.StatusCode)
	}

	// 2. POST to /api/stage/telemetry should return 405 Method Not Allowed
	resp2, err := http.Post(ts.URL+"/api/stage/telemetry", "application/json", nil)
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST /api/stage/telemetry, got %d", resp2.StatusCode)
	}

	// 3. GET to /api/chat should return 405 Method Not Allowed
	resp3, err := http.Get(ts.URL + "/api/chat")
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET /api/chat, got %d", resp3.StatusCode)
	}

	// 4. GET to /api/approve should return 405 Method Not Allowed
	resp4, err := http.Get(ts.URL + "/api/approve")
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET /api/approve, got %d", resp4.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("failed to request /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for /healthz, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "OK" {
		t.Errorf("expected body 'OK', got %q", string(body))
	}
}

func TestSuspenderFailureTolerance(t *testing.T) {
	failingSuspender := gateway.SuspendFunc(func(ctx context.Context, atespace, actorID string) error {
		return errors.New("simulated kubectl-ate network failure")
	})

	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-resilient-1",
			Sender:    types.SenderAssistant,
			Content:   "Processed despite suspender error",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithSuspender(failingSuspender),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	sessResp, _ := http.Get(ts.URL + "/api/session")
	var sess struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sess)
	_ = sessResp.Body.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Client still receives 200 OK chat response
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 despite suspender failure, got %d", resp.StatusCode)
	}

	// Telemetry contains warning event
	foundWarning := false
	deadline := time.Now().Add(2 * time.Second)
	var telem types.StageTelemetry
	for time.Now().Before(deadline) {
		telemResp, err := http.Get(ts.URL + "/api/stage/telemetry")
		if err == nil {
			_ = json.NewDecoder(telemResp.Body).Decode(&telem)
			_ = telemResp.Body.Close()
			for _, ev := range telem.RecentEvents {
				if strings.Contains(ev, "Warning: suspend actor") {
					foundWarning = true
					break
				}
			}
		}
		if foundWarning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !foundWarning {
		t.Errorf("expected warning event logged in telemetry, events: %+v", telem.RecentEvents)
	}
}

func TestProxyRouting_ApprovalDetection_SubstringIgnored(t *testing.T) {
	suspender := &mockSuspender{}

	// Upstream returns WAITING_FOR_APPROVAL substring in content, but Approval field is nil
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-raw-substring",
			Sender:    types.SenderAssistant,
			Content:   "Here is how WAITING_FOR_APPROVAL works in our system docs.",
			Timestamp: time.Now().Unix(),
			Approval:  nil, // No structured approval!
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithSuspender(suspender),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	sessResp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	defer sessResp.Body.Close()
	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sess)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	state, ok := gw.GetActorState(sess.AttendeeID)
	if !ok {
		t.Fatalf("actor %s not found in activeActors", sess.AttendeeID)
	}
	if state != types.StateSuspended {
		t.Errorf("expected actor %s state %s (normal completed turn), but got %s", sess.AttendeeID, types.StateSuspended, state)
	}
}

func TestProxyRouting_RequireValidToken(t *testing.T) {
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-valid-1",
			Sender:    types.SenderAssistant,
			Content:   "OK",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithAtespace("ate-demo-conf"),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// 1. Request with only X-Attendee-ID header (no token) MUST be rejected with 401
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello standalone"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Attendee-ID", "attendee-42")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for standalone X-Attendee-ID, got %d", resp.StatusCode)
	}

	// 2. Request with valid token via X-Attendee-Token should succeed
	sessResp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	defer sessResp.Body.Close()
	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sess)

	reqToken, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello token"}`))
	reqToken.Header.Set("Content-Type", "application/json")
	reqToken.Header.Set("X-Attendee-Token", sess.Token)
	reqToken.Header.Set("X-Attendee-ID", sess.AttendeeID)

	respToken, err := http.DefaultClient.Do(reqToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respToken.Body.Close()

	if respToken.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for valid X-Attendee-Token, got %d", respToken.StatusCode)
	}
}

func TestProxyRouting_ProxyErrorResetsActorState(t *testing.T) {
	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadServer.URL
	deadServer.Close()

	gw := newTestGateway(t, deadURL, "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	sessResp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	defer sessResp.Body.Close()

	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	if err := json.NewDecoder(sessResp.Body).Decode(&sess); err != nil {
		t.Fatalf("failed to decode session: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected status 502 Bad Gateway, got %d", resp.StatusCode)
	}

	telemResp, err := http.Get(ts.URL + "/api/stage/telemetry")
	if err != nil {
		t.Fatalf("failed to get telemetry: %v", err)
	}
	defer telemResp.Body.Close()

	var telem types.StageTelemetry
	if err := json.NewDecoder(telemResp.Body).Decode(&telem); err != nil {
		t.Fatalf("failed to decode telemetry: %v", err)
	}

	if state, ok := telem.Actors[sess.AttendeeID]; !ok || state != types.StateSuspended {
		t.Errorf("expected actor %s state %s after proxy error, got %s (present: %v)",
			sess.AttendeeID, types.StateSuspended, state, ok)
	}
}

func TestProxyRouting_UpstreamErrorStatusResetsActorState(t *testing.T) {
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Worker Crash", http.StatusInternalServerError)
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	sessResp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	defer sessResp.Body.Close()

	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	if err := json.NewDecoder(sessResp.Body).Decode(&sess); err != nil {
		t.Fatalf("failed to decode session: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"trigger error"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", resp.StatusCode)
	}

	telemResp, err := http.Get(ts.URL + "/api/stage/telemetry")
	if err != nil {
		t.Fatalf("failed to get telemetry: %v", err)
	}
	defer telemResp.Body.Close()

	var telem types.StageTelemetry
	if err := json.NewDecoder(telemResp.Body).Decode(&telem); err != nil {
		t.Fatalf("failed to decode telemetry: %v", err)
	}

	if state, ok := telem.Actors[sess.AttendeeID]; !ok || state != types.StateSuspended {
		t.Errorf("expected actor %s state %s after upstream 500 error, got %s (present: %v)",
			sess.AttendeeID, types.StateSuspended, state, ok)
	}
}

func TestProxyRouting_AsyncSuspensionNonBlocking(t *testing.T) {
	suspender := &mockSuspender{
		suspendDelay: 150 * time.Millisecond,
	}

	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-async-1",
			Sender:    types.SenderAssistant,
			Content:   "Done",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithSuspender(suspender),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	sessResp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	defer sessResp.Body.Close()

	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	if err := json.NewDecoder(sessResp.Body).Decode(&sess); err != nil {
		t.Fatalf("failed to decode session: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.Token)

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	if duration >= 140*time.Millisecond {
		t.Errorf("expected async suspension not to block HTTP response, but request took %v", duration)
	}

	calls := suspender.waitForCalls(1, 1*time.Second)
	if len(calls) == 0 {
		t.Fatalf("expected async suspension call to complete in background")
	}
	if calls[0].ActorID != sess.AttendeeID {
		t.Errorf("expected actor ID %q, got %+v", sess.AttendeeID, calls[0])
	}
}

func TestStageTelemetry_DeterministicWorkerSorting(t *testing.T) {
	hold := make(chan struct{})
	var enteredCount atomic.Int32

	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		enteredCount.Add(1)
		<-hold
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-sort",
			Sender:    types.SenderAssistant,
			Content:   "done",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Provision sessions for 3 attendees
	tokens := make(map[string]string)
	for i := 0; i < 3; i++ {
		resp, err := http.Get(ts.URL + "/api/session")
		if err != nil {
			t.Fatalf("failed to get session: %v", err)
		}
		var sess struct {
			AttendeeID string `json:"attendee_id"`
			Token      string `json:"token"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&sess)
		_ = resp.Body.Close()
		tokens[sess.AttendeeID] = sess.Token
	}

	var reqWg sync.WaitGroup
	for _, id := range []string{"attendee-03", "attendee-01", "attendee-02"} {
		reqWg.Add(1)
		go func(attID, tok string) {
			defer reqWg.Done()
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hi"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}(id, tokens[id])
	}

	// Wait until all 3 requests are actively being processed upstream
	deadline := time.Now().Add(2 * time.Second)
	for enteredCount.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if enteredCount.Load() < 3 {
		close(hold)
		reqWg.Wait()
		t.Fatalf("timed out waiting for 3 requests to enter upstream, got %d", enteredCount.Load())
	}

	// Check telemetry multiple times: runningActors must be sorted, so Workers[0] is attendee-01 and Workers[1] is attendee-02
	for i := 0; i < 15; i++ {
		resp, err := http.Get(ts.URL + "/api/stage/telemetry")
		if err != nil {
			close(hold)
			reqWg.Wait()
			t.Fatalf("failed to get telemetry: %v", err)
		}
		var telem types.StageTelemetry
		_ = json.NewDecoder(resp.Body).Decode(&telem)
		_ = resp.Body.Close()

		if len(telem.Workers) < 2 {
			close(hold)
			reqWg.Wait()
			t.Fatalf("expected 2 workers")
		}
		if telem.Workers[0].ActiveActor != "attendee-01" || telem.Workers[1].ActiveActor != "attendee-02" {
			close(hold)
			reqWg.Wait()
			t.Fatalf("telemetry not deterministically sorted: worker0=%s, worker1=%s",
				telem.Workers[0].ActiveActor, telem.Workers[1].ActiveActor)
		}
	}

	close(hold)
	reqWg.Wait()
}

func TestGateway_CloseWaitsForAsyncSuspension(t *testing.T) {
	suspender := &mockSuspender{
		suspendDelay: 60 * time.Millisecond,
	}

	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-close-test",
			Sender:    types.SenderAssistant,
			Content:   "Finished turn",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithSuspender(suspender),
	)

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	sessResp, err := http.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	var sess struct {
		AttendeeID string `json:"attendee_id"`
		Token      string `json:"token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sess)
	_ = sessResp.Body.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	// Immediately close the gateway. Close() should block until asyncSuspendActor finishes!
	gw.Close()

	calls := suspender.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected Close() to wait for async suspension to complete, but got %d calls", len(calls))
	}
	if calls[0].ActorID != sess.AttendeeID {
		t.Errorf("expected suspend call for %s, got %s", sess.AttendeeID, calls[0].ActorID)
	}
}

func TestChatRoute(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// 1. GET /chat returns 200 with HTML content and required DOM elements
	resp, err := http.Get(ts.URL + "/chat")
	if err != nil {
		t.Fatalf("failed to request /chat: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /chat, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected Content-Type to contain text/html, got %q", contentType)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	body := string(bodyBytes)

	expectedElements := []string{
		`id="chat-feed"`,
		`id="prompt-input"`,
		`id="send-btn"`,
		`id="status-pill"`,
		`id="attendee-badge"`,
		`Gemini Flash`,
		`Anomaly Analysis`,
		`Security Audit`,
		`Benchmark Sandbox`,
		`approval-card`,
		`btn-reject`,
		`btn-approve`,
	}
	for _, el := range expectedElements {
		if !strings.Contains(body, el) {
			t.Errorf("expected HTML to contain %q, but was missing", el)
		}
	}

	// 2. GET /static/chat.js returns 200 with JS content
	respJS, err := http.Get(ts.URL + "/static/chat.js")
	if err != nil {
		t.Fatalf("failed to request /static/chat.js: %v", err)
	}
	defer respJS.Body.Close()

	if respJS.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /static/chat.js, got %d", respJS.StatusCode)
	}
	jsBytes, _ := io.ReadAll(respJS.Body)
	jsBody := string(jsBytes)
	if !strings.Contains(jsBody, "/api/chat") || !strings.Contains(jsBody, "/api/approve") {
		t.Errorf("expected chat.js to contain /api/chat and /api/approve endpoints")
	}

	// 3. GET /static/chat.css returns 200 with CSS content
	respCSS, err := http.Get(ts.URL + "/static/chat.css")
	if err != nil {
		t.Fatalf("failed to request /static/chat.css: %v", err)
	}
	defer respCSS.Body.Close()

	if respCSS.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /static/chat.css, got %d", respCSS.StatusCode)
	}
	cssBytes, _ := io.ReadAll(respCSS.Body)
	cssBody := string(cssBytes)
	if !strings.Contains(cssBody, "chat-feed") || !strings.Contains(cssBody, "approval-card") {
		t.Errorf("expected chat.css to contain chat-feed and approval-card rules")
	}

	// 4. POST /chat should return 405 Method Not Allowed
	respPost, err := http.Post(ts.URL+"/chat", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to POST /chat: %v", err)
	}
	defer respPost.Body.Close()

	if respPost.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for POST /chat, got %d", respPost.StatusCode)
	}

	// 5. HEAD /chat should return 200 OK with text/html and empty body
	respHead, err := http.Head(ts.URL + "/chat")
	if err != nil {
		t.Fatalf("failed to HEAD /chat: %v", err)
	}
	defer respHead.Body.Close()

	if respHead.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for HEAD /chat, got %d", respHead.StatusCode)
	}
	if !strings.Contains(respHead.Header.Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html Content-Type for HEAD /chat, got %q", respHead.Header.Get("Content-Type"))
	}
	headBody, _ := io.ReadAll(respHead.Body)
	if len(headBody) != 0 {
		t.Errorf("expected empty body for HEAD /chat, got %d bytes", len(headBody))
	}

	// 6. GET /chat/nonexistent should return 404 Not Found (subtree bleed prevention)
	respSubtree, err := http.Get(ts.URL + "/chat/nonexistent")
	if err != nil {
		t.Fatalf("failed to GET /chat/nonexistent: %v", err)
	}
	defer respSubtree.Body.Close()

	if respSubtree.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for /chat/nonexistent, got %d", respSubtree.StatusCode)
	}

	// 7. GET /chat/ should return 200 OK
	respSlash, err := http.Get(ts.URL + "/chat/")
	if err != nil {
		t.Fatalf("failed to GET /chat/: %v", err)
	}
	defer respSlash.Body.Close()

	if respSlash.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for GET /chat/, got %d", respSlash.StatusCode)
	}

	// 8. POST /chat/ should return 405 Method Not Allowed
	respPostSlash, err := http.Post(ts.URL+"/chat/", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to POST /chat/: %v", err)
	}
	defer respPostSlash.Body.Close()

	if respPostSlash.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for POST /chat/, got %d", respPostSlash.StatusCode)
	}
}

func TestStageRoute(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	expectedElements := []string{
		`id="parking-meter"`,
		`id="worker-pod-0"`,
		`id="worker-pod-1"`,
		`id="fleet-grid"`,
		`id="event-ticker"`,
		`id="qr-code"`,
	}

	// 1. GET /stage returns 200 with HTML content and required DOM elements
	resp, err := http.Get(ts.URL + "/stage")
	if err != nil {
		t.Fatalf("failed to request /stage: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /stage, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected Content-Type to contain text/html, got %q", contentType)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	body := string(bodyBytes)

	for _, el := range expectedElements {
		if !strings.Contains(body, el) {
			t.Errorf("expected GET /stage HTML to contain %q, but was missing", el)
		}
	}

	// 2. GET / returns 200 with HTML content and required DOM elements
	respRoot, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("failed to request /: %v", err)
	}
	defer respRoot.Body.Close()

	if respRoot.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /, got %d", respRoot.StatusCode)
	}

	contentTypeRoot := respRoot.Header.Get("Content-Type")
	if !strings.Contains(contentTypeRoot, "text/html") {
		t.Errorf("expected Content-Type to contain text/html, got %q", contentTypeRoot)
	}

	bodyBytesRoot, err := io.ReadAll(respRoot.Body)
	if err != nil {
		t.Fatalf("failed to read root response body: %v", err)
	}
	bodyRoot := string(bodyBytesRoot)

	for _, el := range expectedElements {
		if !strings.Contains(bodyRoot, el) {
			t.Errorf("expected GET / HTML to contain %q, but was missing", el)
		}
	}

	// 3. HEAD /stage should return 200 OK with text/html and empty body
	respHead, err := http.Head(ts.URL + "/stage")
	if err != nil {
		t.Fatalf("failed to HEAD /stage: %v", err)
	}
	defer respHead.Body.Close()

	if respHead.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for HEAD /stage, got %d", respHead.StatusCode)
	}
	if !strings.Contains(respHead.Header.Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html Content-Type for HEAD /stage, got %q", respHead.Header.Get("Content-Type"))
	}
	headBody, _ := io.ReadAll(respHead.Body)
	if len(headBody) != 0 {
		t.Errorf("expected empty body for HEAD /stage, got %d bytes", len(headBody))
	}

	// 4. Subtree path isolation: GET /stage/subpath should return 404 Not Found
	respSubtree, err := http.Get(ts.URL + "/stage/subpath")
	if err != nil {
		t.Fatalf("failed to GET /stage/subpath: %v", err)
	}
	defer respSubtree.Body.Close()

	if respSubtree.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for /stage/subpath, got %d", respSubtree.StatusCode)
	}

	// 5. POST /stage should return 405 Method Not Allowed
	respPost, err := http.Post(ts.URL+"/stage", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to POST /stage: %v", err)
	}
	defer respPost.Body.Close()

	if respPost.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for POST /stage, got %d", respPost.StatusCode)
	}

	// 6. GET /static/stage.js returns 200 with JS content
	respJS, err := http.Get(ts.URL + "/static/stage.js")
	if err != nil {
		t.Fatalf("failed to request /static/stage.js: %v", err)
	}
	defer respJS.Body.Close()

	if respJS.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /static/stage.js, got %d", respJS.StatusCode)
	}
	jsBytes, _ := io.ReadAll(respJS.Body)
	jsBody := string(jsBytes)
	if !strings.Contains(jsBody, "/api/stage/telemetry") || !strings.Contains(jsBody, "updateDashboard") {
		t.Errorf("expected stage.js to contain /api/stage/telemetry and updateDashboard")
	}

	// 7. GET /static/stage.css returns 200 with CSS content
	respCSS, err := http.Get(ts.URL + "/static/stage.css")
	if err != nil {
		t.Fatalf("failed to request /static/stage.css: %v", err)
	}
	defer respCSS.Body.Close()

	if respCSS.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /static/stage.css, got %d", respCSS.StatusCode)
	}
	cssBytes, _ := io.ReadAll(respCSS.Body)
	cssBody := string(cssBytes)
	if !strings.Contains(cssBody, "parking-meter") || !strings.Contains(cssBody, "fleet-grid") {
		t.Errorf("expected stage.css to contain parking-meter and fleet-grid rules")
	}

	// 8. GET /stage/ should return 200 OK
	respSlash, err := http.Get(ts.URL + "/stage/")
	if err != nil {
		t.Fatalf("failed to GET /stage/: %v", err)
	}
	defer respSlash.Body.Close()

	if respSlash.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for GET /stage/, got %d", respSlash.StatusCode)
	}

	// 9. POST /stage/ should return 405 Method Not Allowed
	respPostSlash, err := http.Post(ts.URL+"/stage/", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to POST /stage/: %v", err)
	}
	defer respPostSlash.Body.Close()

	if respPostSlash.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for POST /stage/, got %d", respPostSlash.StatusCode)
	}
}

func TestSSEEventStreaming(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	t.Run("MissingAttendeeID", func(t *testing.T) {
		// Reject missing attendee_id with 400
		resp, err := http.Get(ts.URL + "/api/events")
		if err != nil {
			t.Fatalf("failed to request /api/events: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for missing attendee_id, got %d", resp.StatusCode)
		}

		// Reject empty attendee_id with 400
		respEmpty, err := http.Get(ts.URL + "/api/events?attendee_id=")
		if err != nil {
			t.Fatalf("failed to request /api/events?attendee_id=: %v", err)
		}
		defer respEmpty.Body.Close()
		if respEmpty.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for empty attendee_id, got %d", respEmpty.StatusCode)
		}
	})

	t.Run("EstablishAndReceive", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?attendee_id=attendee-01", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("failed to connect to SSE stream: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			t.Errorf("expected Content-Type text/event-stream, got %q", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("expected Cache-Control no-cache, got %q", cc)
		}

		reader := bufio.NewReader(resp.Body)

		// Broadcast event for attendee-01
		gw.BroadcastStateChange("attendee-01", types.StateThinkingVertexAI, "Thinking...", "Reasoning on Vertex AI")

		// Read event line
		eventChan := make(chan types.StateChangeEvent, 1)
		errChan := make(chan error, 1)

		go func() {
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					errChan <- err
					return
				}
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "data:") {
					dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					var ev types.StateChangeEvent
					if err := json.Unmarshal([]byte(dataStr), &ev); err != nil {
						errChan <- err
						return
					}
					if ev.State == types.StateThinkingVertexAI {
						eventChan <- ev
						return
					}
				}
			}
		}()

		select {
		case ev := <-eventChan:
			if ev.AttendeeID != "attendee-01" {
				t.Errorf("expected attendee_id attendee-01, got %q", ev.AttendeeID)
			}
			if ev.State != types.StateThinkingVertexAI {
				t.Errorf("expected state %s, got %s", types.StateThinkingVertexAI, ev.State)
			}
			if ev.Label != "Thinking..." {
				t.Errorf("expected label 'Thinking...', got %q", ev.Label)
			}
			if ev.Subtext != "Reasoning on Vertex AI" {
				t.Errorf("expected subtext 'Reasoning on Vertex AI', got %q", ev.Subtext)
			}
			if ev.Timestamp <= 0 {
				t.Errorf("expected positive timestamp, got %d", ev.Timestamp)
			}
		case err := <-errChan:
			t.Fatalf("error reading SSE stream: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for SSE event")
		}
	})

	t.Run("CleanDisconnect", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?attendee_id=attendee-disconnect", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("failed to connect: %v", err)
		}
		defer resp.Body.Close()

		// Verify subscribed
		if count := gw.SubscriberCount("attendee-disconnect"); count != 1 {
			t.Fatalf("expected 1 subscriber before disconnect, got %d", count)
		}

		// Cancel request context (client disconnect)
		cancel()

		// Wait for subscriber count to become 0
		deadline := time.Now().Add(2 * time.Second)
		cleanedUp := false
		for time.Now().Before(deadline) {
			if gw.SubscriberCount("attendee-disconnect") == 0 {
				cleanedUp = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		if !cleanedUp {
			t.Errorf("expected subscriber to be cleaned up after disconnect, count: %d",
				gw.SubscriberCount("attendee-disconnect"))
		}
	})

	t.Run("ConcurrencySafety", func(t *testing.T) {
		const numClients = 10
		const numBroadcasts = 20

		var wg sync.WaitGroup
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		for i := 0; i < numClients; i++ {
			wg.Add(1)
			attendeeID := fmt.Sprintf("attendee-conc-%d", i%3)
			go func(attID string) {
				defer wg.Done()
				clientCtx, clientCancel := context.WithTimeout(ctx, 500*time.Millisecond)
				defer clientCancel()

				req, err := http.NewRequestWithContext(clientCtx, http.MethodGet, ts.URL+"/api/events?attendee_id="+attID, nil)
				if err != nil {
					return
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return
				}
				defer resp.Body.Close()

				reader := bufio.NewReader(resp.Body)
				for {
					_, err := reader.ReadString('\n')
					if err != nil {
						return
					}
				}
			}(attendeeID)
		}

		// Concurrently broadcast
		for b := 0; b < numBroadcasts; b++ {
			attID := fmt.Sprintf("attendee-conc-%d", b%3)
			gw.BroadcastStateChange(attID, types.StateRunningGVisor, "Running", "Executing")
			time.Sleep(5 * time.Millisecond)
		}

		wg.Wait()
	})
}

func TestActorInspectorEndpoint(t *testing.T) {
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		sBody := string(bodyBytes)

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(sBody, "need-approval") {
			_ = json.NewEncoder(w).Encode(types.ChatMessage{
				ID:        "msg-pending-approval",
				Sender:    types.SenderAssistant,
				Content:   "WAITING_FOR_APPROVAL: Scale telemetry pods",
				Timestamp: time.Now().Unix(),
				Approval: &types.ApprovalRequest{
					ApprovalID: "app-inspector-1",
					Summary:    "Scale telemetry pods",
					Code:       "print('scaling pods')",
					Language:   "python",
					RiskLevel:  types.RiskHigh,
				},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-chat-ok",
			Sender:    types.SenderAssistant,
			Content:   "Telemetry inspection ready.",
			Timestamp: time.Now().Unix(),
		})
	})

	upstreamMux.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-approved-ok",
			Sender:    types.SenderAssistant,
			Content:   "Execution completed in 36ms (exit code 0):\nSystem audit passed",
			Timestamp: time.Now().Unix(),
		})
	})

	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithAtespace("ate-demo-conf"),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	t.Run("DefaultSuspendedActorInspection", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/actor/attendee-05")
		if err != nil {
			t.Fatalf("failed to request /api/actor/attendee-05: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		var details types.ActorDetails
		if err := json.NewDecoder(resp.Body).Decode(&details); err != nil {
			t.Fatalf("failed to decode ActorDetails: %v", err)
		}

		if details.ActorID != "attendee-05" {
			t.Errorf("expected actor_id attendee-05, got %q", details.ActorID)
		}
		if details.State != types.StateSuspended {
			t.Errorf("expected state %s, got %s", types.StateSuspended, details.State)
		}
		if !strings.Contains(details.SnapshotLocation, "bucket") || !strings.Contains(details.SnapshotLocation, "ate-demo-conf") {
			t.Errorf("expected snapshot_location to contain bucket and atespace path, got %q", details.SnapshotLocation)
		}
		if details.SnapshotSizeBytes == 0 {
			t.Errorf("expected non-zero snapshot_size_bytes, got %d", details.SnapshotSizeBytes)
		}
	})

	t.Run("PendingApprovalStateAndPayload", func(t *testing.T) {
		token := gw.MintToken("attendee-02")

		chatReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"need-approval"}`))
		chatReq.Header.Set("Content-Type", "application/json")
		chatReq.Header.Set("Authorization", "Bearer "+token)

		chatResp, err := http.DefaultClient.Do(chatReq)
		if err != nil {
			t.Fatalf("failed chat request: %v", err)
		}
		_ = chatResp.Body.Close()

		resp, err := http.Get(ts.URL + "/api/actor/attendee-02")
		if err != nil {
			t.Fatalf("failed to request /api/actor/attendee-02: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		var details types.ActorDetails
		if err := json.NewDecoder(resp.Body).Decode(&details); err != nil {
			t.Fatalf("failed to decode ActorDetails: %v", err)
		}

		if details.ActorID != "attendee-02" {
			t.Errorf("expected actor_id attendee-02, got %q", details.ActorID)
		}
		if details.State != types.StateAwaitingApproval {
			t.Errorf("expected state %s, got %s", types.StateAwaitingApproval, details.State)
		}
		if details.PendingApproval == nil {
			t.Fatalf("expected populated pending_approval, got nil")
		}
		if details.PendingApproval.ApprovalID != "app-inspector-1" {
			t.Errorf("expected approval_id app-inspector-1, got %q", details.PendingApproval.ApprovalID)
		}
		if details.PendingApproval.Summary != "Scale telemetry pods" {
			t.Errorf("expected summary 'Scale telemetry pods', got %q", details.PendingApproval.Summary)
		}
	})

	t.Run("ExecutionHistoryAndLastResult", func(t *testing.T) {
		token := gw.MintToken("attendee-01")

		chatReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
		chatReq.Header.Set("Content-Type", "application/json")
		chatReq.Header.Set("Authorization", "Bearer "+token)
		cResp, err := http.DefaultClient.Do(chatReq)
		if err != nil {
			t.Fatalf("failed chat request: %v", err)
		}
		_ = cResp.Body.Close()

		appReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/approve", strings.NewReader(`{"approval_id":"app-inspector-1","decision":"APPROVE"}`))
		appReq.Header.Set("Content-Type", "application/json")
		appReq.Header.Set("Authorization", "Bearer "+token)
		aResp, err := http.DefaultClient.Do(appReq)
		if err != nil {
			t.Fatalf("failed approve request: %v", err)
		}
		_ = aResp.Body.Close()

		resp, err := http.Get(ts.URL + "/api/actor/attendee-01")
		if err != nil {
			t.Fatalf("failed to request /api/actor/attendee-01: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		var details types.ActorDetails
		if err := json.NewDecoder(resp.Body).Decode(&details); err != nil {
			t.Fatalf("failed to decode ActorDetails: %v", err)
		}

		if details.ActorID != "attendee-01" {
			t.Errorf("expected actor_id attendee-01, got %q", details.ActorID)
		}
		if len(details.ChatHistory) == 0 {
			t.Errorf("expected non-empty chat_history")
		}
		if details.LastResult == nil {
			t.Fatalf("expected populated last_result, got nil")
		}
		if details.LastResult.ExitCode != 0 {
			t.Errorf("expected exit_code 0, got %d", details.LastResult.ExitCode)
		}
		if details.LastResult.DurationMs != 36 {
			t.Errorf("expected duration_ms 36, got %d", details.LastResult.DurationMs)
		}
		if !strings.Contains(details.LastResult.Stdout, "System audit passed") {
			t.Errorf("expected stdout to contain 'System audit passed', got %q", details.LastResult.Stdout)
		}
	})

	t.Run("MethodValidation", func(t *testing.T) {
		methods := []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}
		for _, m := range methods {
			req, _ := http.NewRequest(m, ts.URL+"/api/actor/attendee-01", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed %s request: %v", m, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("expected 405 Method Not Allowed for %s, got %d", m, resp.StatusCode)
			}
		}

		// HEAD is allowed and returns 200 with empty body
		headReq, _ := http.NewRequest(http.MethodHead, ts.URL+"/api/actor/attendee-01", nil)
		headResp, err := http.DefaultClient.Do(headReq)
		if err != nil {
			t.Fatalf("failed HEAD request: %v", err)
		}
		defer headResp.Body.Close()
		if headResp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for HEAD, got %d", headResp.StatusCode)
		}
		headBody, _ := io.ReadAll(headResp.Body)
		if len(headBody) != 0 {
			t.Errorf("expected empty body for HEAD, got %d bytes", len(headBody))
		}
	})

	t.Run("EmptyOrRootActorPath", func(t *testing.T) {
		paths := []string{"/api/actor/", "/api/actor"}
		for _, p := range paths {
			resp, err := http.Get(ts.URL + p)
			if err != nil {
				t.Fatalf("failed GET %s: %v", p, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for %s, got %d", p, resp.StatusCode)
			}
		}
	})

	t.Run("ConcurrencySafety", func(t *testing.T) {
		const numWorkers = 8
		const numIterations = 20

		var wg sync.WaitGroup
		wg.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go func(workerID int) {
				defer wg.Done()
				client := &http.Client{Timeout: 5 * time.Second}

				for j := 0; j < numIterations; j++ {
					actorID := fmt.Sprintf("attendee-%02d", (workerID+j)%5+1)
					resp, err := client.Get(ts.URL + "/api/actor/" + actorID)
					if err != nil {
						t.Errorf("concurrent GET /api/actor/%s failed: %v", actorID, err)
						return
					}
					if resp.StatusCode != http.StatusOK {
						t.Errorf("concurrent GET /api/actor/%s returned status %d", actorID, resp.StatusCode)
					}
					var details types.ActorDetails
					_ = json.NewDecoder(resp.Body).Decode(&details)
					_ = resp.Body.Close()

					if details.ActorID != actorID {
						t.Errorf("expected actor_id %s, got %s", actorID, details.ActorID)
					}
				}
			}(i)
		}

		wg.Wait()
	})
}

func TestResetEndpoint(t *testing.T) {
	suspender := &mockSuspender{}
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-chat-1",
			Sender:    types.SenderAssistant,
			Content:   "WAITING_FOR_APPROVAL: review",
			Timestamp: time.Now().Unix(),
			Approval: &types.ApprovalRequest{
				ApprovalID: "app-reset-1",
				Summary:    "Scale cluster",
				Code:       "print('scaling')",
				Language:   "python",
				RiskLevel:  types.RiskHigh,
			},
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithAtespace("ate-demo-conf"),
		gateway.WithSuspender(suspender),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	t.Run("ResetSuccess", func(t *testing.T) {
		// Populate attendee-02 with pending approval and chat history via chat call
		token2 := gw.MintToken("attendee-02")
		req2, err := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"need-approval"}`))
		if err != nil {
			t.Fatalf("failed to create chat request: %v", err)
		}
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Authorization", "Bearer "+token2)
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("failed chat request: %v", err)
		}
		_ = resp2.Body.Close()

		// Set attendee-01 to Running with last result
		gw.SetActorState("attendee-01", types.StateRunning)
		gw.SetLastResult("attendee-01", &types.ExecutionResult{
			Stdout:     "simulation report",
			ExitCode:   0,
			DurationMs: 42,
		})

		// Set attendee-03 to Suspended (already suspended)
		gw.SetActorState("attendee-03", types.StateSuspended)

		// Subscribe to SSE for attendee-01 to verify StateSuspended broadcast
		sseCtx, sseCancel := context.WithCancel(context.Background())
		defer sseCancel()

		sseReq, err := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/api/events?attendee_id=attendee-01", nil)
		if err != nil {
			t.Fatalf("failed to create SSE request: %v", err)
		}
		sseResp, err := http.DefaultClient.Do(sseReq)
		if err != nil {
			t.Fatalf("failed to connect SSE stream: %v", err)
		}
		defer sseResp.Body.Close()

		sseReader := bufio.NewReader(sseResp.Body)
		sseEvents := make(chan types.StateChangeEvent, 10)
		go func() {
			for {
				line, err := sseReader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "data:") {
					dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					var ev types.StateChangeEvent
					if err := json.Unmarshal([]byte(dataStr), &ev); err == nil {
						sseEvents <- ev
					}
				}
			}
		}()

		// Drain initial SSE event
		select {
		case <-sseEvents:
		case <-time.After(1 * time.Second):
			t.Fatalf("timed out waiting for initial SSE event")
		}

		// Clear prior mock suspender calls from the setup chat call
		suspender.mu.Lock()
		suspender.invocations = nil
		suspender.mu.Unlock()

		// Call POST /api/reset
		resetResp, err := http.Post(ts.URL+"/api/reset", "application/json", nil)
		if err != nil {
			t.Fatalf("failed to call /api/reset: %v", err)
		}
		defer resetResp.Body.Close()

		if resetResp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resetResp.StatusCode)
		}

		var resetBody struct {
			Status      string `json:"status"`
			Message     string `json:"message"`
			ActorsReset int    `json:"actors_reset"`
		}
		if err := json.NewDecoder(resetResp.Body).Decode(&resetBody); err != nil {
			t.Fatalf("failed to decode reset response: %v", err)
		}

		if resetBody.Status != "ok" {
			t.Errorf("expected status 'ok', got %q", resetBody.Status)
		}
		if !strings.Contains(resetBody.Message, "Cluster reset") {
			t.Errorf("expected message to contain 'Cluster reset', got %q", resetBody.Message)
		}
		if resetBody.ActorsReset != 2 {
			t.Errorf("expected actors_reset 2, got %d", resetBody.ActorsReset)
		}

		// Verify all actor states reset to StateSuspended
		st1, _ := gw.GetActorState("attendee-01")
		st2, _ := gw.GetActorState("attendee-02")
		st3, _ := gw.GetActorState("attendee-03")
		if st1 != types.StateSuspended {
			t.Errorf("expected attendee-01 state %s, got %s", types.StateSuspended, st1)
		}
		if st2 != types.StateSuspended {
			t.Errorf("expected attendee-02 state %s, got %s", types.StateSuspended, st2)
		}
		if st3 != types.StateSuspended {
			t.Errorf("expected attendee-03 state %s, got %s", types.StateSuspended, st3)
		}

		// Verify suspender was called for previously active actors (attendee-01 and attendee-02), but not attendee-03
		calls := suspender.getCalls()
		suspendedActors := make(map[string]bool)
		for _, c := range calls {
			if c.Atespace == "ate-demo-conf" {
				suspendedActors[c.ActorID] = true
			}
		}
		if !suspendedActors["attendee-01"] {
			t.Errorf("expected suspender called for attendee-01, calls: %+v", calls)
		}
		if !suspendedActors["attendee-02"] {
			t.Errorf("expected suspender called for attendee-02, calls: %+v", calls)
		}
		if suspendedActors["attendee-03"] {
			t.Errorf("expected suspender NOT called for already suspended attendee-03, calls: %+v", calls)
		}

		// Verify last results cleared
		if res, ok := gw.GetLastResult("attendee-01"); ok && res != nil {
			t.Errorf("expected last result for attendee-01 to be cleared, got %+v", res)
		}

		// Verify pending approval and chat history cleared via actor inspector
		detResp, err := http.Get(ts.URL + "/api/actor/attendee-02")
		if err != nil {
			t.Fatalf("failed to get actor details: %v", err)
		}
		var details types.ActorDetails
		if err := json.NewDecoder(detResp.Body).Decode(&details); err != nil {
			t.Fatalf("failed to decode actor details: %v", err)
		}
		_ = detResp.Body.Close()

		if details.PendingApproval != nil {
			t.Errorf("expected pending approval to be cleared, got %+v", details.PendingApproval)
		}
		if len(details.ChatHistory) != 0 {
			t.Errorf("expected chat history to be cleared, got %d items", len(details.ChatHistory))
		}
		if details.LastResult != nil {
			t.Errorf("expected last result to be nil, got %+v", details.LastResult)
		}

		// Verify reset event logged in recentEvents
		telemResp, err := http.Get(ts.URL + "/api/stage/telemetry")
		if err != nil {
			t.Fatalf("failed to get telemetry: %v", err)
		}
		var telem types.StageTelemetry
		if err := json.NewDecoder(telemResp.Body).Decode(&telem); err != nil {
			t.Fatalf("failed to decode telemetry: %v", err)
		}
		_ = telemResp.Body.Close()

		foundResetEvent := false
		for _, ev := range telem.RecentEvents {
			if strings.Contains(ev, "Cluster reset: all actors suspended to GCS, worker pods freed") {
				foundResetEvent = true
				break
			}
		}
		if !foundResetEvent {
			t.Errorf("expected reset event in recentEvents, got %+v", telem.RecentEvents)
		}

		// Verify SSE broadcasted StateSuspended event
		select {
		case sseEv := <-sseEvents:
			if sseEv.AttendeeID != "attendee-01" {
				t.Errorf("expected SSE event attendee_id 'attendee-01', got %q", sseEv.AttendeeID)
			}
			if sseEv.State != types.StateSuspended {
				t.Errorf("expected SSE event state %s, got %s", types.StateSuspended, sseEv.State)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for StateSuspended SSE broadcast")
		}
	})

	t.Run("MethodValidation", func(t *testing.T) {
		methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}
		for _, m := range methods {
			req, err := http.NewRequest(m, ts.URL+"/api/reset", nil)
			if err != nil {
				t.Fatalf("failed to create %s request: %v", m, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed %s /api/reset: %v", m, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("expected 405 Method Not Allowed for %s /api/reset, got %d", m, resp.StatusCode)
			}
		}
	})

	t.Run("HaltsSimulationRunner", func(t *testing.T) {
		runner := &mockSimulationController{}
		gw.SetSimulationController(runner)

		resp, err := http.Post(ts.URL+"/api/reset", "application/json", nil)
		if err != nil {
			t.Fatalf("failed to call /api/reset: %v", err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		if !runner.stopped.Load() {
			t.Errorf("expected active simulation runner to be halted upon reset")
		}
	})

	t.Run("ConcurrencySafety", func(t *testing.T) {
		const numWorkers = 8
		const numIterations = 15

		var wg sync.WaitGroup
		wg.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go func(workerID int) {
				defer wg.Done()
				client := &http.Client{Timeout: 5 * time.Second}

				for j := 0; j < numIterations; j++ {
					switch (workerID + j) % 4 {
					case 0:
						resp, err := client.Post(ts.URL+"/api/reset", "application/json", nil)
						if err == nil {
							_ = resp.Body.Close()
						}
					case 1:
						resp, err := client.Get(ts.URL + "/api/stage/telemetry")
						if err == nil {
							var telem types.StageTelemetry
							_ = json.NewDecoder(resp.Body).Decode(&telem)
							_ = resp.Body.Close()
						}
					case 2:
						resp, err := client.Get(ts.URL + "/api/actor/attendee-01")
						if err == nil {
							_ = resp.Body.Close()
						}
					case 3:
						resp, err := client.Get(ts.URL + "/api/session")
						if err == nil {
							_ = resp.Body.Close()
						}
					}
				}
			}(i)
		}

		wg.Wait()
	})
}

func TestSimulationEndpoint(t *testing.T) {
	var chatCalls atomic.Int64
	var approveCalls atomic.Int64
	var seenActors sync.Map

	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		chatCalls.Add(1)
		actor := r.Header.Get("ate-target-actor")
		if actor != "" {
			seenActors.Store(actor, true)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        fmt.Sprintf("msg-sim-%d", chatCalls.Load()),
			Sender:    types.SenderAssistant,
			Content:   "WAITING_FOR_APPROVAL: run simulated workload",
			Timestamp: time.Now().Unix(),
			Approval: &types.ApprovalRequest{
				ApprovalID: fmt.Sprintf("app-sim-%d", chatCalls.Load()),
				Summary:    "Run simulation benchmark",
				Code:       "print('simulated benchmark')",
				Language:   "python",
				RiskLevel:  types.RiskMedium,
			},
		})
	})
	upstreamMux.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		approveCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        fmt.Sprintf("msg-approved-%d", approveCalls.Load()),
			Sender:    types.SenderAssistant,
			Content:   "Execution completed in 15ms (exit code 0):\nBenchmark OK",
			Timestamp: time.Now().Unix(),
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	suspender := &mockSuspender{}
	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithAtespace("ate-demo-conf"),
		gateway.WithSuspender(suspender),
	)
	defer gw.Close()

	simRunner := gateway.NewSimulationRunner(gw, gateway.WithSimulationDelays(20*time.Millisecond, 40*time.Millisecond))
	gw.SetSimulationController(simRunner)

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	t.Run("InitialStatus", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/api/simulation")
		if err != nil {
			t.Fatalf("failed to GET /api/simulation: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}

		var status struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("failed to decode simulation status: %v", err)
		}

		if status.Running {
			t.Errorf("expected running to be false initially, got %v", status.Running)
		}
		if status.Count != 0 {
			t.Errorf("expected count to be 0 initially, got %d", status.Count)
		}

		// Verify HEAD request
		headResp, err := http.Head(ts.URL + "/api/simulation")
		if err != nil {
			t.Fatalf("failed to HEAD /api/simulation: %v", err)
		}
		defer headResp.Body.Close()
		if headResp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200 for HEAD /api/simulation, got %d", headResp.StatusCode)
		}
		headBody, _ := io.ReadAll(headResp.Body)
		if len(headBody) != 0 {
			t.Errorf("expected empty body for HEAD, got %d bytes", len(headBody))
		}
	})

	t.Run("StartAndStop", func(t *testing.T) {
		// Reset call counts
		chatCalls.Store(0)
		approveCalls.Store(0)

		// 1. POST /api/simulation with start and count 5
		startReqBody := `{"action":"start","count":5}`
		respStart, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(startReqBody))
		if err != nil {
			t.Fatalf("failed to POST /api/simulation start: %v", err)
		}
		defer respStart.Body.Close()

		if respStart.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 for start, got %d", respStart.StatusCode)
		}

		var startStatus struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		if err := json.NewDecoder(respStart.Body).Decode(&startStatus); err != nil {
			t.Fatalf("failed to decode start response: %v", err)
		}
		if !startStatus.Running {
			t.Errorf("expected start response running=true, got %v", startStatus.Running)
		}
		if startStatus.Count != 5 {
			t.Errorf("expected start response count=5, got %d", startStatus.Count)
		}

		// 2. GET /api/simulation returns running=true, count=5
		respGet, err := http.Get(ts.URL + "/api/simulation")
		if err != nil {
			t.Fatalf("failed to GET /api/simulation: %v", err)
		}
		var getStatus struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		_ = json.NewDecoder(respGet.Body).Decode(&getStatus)
		_ = respGet.Body.Close()

		if !getStatus.Running || getStatus.Count != 5 {
			t.Errorf("expected GET /api/simulation running=true, count=5, got %+v", getStatus)
		}

		// 3. Wait for virtual attendees to submit prompts and approvals
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if chatCalls.Load() >= 5 && approveCalls.Load() >= 2 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}

		if chatCalls.Load() < 5 {
			t.Errorf("expected at least 5 chat calls submitted by virtual attendees, got %d", chatCalls.Load())
		}
		if approveCalls.Load() < 2 {
			t.Errorf("expected at least 2 approve calls submitted by virtual attendees, got %d", approveCalls.Load())
		}

		// Check multiple distinct actors were active
		actorCount := 0
		seenActors.Range(func(key, value any) bool {
			actorCount++
			return true
		})
		if actorCount < 2 {
			t.Errorf("expected multiple virtual actors to be active, got %d", actorCount)
		}

		// 4. POST /api/simulation with stop
		stopReqBody := `{"action":"stop"}`
		respStop, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(stopReqBody))
		if err != nil {
			t.Fatalf("failed to POST /api/simulation stop: %v", err)
		}
		defer respStop.Body.Close()

		if respStop.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 for stop, got %d", respStop.StatusCode)
		}

		var stopStatus struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		if err := json.NewDecoder(respStop.Body).Decode(&stopStatus); err != nil {
			t.Fatalf("failed to decode stop response: %v", err)
		}
		if stopStatus.Running {
			t.Errorf("expected stop response running=false, got %v", stopStatus.Running)
		}
		if stopStatus.Count != 0 {
			t.Errorf("expected stop response count=0, got %d", stopStatus.Count)
		}

		// 5. GET /api/simulation returns running=false, count=0
		respGetStop, err := http.Get(ts.URL + "/api/simulation")
		if err != nil {
			t.Fatalf("failed to GET /api/simulation after stop: %v", err)
		}
		var getStopStatus struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		_ = json.NewDecoder(respGetStop.Body).Decode(&getStopStatus)
		_ = respGetStop.Body.Close()

		if getStopStatus.Running || getStopStatus.Count != 0 {
			t.Errorf("expected GET after stop running=false, count=0, got %+v", getStopStatus)
		}
	})

	t.Run("ConsecutiveStarts", func(t *testing.T) {
		// 1. Start simulation with count 3
		resp1, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(`{"action":"start","count":3}`))
		if err != nil {
			t.Fatalf("failed to start simulation with count 3: %v", err)
		}
		defer resp1.Body.Close()
		if resp1.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp1.StatusCode)
		}
		var status1 struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		if err := json.NewDecoder(resp1.Body).Decode(&status1); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !status1.Running || status1.Count != 3 {
			t.Errorf("expected running=true, count=3, got %+v", status1)
		}

		// Verify via GET
		getResp1, err := http.Get(ts.URL + "/api/simulation")
		if err != nil {
			t.Fatalf("failed to GET simulation status: %v", err)
		}
		var getStatus1 struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		_ = json.NewDecoder(getResp1.Body).Decode(&getStatus1)
		_ = getResp1.Body.Close()
		if !getStatus1.Running || getStatus1.Count != 3 {
			t.Errorf("expected GET status running=true, count=3, got %+v", getStatus1)
		}

		// 2. Immediately start simulation with count 6 without explicit stop (consecutive start replaces previous cleanly)
		resp2, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(`{"action":"start","count":6}`))
		if err != nil {
			t.Fatalf("failed consecutive start simulation with count 6: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp2.StatusCode)
		}
		var status2 struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		if err := json.NewDecoder(resp2.Body).Decode(&status2); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !status2.Running || status2.Count != 6 {
			t.Errorf("expected running=true, count=6, got %+v", status2)
		}

		// Verify via GET that count updated to 6
		getResp2, err := http.Get(ts.URL + "/api/simulation")
		if err != nil {
			t.Fatalf("failed to GET simulation status after consecutive start: %v", err)
		}
		var getStatus2 struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		_ = json.NewDecoder(getResp2.Body).Decode(&getStatus2)
		_ = getResp2.Body.Close()
		if !getStatus2.Running || getStatus2.Count != 6 {
			t.Errorf("expected GET status running=true, count=6, got %+v", getStatus2)
		}

		// 3. Stop simulation cleanly
		respStop, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(`{"action":"stop"}`))
		if err != nil {
			t.Fatalf("failed to stop simulation: %v", err)
		}
		_ = respStop.Body.Close()
		if respStop.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200 for stop, got %d", respStop.StatusCode)
		}

		// Verify via GET that simulation is stopped
		getRespStop, err := http.Get(ts.URL + "/api/simulation")
		if err != nil {
			t.Fatalf("failed to GET simulation status after stop: %v", err)
		}
		var getStopStatus struct {
			Running bool `json:"running"`
			Count   int  `json:"count"`
		}
		_ = json.NewDecoder(getRespStop.Body).Decode(&getStopStatus)
		_ = getRespStop.Body.Close()
		if getStopStatus.Running || getStopStatus.Count != 0 {
			t.Errorf("expected running=false, count=0, got %+v", getStopStatus)
		}
	})

	t.Run("ClosedGatewayGuard", func(t *testing.T) {
		gwClosed := newTestGateway(t, "http://localhost:8000", "")
		runnerClosed := gateway.NewSimulationRunner(gwClosed)
		gwClosed.Close()

		err := runnerClosed.Start(5)
		if err == nil {
			t.Fatalf("expected error starting simulation on closed gateway, got nil")
		}
		if !strings.Contains(err.Error(), "gateway is closed") {
			t.Errorf("expected error message to contain 'gateway is closed', got: %v", err)
		}
	})

	t.Run("Validation", func(t *testing.T) {
		invalidCases := []struct {
			name string
			body string
		}{
			{"InvalidActionPause", `{"action":"pause","count":5}`},
			{"InvalidActionUnknown", `{"action":"unknown"}`},
			{"InvalidActionEmpty", `{"action":""}`},
			{"CountZero", `{"action":"start","count":0}`},
			{"CountNegative", `{"action":"start","count":-5}`},
			{"CountTooHigh", `{"action":"start","count":101}`},
			{"MalformedJSON", `{"action":`},
		}

		for _, tc := range invalidCases {
			t.Run(tc.name, func(t *testing.T) {
				resp, err := http.Post(ts.URL+"/api/simulation", "application/json", strings.NewReader(tc.body))
				if err != nil {
					t.Fatalf("failed request: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusBadRequest {
					t.Errorf("expected 400 Bad Request for %s, got %d", tc.name, resp.StatusCode)
				}
			})
		}

		// Non-GET/POST/HEAD methods should return 405 Method Not Allowed
		methods := []string{http.MethodPut, http.MethodDelete, http.MethodPatch}
		for _, m := range methods {
			req, err := http.NewRequest(m, ts.URL+"/api/simulation", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed %s request: %v", m, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("expected 405 Method Not Allowed for %s, got %d", m, resp.StatusCode)
			}
		}
	})

	t.Run("ConcurrencySafety", func(t *testing.T) {
		const numWorkers = 10
		const numIterations = 15

		token := gw.MintToken("attendee-conc-sim")
		var wg sync.WaitGroup
		wg.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go func(workerID int) {
				defer wg.Done()
				client := &http.Client{Timeout: 5 * time.Second}

				for j := 0; j < numIterations; j++ {
					switch (workerID + j) % 5 {
					case 0:
						// Alternate start and stop
						action := "start"
						count := (workerID % 5) + 1
						if j%2 == 1 {
							action = "stop"
							count = 0
						}
						reqBody, _ := json.Marshal(map[string]any{"action": action, "count": count})
						resp, err := client.Post(ts.URL+"/api/simulation", "application/json", bytes.NewReader(reqBody))
						if err == nil {
							_ = resp.Body.Close()
						}
					case 1:
						resp, err := client.Get(ts.URL + "/api/simulation")
						if err == nil {
							var st struct {
								Running bool `json:"running"`
								Count   int  `json:"count"`
							}
							_ = json.NewDecoder(resp.Body).Decode(&st)
							_ = resp.Body.Close()
						}
					case 2:
						req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"ping"}`))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Authorization", "Bearer "+token)
						resp, err := client.Do(req)
						if err == nil {
							_ = resp.Body.Close()
						}
					case 3:
						resp, err := client.Post(ts.URL+"/api/reset", "application/json", nil)
						if err == nil {
							_ = resp.Body.Close()
						}
					case 4:
						resp, err := client.Get(ts.URL + "/api/stage/telemetry")
						if err == nil {
							var telem types.StageTelemetry
							_ = json.NewDecoder(resp.Body).Decode(&telem)
							_ = resp.Body.Close()
						}
					}
				}
			}(i)
		}

		wg.Wait()
	})
}

func TestActionStreamEndpoints(t *testing.T) {
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:        "msg-stream-chat-1",
			Sender:    types.SenderAssistant,
			Content:   "WAITING_FOR_APPROVAL: generate report",
			Timestamp: time.Now().Unix(),
			Approval: &types.ApprovalRequest{
				ApprovalID: "appr-flow-1",
				Summary:    "Generate report",
				Code:       "print('report generated')",
				Language:   "python",
				RiskLevel:  types.RiskLow,
			},
		})
	})
	upstreamMux.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ExecutionResult{
			Stdout:     "report generated\n",
			ExitCode:   0,
			DurationMs: 55,
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	suspender := &mockSuspender{}
	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithAtespace("ate-demo-conf"),
		gateway.WithSuspender(suspender),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	t.Run("FeedHistoryEndpoints", func(t *testing.T) {
		// Non-GET/HEAD methods return 405 Method Not Allowed
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequest(method, ts.URL+"/api/feed/history", nil)
			if err != nil {
				t.Fatalf("failed to create %s request: %v", method, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed to execute %s request: %v", method, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("expected status 405 for %s /api/feed/history, got %d", method, resp.StatusCode)
			}
		}

		// HEAD /api/feed/history returns 200 OK with application/json
		headResp, err := http.Head(ts.URL + "/api/feed/history")
		if err != nil {
			t.Fatalf("failed HEAD /api/feed/history: %v", err)
		}
		_ = headResp.Body.Close()
		if headResp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for HEAD /api/feed/history, got %d", headResp.StatusCode)
		}
		if ct := headResp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("expected application/json for HEAD, got %q", ct)
		}

		// Initial GET /api/feed/history returns 200 OK and empty array []
		getResp, err := http.Get(ts.URL + "/api/feed/history")
		if err != nil {
			t.Fatalf("failed GET /api/feed/history: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for GET /api/feed/history, got %d", getResp.StatusCode)
		}
		if ct := getResp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("expected application/json for GET, got %q", ct)
		}

		bodyBytes, err := io.ReadAll(getResp.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		if strings.TrimSpace(string(bodyBytes)) != "[]" {
			t.Errorf("expected empty JSON array '[]', got %q", string(bodyBytes))
		}

		var initialEntries []types.ActionLogEntry
		if err := json.Unmarshal(bodyBytes, &initialEntries); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}
		if len(initialEntries) != 0 {
			t.Errorf("expected 0 entries initially, got %d", len(initialEntries))
		}

		// Record a single entry and check retrieval
		singleEntry := types.ActionLogEntry{
			ID:            "test-entry-1",
			AttendeeID:    "attendee-01",
			Timestamp:     time.Now().Unix(),
			Prompt:        "Analyze cluster health",
			ModelResponse: "Here is your analysis",
			Code:          "print('cluster healthy')",
			Language:      "python",
			RiskLevel:     types.RiskLow,
			ApprovalID:    "app-test-1",
			Decision:      types.DecisionApprove,
			State:         types.StateRunningGVisor,
		}
		gw.RecordActionEntry(singleEntry)

		respAfterOne, err := http.Get(ts.URL + "/api/feed/history")
		if err != nil {
			t.Fatalf("failed GET after recording entry: %v", err)
		}
		defer respAfterOne.Body.Close()

		var entriesAfterOne []types.ActionLogEntry
		if err := json.NewDecoder(respAfterOne.Body).Decode(&entriesAfterOne); err != nil {
			t.Fatalf("failed to decode entries: %v", err)
		}
		if len(entriesAfterOne) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entriesAfterOne))
		}
		if entriesAfterOne[0].ID != singleEntry.ID || entriesAfterOne[0].Prompt != singleEntry.Prompt {
			t.Errorf("entry mismatch: got %+v, want %+v", entriesAfterOne[0], singleEntry)
		}

		// Buffer cap test: append 105 entries and ensure exactly last 100 entries retained
		for i := 0; i < 105; i++ {
			gw.RecordActionEntry(types.ActionLogEntry{
				ID:         fmt.Sprintf("entry-buffer-%d", i),
				AttendeeID: fmt.Sprintf("attendee-%02d", i%10),
				Timestamp:  time.Now().Unix(),
				Prompt:     fmt.Sprintf("Prompt %d", i),
				State:      types.StateThinkingVertexAI,
			})
		}

		feed := gw.GetFeedHistory()
		if len(feed) != 100 {
			t.Fatalf("expected buffer cap of 100 entries, got %d", len(feed))
		}
		if feed[0].ID != "entry-buffer-5" {
			t.Errorf("expected oldest entry to be entry-buffer-5, got %s", feed[0].ID)
		}
		if feed[99].ID != "entry-buffer-104" {
			t.Errorf("expected newest entry to be entry-buffer-104, got %s", feed[99].ID)
		}
	})

	t.Run("ActionStreamSSE", func(t *testing.T) {
		// Non-GET methods return 405 Method Not Allowed
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequest(method, ts.URL+"/api/stream", nil)
			if err != nil {
				t.Fatalf("failed to create %s request: %v", method, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed to execute %s request: %v", method, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("expected status 405 for %s /api/stream, got %d", method, resp.StatusCode)
			}
		}

		// Connect client to GET /api/stream
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/stream", nil)
		if err != nil {
			t.Fatalf("failed to create SSE request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("failed to connect to /api/stream: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from /api/stream, got %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			t.Errorf("expected text/event-stream, got %q", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("expected Cache-Control no-cache, got %q", cc)
		}

		if count := gw.ActionSubscriberCount(); count != 1 {
			t.Errorf("expected 1 action subscriber, got %d", count)
		}

		reader := bufio.NewReader(resp.Body)
		actionChan := make(chan types.ActionLogEntry, 5)
		resetChan := make(chan struct{}, 5)
		errChan := make(chan error, 1)

		go func() {
			var currentEvent string
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					errChan <- err
					return
				}
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "event:") {
					currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				} else if strings.HasPrefix(line, "data:") {
					dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					if currentEvent == "action_entry" {
						var entry types.ActionLogEntry
						if err := json.Unmarshal([]byte(dataStr), &entry); err == nil {
							actionChan <- entry
						}
					} else if currentEvent == "reset" {
						resetChan <- struct{}{}
					}
					currentEvent = ""
				}
			}
		}()

		// Broadcast action entry
		broadcastEntry := types.ActionLogEntry{
			ID:         "entry-sse-test-1",
			AttendeeID: "attendee-77",
			Timestamp:  time.Now().Unix(),
			Prompt:     "Stream test prompt",
			State:      types.StateThinkingVertexAI,
		}
		gw.RecordActionEntry(broadcastEntry)

		select {
		case received := <-actionChan:
			if received.ID != broadcastEntry.ID {
				t.Errorf("expected ID %s, got %s", broadcastEntry.ID, received.ID)
			}
			if received.AttendeeID != broadcastEntry.AttendeeID {
				t.Errorf("expected AttendeeID %s, got %s", broadcastEntry.AttendeeID, received.AttendeeID)
			}
			if received.Prompt != broadcastEntry.Prompt {
				t.Errorf("expected Prompt %q, got %q", broadcastEntry.Prompt, received.Prompt)
			}
		case err := <-errChan:
			t.Fatalf("error reading SSE stream: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for action_entry SSE event")
		}

		// Test reset event broadcast
		resetResp, err := http.Post(ts.URL+"/api/reset", "application/json", nil)
		if err != nil {
			t.Fatalf("failed to call /api/reset: %v", err)
		}
		_ = resetResp.Body.Close()
		if resetResp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK from /api/reset, got %d", resetResp.StatusCode)
		}

		select {
		case <-resetChan:
			// Reset received as expected
		case err := <-errChan:
			t.Fatalf("error reading SSE stream during reset: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for reset SSE event")
		}

		// Verify feed history cleared after reset
		if history := gw.GetFeedHistory(); len(history) != 0 {
			t.Errorf("expected empty feed history after reset, got %d entries", len(history))
		}

		// Clean client disconnect test
		cancel()

		deadline := time.Now().Add(2 * time.Second)
		cleanedUp := false
		for time.Now().Before(deadline) {
			if gw.ActionSubscriberCount() == 0 {
				cleanedUp = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !cleanedUp {
			t.Errorf("expected action subscriber count 0 after disconnect, got %d", gw.ActionSubscriberCount())
		}
	})

	t.Run("ChatAndApprovalFlowActionStream", func(t *testing.T) {
		// Connect to stream to monitor full flow
		streamCtx, streamCancel := context.WithCancel(context.Background())
		defer streamCancel()

		streamReq, err := http.NewRequestWithContext(streamCtx, http.MethodGet, ts.URL+"/api/stream", nil)
		if err != nil {
			t.Fatalf("failed to create stream request: %v", err)
		}
		streamResp, err := http.DefaultClient.Do(streamReq)
		if err != nil {
			t.Fatalf("failed to connect to stream: %v", err)
		}
		defer streamResp.Body.Close()

		reader := bufio.NewReader(streamResp.Body)
		streamEvents := make(chan types.ActionLogEntry, 20)
		go func() {
			var currentEvent string
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "event:") {
					currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				} else if strings.HasPrefix(line, "data:") {
					dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					if currentEvent == "action_entry" {
						var entry types.ActionLogEntry
						if err := json.Unmarshal([]byte(dataStr), &entry); err == nil {
							streamEvents <- entry
						}
					}
					currentEvent = ""
				}
			}
		}()

		// 1. Chat Prompt
		token := gw.MintToken("attendee-99")
		chatReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"generate monthly report"}`))
		if err != nil {
			t.Fatalf("failed to build chat request: %v", err)
		}
		chatReq.Header.Set("Content-Type", "application/json")
		chatReq.Header.Set("Authorization", "Bearer "+token)

		chatResp, err := http.DefaultClient.Do(chatReq)
		if err != nil {
			t.Fatalf("failed chat request: %v", err)
		}
		_ = chatResp.Body.Close()
		if chatResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from chat, got %d", chatResp.StatusCode)
		}

		// 2. Approval & Execution
		apprReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/approve", strings.NewReader(`{"approval_id":"appr-flow-1","decision":"APPROVE"}`))
		if err != nil {
			t.Fatalf("failed to build approve request: %v", err)
		}
		apprReq.Header.Set("Content-Type", "application/json")
		apprReq.Header.Set("Authorization", "Bearer "+token)

		apprResp, err := http.DefaultClient.Do(apprReq)
		if err != nil {
			t.Fatalf("failed approve request: %v", err)
		}
		_ = apprResp.Body.Close()
		if apprResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from approve, got %d", apprResp.StatusCode)
		}

		// Drain and assert the specific sequence of states from action stream
		var events []types.ActionLogEntry
		deadline := time.Now().Add(3 * time.Second)
	drainLoop:
		for time.Now().Before(deadline) {
			select {
			case ev := <-streamEvents:
				if ev.AttendeeID == "attendee-99" {
					events = append(events, ev)
					if ev.Execution != nil && (ev.State == types.StateSuspended || ev.State == types.StateRunningGVisor) {
						break drainLoop
					}
				}
			case <-time.After(100 * time.Millisecond):
				if len(events) >= 4 {
					break drainLoop
				}
			}
		}

		var chatEvent *types.ActionLogEntry
		var approvalEvent *types.ActionLogEntry
		var executionEvent *types.ActionLogEntry
		var completedEntry *types.ActionLogEntry

		for i := range events {
			ev := &events[i]
			if chatEvent == nil && (ev.State == types.StateThinkingVertexAI || ev.State == types.StateRunning) {
				chatEvent = ev
			}
			if approvalEvent == nil && ev.State == types.StateAwaitingApproval {
				approvalEvent = ev
			}
			if executionEvent == nil && (ev.State == types.StateRunningGVisor || ev.State == types.StateSuspended) {
				executionEvent = ev
			}
			if completedEntry == nil && ev.Execution != nil {
				completedEntry = ev
			}
		}

		if chatEvent == nil {
			t.Fatalf("expected chat event with state StateThinkingVertexAI or StateRunning, none found in %d events", len(events))
		}
		if approvalEvent == nil {
			t.Fatalf("expected approval event with state StateAwaitingApproval, none found in %d events", len(events))
		}
		if executionEvent == nil {
			t.Fatalf("expected execution event with state StateRunningGVisor or StateSuspended, none found in %d events", len(events))
		}
		if completedEntry == nil {
			t.Fatalf("expected completed entry with Execution result, none found in %d events", len(events))
		}
		if completedEntry.Execution == nil || completedEntry.Execution.Stdout == "" {
			t.Errorf("expected non-empty Execution result on completed action entry, got %+v", completedEntry.Execution)
		}

		// Assert action ID correlation across lifecycle phases
		if chatEvent.ID != approvalEvent.ID || chatEvent.ID != completedEntry.ID {
			t.Errorf("expected action ID correlation across lifecycle: chat=%s, approval=%s, completed=%s",
				chatEvent.ID, approvalEvent.ID, completedEntry.ID)
		}

		// Verify entries recorded in feed history and merged properly
		history := gw.GetFeedHistory()
		if len(history) == 0 {
			t.Fatalf("expected non-empty feed history, got %d entries", len(history))
		}
		var historyEntry *types.ActionLogEntry
		for i := range history {
			if history[i].AttendeeID == "attendee-99" {
				historyEntry = &history[i]
				break
			}
		}
		if historyEntry == nil {
			t.Fatalf("expected feed history entry for attendee-99")
		}
		if historyEntry.Execution == nil {
			t.Errorf("expected Execution result present on feed history entry for attendee-99")
		}
		if historyEntry.Prompt != "generate monthly report" {
			t.Errorf("expected preserved Prompt %q, got %q", "generate monthly report", historyEntry.Prompt)
		}
		if historyEntry.Code != "print('report generated')" {
			t.Errorf("expected preserved Code %q, got %q", "print('report generated')", historyEntry.Code)
		}
	})

	t.Run("ConcurrencySafety", func(t *testing.T) {
		const goroutines = 8
		const iterations = 25

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var subscriberWg sync.WaitGroup
		// Stream subscriber goroutines
		for i := 0; i < 3; i++ {
			subscriberWg.Add(1)
			go func() {
				defer subscriberWg.Done()
				subReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/stream", nil)
				if err != nil {
					return
				}
				subResp, err := http.DefaultClient.Do(subReq)
				if err != nil {
					return
				}
				defer subResp.Body.Close()

				reader := bufio.NewReader(subResp.Body)
				for {
					_, err := reader.ReadString('\n')
					if err != nil {
						return
					}
				}
			}()
		}

		var producerWg sync.WaitGroup
		// Producer / reader / reset goroutines
		for g := 0; g < goroutines; g++ {
			producerWg.Add(1)
			go func(gid int) {
				defer producerWg.Done()
				for i := 0; i < iterations; i++ {
					switch i % 4 {
					case 0:
						gw.RecordActionEntry(types.ActionLogEntry{
							ID:         fmt.Sprintf("entry-conc-%d-%d", gid, i),
							AttendeeID: fmt.Sprintf("attendee-%d", gid),
							Timestamp:  time.Now().Unix(),
							Prompt:     "Concurrent test prompt",
							State:      types.StateThinkingVertexAI,
						})
					case 1:
						_ = gw.GetFeedHistory()
					case 2:
						resp, err := http.Get(ts.URL + "/api/feed/history")
						if err == nil {
							var entries []types.ActionLogEntry
							_ = json.NewDecoder(resp.Body).Decode(&entries)
							_ = resp.Body.Close()
						}
					case 3:
						if i == iterations-1 {
							resp, err := http.Post(ts.URL+"/api/reset", "application/json", nil)
							if err == nil {
								_ = resp.Body.Close()
							}
						}
					}
				}
			}(g)
		}

		producerWg.Wait()
		cancel()
		subscriberWg.Wait()
	})
}

func TestChatAndApprovalFlowActionStream_WithAIClient(t *testing.T) {
	upstreamMux := http.NewServeMux()
	upstreamMux.HandleFunc("/api/approve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ExecutionResult{
			Stdout:     "anomaly scan complete: no anomalies found\n",
			ExitCode:   0,
			DurationMs: 42,
		})
	})
	upstreamSrv := httptest.NewServer(upstreamMux)
	defer upstreamSrv.Close()

	suspender := &mockSuspender{}
	aiClient := ai.NewGeminiClient(ai.WithProject(""), ai.WithGeminiMockEngine(engine.NewMockEngine()))
	gw := newTestGateway(t, upstreamSrv.URL, "",
		gateway.WithAtespace("ate-demo-conf"),
		gateway.WithSuspender(suspender),
		gateway.WithAIClient(aiClient),
	)
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// Connect SSE stream
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	streamReq, err := http.NewRequestWithContext(streamCtx, http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("failed to create stream request: %v", err)
	}
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("failed to connect to stream: %v", err)
	}
	defer streamResp.Body.Close()

	reader := bufio.NewReader(streamResp.Body)
	streamEvents := make(chan types.ActionLogEntry, 20)
	go func() {
		var currentEvent string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event:") {
				currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if currentEvent == "action_entry" {
					var entry types.ActionLogEntry
					if err := json.Unmarshal([]byte(dataStr), &entry); err == nil {
						streamEvents <- entry
					}
				}
				currentEvent = ""
			}
		}
	}()

	token := gw.MintToken("attendee-42")

	// 1. Chat Prompt
	chatReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"check telemetry anomalies"}`))
	if err != nil {
		t.Fatalf("failed to build chat request: %v", err)
	}
	chatReq.Header.Set("Content-Type", "application/json")
	chatReq.Header.Set("Authorization", "Bearer "+token)

	chatResp, err := http.DefaultClient.Do(chatReq)
	if err != nil {
		t.Fatalf("failed chat request: %v", err)
	}
	defer chatResp.Body.Close()
	if chatResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from chat, got %d", chatResp.StatusCode)
	}

	var chatMsg types.ChatMessage
	if err := json.NewDecoder(chatResp.Body).Decode(&chatMsg); err != nil {
		t.Fatalf("failed to decode chat message: %v", err)
	}
	if chatMsg.Approval == nil {
		t.Fatalf("expected approval request in chat message, got nil")
	}

	// 2. Approval & Execution
	apprBody := fmt.Sprintf(`{"approval_id":%q,"decision":"APPROVE"}`, chatMsg.Approval.ApprovalID)
	apprReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/approve", strings.NewReader(apprBody))
	if err != nil {
		t.Fatalf("failed to build approve request: %v", err)
	}
	apprReq.Header.Set("Content-Type", "application/json")
	apprReq.Header.Set("Authorization", "Bearer "+token)

	apprResp, err := http.DefaultClient.Do(apprReq)
	if err != nil {
		t.Fatalf("failed approve request: %v", err)
	}
	defer apprResp.Body.Close()
	if apprResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from approve, got %d", apprResp.StatusCode)
	}

	// Drain and assert stream events
	var events []types.ActionLogEntry
	deadline := time.Now().Add(3 * time.Second)
drainLoop:
	for time.Now().Before(deadline) {
		select {
		case ev := <-streamEvents:
			if ev.AttendeeID == "attendee-42" {
				events = append(events, ev)
				if ev.Execution != nil && (ev.State == types.StateSuspended || ev.State == types.StateRunningGVisor) {
					break drainLoop
				}
			}
		case <-time.After(100 * time.Millisecond):
			if len(events) >= 4 {
				break drainLoop
			}
		}
	}

	var chatEvent *types.ActionLogEntry
	var approvalEvent *types.ActionLogEntry
	var executionEvent *types.ActionLogEntry
	var completedEntry *types.ActionLogEntry

	for i := range events {
		ev := &events[i]
		if chatEvent == nil && ev.State == types.StateThinkingVertexAI {
			chatEvent = ev
		}
		if approvalEvent == nil && ev.State == types.StateAwaitingApproval {
			approvalEvent = ev
		}
		if executionEvent == nil && (ev.State == types.StateRunningGVisor || ev.State == types.StateSuspended) {
			executionEvent = ev
		}
		if completedEntry == nil && ev.Execution != nil {
			completedEntry = ev
		}
	}

	if chatEvent == nil {
		t.Fatalf("expected StateThinkingVertexAI chat event, none found in %d events", len(events))
	}
	if approvalEvent == nil {
		t.Fatalf("expected StateAwaitingApproval event, none found in %d events", len(events))
	}
	if executionEvent == nil {
		t.Fatalf("expected StateRunningGVisor or StateSuspended event, none found in %d events", len(events))
	}
	if completedEntry == nil {
		t.Fatalf("expected completed entry with Execution result, none found in %d events", len(events))
	}

	// Assert action ID correlation across all phases
	if chatEvent.ID != approvalEvent.ID || chatEvent.ID != completedEntry.ID {
		t.Errorf("expected correlated action ID across lifecycle: chat=%s, approval=%s, completed=%s",
			chatEvent.ID, approvalEvent.ID, completedEntry.ID)
	}

	// Verify merged feed history
	history := gw.GetFeedHistory()
	if len(history) == 0 {
		t.Fatalf("expected non-empty feed history")
	}
	var historyEntry *types.ActionLogEntry
	for i := range history {
		if history[i].AttendeeID == "attendee-42" {
			historyEntry = &history[i]
			break
		}
	}
	if historyEntry == nil {
		t.Fatalf("expected feed history entry for attendee-42")
	}
	if historyEntry.Execution == nil {
		t.Errorf("expected Execution result present in history entry")
	}
	if historyEntry.Prompt != "check telemetry anomalies" {
		t.Errorf("expected preserved Prompt %q, got %q", "check telemetry anomalies", historyEntry.Prompt)
	}
	if historyEntry.Code == "" {
		t.Errorf("expected preserved Code in history entry")
	}
}

func TestFeedRoute(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "")
	defer gw.Close()

	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	requiredIDs := []string{
		`id="action-stream"`,
		`id="feed-search"`,
		`id="feed-autoscroll"`,
		`id="btn-reset-all"`,
	}

	// 1. GET /feed returns 200 OK with Content-Type: text/html and required DOM IDs
	respFeed, err := http.Get(ts.URL + "/feed")
	if err != nil {
		t.Fatalf("failed to GET /feed: %v", err)
	}
	defer respFeed.Body.Close()

	if respFeed.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /feed, got %d", respFeed.StatusCode)
	}
	if ct := respFeed.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected Content-Type text/html for GET /feed, got %q", ct)
	}

	bodyBytesFeed, err := io.ReadAll(respFeed.Body)
	if err != nil {
		t.Fatalf("failed to read /feed body: %v", err)
	}
	bodyFeed := string(bodyBytesFeed)
	for _, id := range requiredIDs {
		if !strings.Contains(bodyFeed, id) {
			t.Errorf("expected GET /feed to contain %q, but was missing", id)
		}
	}

	// GET /feed/ returns 200 OK with Content-Type: text/html and required DOM IDs
	respFeedSlash, err := http.Get(ts.URL + "/feed/")
	if err != nil {
		t.Fatalf("failed to GET /feed/: %v", err)
	}
	defer respFeedSlash.Body.Close()

	if respFeedSlash.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for GET /feed/, got %d", respFeedSlash.StatusCode)
	}
	if ct := respFeedSlash.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected Content-Type text/html for GET /feed/, got %q", ct)
	}

	bodyBytesFeedSlash, err := io.ReadAll(respFeedSlash.Body)
	if err != nil {
		t.Fatalf("failed to read /feed/ body: %v", err)
	}
	bodyFeedSlash := string(bodyBytesFeedSlash)
	for _, id := range requiredIDs {
		if !strings.Contains(bodyFeedSlash, id) {
			t.Errorf("expected GET /feed/ to contain %q, but was missing", id)
		}
	}

	// 2. HEAD /feed returns 200 OK with empty body
	headResp, err := http.Head(ts.URL + "/feed")
	if err != nil {
		t.Fatalf("failed to HEAD /feed: %v", err)
	}
	defer headResp.Body.Close()

	if headResp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for HEAD /feed, got %d", headResp.StatusCode)
	}
	if ct := headResp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected Content-Type text/html for HEAD /feed, got %q", ct)
	}
	headBody, _ := io.ReadAll(headResp.Body)
	if len(headBody) != 0 {
		t.Errorf("expected empty body for HEAD /feed, got %d bytes", len(headBody))
	}

	// 3. Subtree path isolation (/feed/subpath returns 404)
	respSubtree, err := http.Get(ts.URL + "/feed/subpath")
	if err != nil {
		t.Fatalf("failed to GET /feed/subpath: %v", err)
	}
	defer respSubtree.Body.Close()

	if respSubtree.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404 for /feed/subpath, got %d", respSubtree.StatusCode)
	}

	// 4. Non-GET/HEAD methods return 405 Method Not Allowed
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, err := http.NewRequest(method, ts.URL+"/feed", nil)
		if err != nil {
			t.Fatalf("failed to create %s request: %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("failed to execute %s request: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected status 405 for %s /feed, got %d", method, resp.StatusCode)
		}

		// Also check /feed/
		reqSlash, err := http.NewRequest(method, ts.URL+"/feed/", nil)
		if err != nil {
			t.Fatalf("failed to create %s request for /feed/: %v", method, err)
		}
		respSlash, err := http.DefaultClient.Do(reqSlash)
		if err != nil {
			t.Fatalf("failed to execute %s request for /feed/: %v", method, err)
		}
		_ = respSlash.Body.Close()
		if respSlash.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected status 405 for %s /feed/, got %d", method, respSlash.StatusCode)
		}
	}

	// 5. Static asset delivery: /static/feed.js and /static/feed.css return 200 OK
	respJS, err := http.Get(ts.URL + "/static/feed.js")
	if err != nil {
		t.Fatalf("failed to GET /static/feed.js: %v", err)
	}
	defer respJS.Body.Close()
	if respJS.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for GET /static/feed.js, got %d", respJS.StatusCode)
	}

	respCSS, err := http.Get(ts.URL + "/static/feed.css")
	if err != nil {
		t.Fatalf("failed to GET /static/feed.css: %v", err)
	}
	defer respCSS.Body.Close()
	if respCSS.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 for GET /static/feed.css, got %d", respCSS.StatusCode)
	}
}

func TestSegmentedSessionAllocation(t *testing.T) {
	requestAttendee := func(t *testing.T, client *http.Client, endpoint string) string {
		t.Helper()
		resp, err := client.Get(endpoint)
		if err != nil {
			t.Fatalf("failed to GET %s: %v", endpoint, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		var payload struct {
			AttendeeID string `json:"attendee_id"`
			Token      string `json:"token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode json response: %v", err)
		}
		return payload.AttendeeID
	}

	t.Run("NoSimulationRunning", func(t *testing.T) {
		gw := newTestGateway(t, "http://localhost:8000", "",
			gateway.WithJWTSecret("secret"),
		)
		defer gw.Close()

		sim := &mockSimulationController{}
		gw.SetSimulationController(sim)

		ts := httptest.NewServer(gw.Handler())
		defer ts.Close()

		for i := 1; i <= 5; i++ {
			expected := fmt.Sprintf("attendee-%02d", i)
			got := requestAttendee(t, ts.Client(), ts.URL+"/api/session")
			if got != expected {
				t.Fatalf("call %d: expected %s, got %s", i, expected, got)
			}
		}
	})

	t.Run("SimulationRunningCount25", func(t *testing.T) {
		gw := newTestGateway(t, "http://localhost:8000", "",
			gateway.WithJWTSecret("secret"),
		)
		defer gw.Close()

		sim := &mockSimulationController{}
		sim.SetStatus(true, 25)
		gw.SetSimulationController(sim)

		ts := httptest.NewServer(gw.Handler())
		defer ts.Close()

		// Successive calls allocate attendee-26 up to attendee-50
		for i := 26; i <= 50; i++ {
			expected := fmt.Sprintf("attendee-%02d", i)
			got := requestAttendee(t, ts.Client(), ts.URL+"/api/session")
			if got != expected {
				t.Fatalf("expected %s, got %s", expected, got)
			}
		}

		// Then wraps back to attendee-26
		gotWrap := requestAttendee(t, ts.Client(), ts.URL+"/api/session")
		if gotWrap != "attendee-26" {
			t.Fatalf("expected wrap-around to attendee-26, got %s", gotWrap)
		}
	})

	t.Run("SimulationRunningCount50MaxActors100", func(t *testing.T) {
		gw := newTestGateway(t, "http://localhost:8000", "",
			gateway.WithJWTSecret("secret"),
			gateway.WithMaxActors(100),
		)
		defer gw.Close()

		sim := &mockSimulationController{}
		sim.SetStatus(true, 50)
		gw.SetSimulationController(sim)

		ts := httptest.NewServer(gw.Handler())
		defer ts.Close()

		// Successive calls allocate attendee-51 up to attendee-100
		for i := 51; i <= 100; i++ {
			expected := fmt.Sprintf("attendee-%02d", i)
			got := requestAttendee(t, ts.Client(), ts.URL+"/api/session")
			if got != expected {
				t.Fatalf("expected %s, got %s", expected, got)
			}
		}

		// Then wraps back to attendee-51
		gotWrap := requestAttendee(t, ts.Client(), ts.URL+"/api/session")
		if gotWrap != "attendee-51" {
			t.Fatalf("expected wrap-around to attendee-51, got %s", gotWrap)
		}
	})

	t.Run("ExplicitAttendeeID", func(t *testing.T) {
		gw := newTestGateway(t, "http://localhost:8000", "",
			gateway.WithJWTSecret("secret"),
		)
		defer gw.Close()

		sim := &mockSimulationController{}
		sim.SetStatus(true, 25)
		gw.SetSimulationController(sim)

		ts := httptest.NewServer(gw.Handler())
		defer ts.Close()

		// When simulation is running
		got := requestAttendee(t, ts.Client(), ts.URL+"/api/session?attendee_id=attendee-07")
		if got != "attendee-07" {
			t.Fatalf("with simulation running: expected attendee-07, got %s", got)
		}

		// When simulation is stopped
		sim.SetStatus(false, 0)
		gotStopped := requestAttendee(t, ts.Client(), ts.URL+"/api/session?attendee_id=attendee-07")
		if gotStopped != "attendee-07" {
			t.Fatalf("with simulation stopped: expected attendee-07, got %s", gotStopped)
		}
	})

	t.Run("ConcurrencySafety", func(t *testing.T) {
		gw := newTestGateway(t, "http://localhost:8000", "",
			gateway.WithJWTSecret("secret"),
		)
		defer gw.Close()

		sim := &mockSimulationController{}
		gw.SetSimulationController(sim)

		ts := httptest.NewServer(gw.Handler())
		defer ts.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		// Background goroutine toggling simulation state
		wg.Add(1)
		go func() {
			defer wg.Done()
			counts := []int{0, 10, 25, 40, 0}
			idx := 0
			for {
				select {
				case <-ctx.Done():
					return
				default:
					c := counts[idx%len(counts)]
					if c == 0 {
						sim.SetStatus(false, 0)
					} else {
						sim.SetStatus(true, c)
					}
					idx++
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()

		// Multiple worker goroutines requesting sessions
		const workers = 8
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				client := ts.Client()
				for i := 0; i < 25; i++ {
					select {
					case <-ctx.Done():
						return
					default:
					}
					var reqURL string
					if workerID%2 == 0 {
						reqURL = ts.URL + "/api/session"
					} else {
						reqURL = ts.URL + "/api/session?attendee_id=attendee-07"
					}
					resp, err := client.Get(reqURL)
					if err != nil {
						t.Errorf("worker %d request failed: %v", workerID, err)
						return
					}
					if resp.StatusCode != http.StatusOK {
						t.Errorf("worker %d got status %d", workerID, resp.StatusCode)
						resp.Body.Close()
						return
					}
					var payload struct {
						AttendeeID string `json:"attendee_id"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
						t.Errorf("worker %d json decode error: %v", workerID, err)
						resp.Body.Close()
						return
					}
					resp.Body.Close()

					if !strings.HasPrefix(payload.AttendeeID, "attendee-") {
						t.Errorf("unexpected attendee ID: %s", payload.AttendeeID)
					}
				}
			}(w)
		}

		wg.Wait()
	})
}

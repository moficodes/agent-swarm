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

func TestSessionMinting(t *testing.T) {
	secret := "test-secret-12345"
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040",
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
	gwOther := gateway.NewGateway("http://localhost:8000", "http://localhost:4040",
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

	gw := gateway.NewGateway("http://localhost:8000", statusSrv.URL,
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
	for time.Now().Before(time.Now().Add(2 * time.Second)) {
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040",
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
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040",
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
	gw := gateway.NewGateway(upstreamSrv.URL, statusSrv.URL,
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
				reqApp, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/approve", strings.NewReader(`{"approval_id":"app-1","decision":"APPROVE"}`))
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
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040",
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
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
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
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040",
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040",
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040",
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

	gw := gateway.NewGateway(deadURL, "http://localhost:4040")
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040")
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040",
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040")
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

	gw := gateway.NewGateway(upstreamSrv.URL, "http://localhost:4040",
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
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
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
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
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
	gw := gateway.NewGateway("http://localhost:8000", "http://localhost:4040")
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

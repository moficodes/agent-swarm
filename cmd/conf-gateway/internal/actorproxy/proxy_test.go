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

package actorproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

func TestProxy_RewritesHeadersAndReportsReply(t *testing.T) {
	var got http.Header
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set(types.ModelKeyHeader, "leaked")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{ID: "m1", Content: "hi"})
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)

	var reply *types.ChatMessage
	var replyStatus int
	p := New(u, "space", []byte("secret"), nil, nil, Hooks{
		OnReply: func(attendee string, status int, msg *types.ChatMessage) {
			if attendee == "attendee-01" {
				reply, replyStatus = msg, status
			}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Cookie", "c=1")
	req.Header.Set(auth.PresenterHeaderName, "pk")
	req.Header.Set(types.ModelKeyHeader, "forged")
	req.Header.Set(auth.AttendeeTokenHeader, "jwt")
	req.Header.Set(auth.AttendeeIDHeader, "attendee-01")
	req.Header.Set("ate-target-actor", "space/attendee-99")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, WithAttendee(req, "attendee-01"))

	for _, h := range []string{"Authorization", "Cookie", auth.PresenterHeaderName, auth.AttendeeTokenHeader, auth.AttendeeIDHeader} {
		if got.Get(h) != "" {
			t.Errorf("router received %s", h)
		}
	}
	if v := got.Values("ate-target-actor"); len(v) != 1 || v[0] != "space/attendee-01" {
		t.Errorf("ate-target-actor = %q, want only space/attendee-01", v)
	}
	if want := ModelKey([]byte("secret"), "space", "attendee-01"); got.Get(types.ModelKeyHeader) != want {
		t.Errorf("model key = %q, want %q", got.Get(types.ModelKeyHeader), want)
	}
	if rec.Header().Get(types.ModelKeyHeader) != "" {
		t.Error("model key header reached the client")
	}
	if reply == nil || reply.ID != "m1" || replyStatus != http.StatusOK {
		t.Errorf("OnReply got %+v, %d", reply, replyStatus)
	}
	if !strings.Contains(rec.Body.String(), `"m1"`) {
		t.Errorf("client body = %q, want the actor's reply", rec.Body.String())
	}
}

func TestProxy_ReportsActorAndRouterErrors(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	u, _ := url.Parse(router.URL)
	actorErr := make(chan string, 1)
	routerErr := make(chan error, 1)
	p := New(u, "space", []byte("s"), nil, nil, Hooks{
		OnActorError: func(a string) { actorErr <- a },
		OnError:      func(_ string, err error) { routerErr <- err },
	})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, WithAttendee(httptest.NewRequest(http.MethodPost, "/api/chat", nil), "attendee-02"))
	if a := <-actorErr; a != "attendee-02" || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("OnActorError(%q), status %d", a, rec.Code)
	}

	router.Close()
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, WithAttendee(httptest.NewRequest(http.MethodPost, "/api/chat", nil), "attendee-02"))
	if err := <-routerErr; err == nil || rec.Code != http.StatusBadGateway {
		t.Fatalf("OnError(%v), status %d", err, rec.Code)
	}
}

func claim(p *Proxy, attendee, body string) (*httptest.ResponseRecorder, *http.Request, types.ApprovalResponse, func(), bool) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/approve", strings.NewReader(body))
	got, release, ok := p.ClaimApproval(rec, req, attendee)
	return rec, req, got, release, ok
}

func TestClaimApproval_BindsToPendingApproval(t *testing.T) {
	p := New(nil, "space", []byte("s"), nil, nil, Hooks{})
	p.SetPending("attendee-01", &types.ApprovalRequest{ApprovalID: "a1", Code: "print(1)", Language: "python"})

	if rec, _, _, _, ok := claim(p, "attendee-01", `{"approval_id":"other","decision":"APPROVE"}`); ok || rec.Code != http.StatusConflict {
		t.Fatalf("mismatched approval ID: ok=%v status=%d", ok, rec.Code)
	}
	if rec, _, _, _, ok := claim(p, "attendee-02", `{"approval_id":"a1","decision":"APPROVE"}`); ok || rec.Code != http.StatusConflict {
		t.Fatalf("another attendee's approval: ok=%v status=%d", ok, rec.Code)
	}

	_, req, got, release, ok := claim(p, "attendee-01", `{"approval_id":"a1","decision":"APPROVE","code":"rm -rf /"}`)
	if !ok {
		t.Fatal("matching claim rejected")
	}
	if got.Code != "print(1)" || got.Language != "python" {
		t.Errorf("claimed code = %q/%q, want the pending approval's", got.Code, got.Language)
	}
	var fwd types.ApprovalResponse
	if err := json.NewDecoder(req.Body).Decode(&fwd); err != nil || fwd.Code != "print(1)" {
		t.Errorf("forwarded body code = %q (%v), want print(1)", fwd.Code, err)
	}

	if rec, _, _, _, ok := claim(p, "attendee-01", `{"approval_id":"a1","decision":"APPROVE"}`); ok || rec.Code != http.StatusConflict {
		t.Fatalf("duplicate in-flight claim: ok=%v status=%d", ok, rec.Code)
	}
	release()
	if _, _, _, r, ok := claim(p, "attendee-01", `{"approval_id":"a1","decision":"APPROVE"}`); !ok {
		t.Fatal("claim after release rejected")
	} else {
		r()
	}

	if pa := p.PendingApproval("attendee-01"); pa == nil || pa.ApprovalID != "a1" {
		t.Fatalf("PendingApproval = %+v", pa)
	}
	p.ClearPending("attendee-01")
	if pa := p.PendingApproval("attendee-01"); pa != nil {
		t.Fatalf("PendingApproval after ClearPending = %+v", pa)
	}
	p.SetPending("attendee-03", &types.ApprovalRequest{ApprovalID: "a3"})
	p.ResetPending()
	if pa := p.PendingApproval("attendee-03"); pa != nil {
		t.Fatalf("PendingApproval after ResetPending = %+v", pa)
	}
}

func TestClaimApproval_RejectsBadPayload(t *testing.T) {
	p := New(nil, "space", []byte("s"), nil, nil, Hooks{})
	if rec, _, _, _, ok := claim(p, "attendee-01", `not json`); ok || rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: ok=%v status=%d", ok, rec.Code)
	}
}

func TestProxy_StripsModelKeyFromErrorResponse(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(types.ModelKeyHeader, "leaked")
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)
	p := New(u, "space", []byte("s"), nil, nil, Hooks{})

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, WithAttendee(httptest.NewRequest(http.MethodPost, "/api/chat", nil), "attendee-01"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get(types.ModelKeyHeader); got != "" {
		t.Errorf("model key header reached the client on an error response: %q", got)
	}
}

func TestClearHistory_PostsToTheAttendeesActor(t *testing.T) {
	var got *http.Request
	status := http.StatusNoContent
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.WriteHeader(status)
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)
	p := New(u, "space", []byte("secret"), nil, nil, Hooks{})

	if err := p.ClearHistory(context.Background(), "attendee-04"); err != nil {
		t.Fatalf("ClearHistory = %v", err)
	}
	if got.Method != http.MethodPost || got.URL.Path != types.ClearHistoryPath {
		t.Errorf("request = %s %s, want POST %s", got.Method, got.URL.Path, types.ClearHistoryPath)
	}
	if got.Header.Get("ate-target-actor") != "space/attendee-04" {
		t.Errorf("ate-target-actor = %q", got.Header.Get("ate-target-actor"))
	}
	if want := ModelKey([]byte("secret"), "space", "attendee-04"); got.Header.Get(types.ModelKeyHeader) != want {
		t.Errorf("model key = %q, want %q", got.Header.Get(types.ModelKeyHeader), want)
	}

	status = http.StatusForbidden
	if err := p.ClearHistory(context.Background(), "attendee-04"); err == nil {
		t.Error("ClearHistory = nil for a 403, want an error")
	}
}

type funcModelCaller func(ctx context.Context, actor string, call types.ModelCall) types.ModelResult

func (f funcModelCaller) Call(ctx context.Context, actor string, call types.ModelCall) types.ModelResult {
	return f(ctx, actor, call)
}

func TestProxy_RouterInFlight(t *testing.T) {
	chatEntered := make(chan struct{})
	releaseChat := make(chan struct{})
	resultEntered := make(chan struct{})
	releaseResult := make(chan struct{})
	clearEntered := make(chan struct{})
	releaseClear := make(chan struct{})

	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			close(chatEntered)
			<-releaseChat
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ModelCallStep{
				ModelCall: &types.ModelCall{CallID: "c1"},
			})
		case types.ModelResultPath:
			close(resultEntered)
			<-releaseResult
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.ChatMessage{ID: "m1", Content: "done"})
		case types.ClearHistoryPath:
			close(clearEntered)
			<-releaseClear
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)

	modelEntered := make(chan struct{})
	releaseModel := make(chan struct{})
	model := funcModelCaller(func(ctx context.Context, actor string, call types.ModelCall) types.ModelResult {
		close(modelEntered)
		<-releaseModel
		return types.ModelResult{CallID: call.CallID, Status: http.StatusOK}
	})

	p := New(u, "space", []byte("secret"), nil, model, Hooks{})
	if got := p.RouterInFlight(); got != 0 {
		t.Fatalf("initial RouterInFlight() = %d, want 0", got)
	}

	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`))
		p.ServeHTTP(rec, WithAttendee(req, "attendee-01"))
	}()

	<-chatEntered
	if got := p.RouterInFlight(); got != 1 {
		t.Fatalf("RouterInFlight() while initial /api/chat waits in router = %d, want 1", got)
	}
	close(releaseChat)

	<-modelEntered
	if got := p.RouterInFlight(); got != 0 {
		t.Fatalf("RouterInFlight() during gateway ModelCall = %d, want 0", got)
	}
	close(releaseModel)

	<-resultEntered
	if got := p.RouterInFlight(); got != 0 {
		t.Fatalf("RouterInFlight() during postResult to awake actor = %d, want 0", got)
	}
	close(releaseResult)
	<-turnDone

	clearDone := make(chan error, 1)
	go func() {
		clearDone <- p.ClearHistory(context.Background(), "attendee-01")
	}()
	<-clearEntered
	if got := p.RouterInFlight(); got != 1 {
		t.Fatalf("RouterInFlight() while ClearHistory waits in router = %d, want 1", got)
	}
	close(releaseClear)
	if err := <-clearDone; err != nil {
		t.Fatalf("ClearHistory = %v", err)
	}
	if got := p.RouterInFlight(); got != 0 {
		t.Fatalf("final RouterInFlight() = %d, want 0", got)
	}
}

func TestProxy_IsolatesHumanTransportFromMockSaturation(t *testing.T) {
	mockEntered := make(chan struct{})
	releaseMock := make(chan struct{})

	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ate-target-actor") == "space/attendee-01" {
			close(mockEntered)
			<-releaseMock
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{ID: "m1", Content: "ok"})
	}))
	defer router.Close()
	u, _ := url.Parse(router.URL)

	// Constrain the base transport to MaxConnsPerHost: 1 so a single simulated
	// mock turn saturates the mock pool. A real human turn must still succeed
	// immediately over its own isolated transport pool.
	baseTr := http.DefaultTransport.(*http.Transport).Clone()
	baseTr.MaxConnsPerHost = 1

	p := New(u, "space", []byte("secret"), baseTr, nil, Hooks{})

	mockDone := make(chan struct{})
	go func() {
		defer close(mockDone)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"sim","mock":true}`))
		p.ServeHTTP(rec, WithMock(WithAttendee(req, "attendee-01"), true))
	}()
	<-mockEntered

	if got := p.SimRouterInFlight(); got != 1 {
		t.Fatalf("SimRouterInFlight() = %d, want 1 while mock turn is parked", got)
	}

	humanDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"human"}`))
		p.ServeHTTP(rec, WithMock(WithAttendee(req, "attendee-4096"), false))
		humanDone <- rec.Code
	}()

	select {
	case code := <-humanDone:
		if code != http.StatusOK {
			t.Fatalf("human turn status = %d, want 200", code)
		}
	case <-mockDone:
		t.Fatal("mock turn finished unexpectedly")
	}

	close(releaseMock)
	<-mockDone
}

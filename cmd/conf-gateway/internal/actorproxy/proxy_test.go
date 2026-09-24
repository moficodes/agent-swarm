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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
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
	p := New(u, "space", []byte("secret"), nil, Hooks{
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
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, WithAttendee(req, "attendee-01"))

	for _, h := range []string{"Authorization", "Cookie", auth.PresenterHeaderName} {
		if got.Get(h) != "" {
			t.Errorf("router received %s", h)
		}
	}
	if got.Get("ate-target-actor") != "space/attendee-01" {
		t.Errorf("ate-target-actor = %q", got.Get("ate-target-actor"))
	}
	if want := modelproxy.Key([]byte("secret"), "space", "attendee-01"); got.Get(types.ModelKeyHeader) != want {
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
	p := New(u, "space", []byte("s"), nil, Hooks{
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
	p := New(nil, "space", []byte("s"), nil, Hooks{})
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
	p := New(nil, "space", []byte("s"), nil, Hooks{})
	if rec, _, _, _, ok := claim(p, "attendee-01", `not json`); ok || rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: ok=%v status=%d", ok, rec.Code)
	}
}

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
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const testPresenterKey = "test-presenter-key"

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func presenterCookie() *http.Cookie {
	return &http.Cookie{Name: gateway.PresenterCookieName, Value: testPresenterKey}
}

func TestPresenterPages_RequireKey(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000")
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)
	client := noRedirectClient()

	for _, page := range []string{"/stage", "/feed"} {
		t.Run(page, func(t *testing.T) {
			resp, err := client.Get(ts.URL + page)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("no cookie: status = %d, want 401", resp.StatusCode)
			}

			resp, err = client.Get(ts.URL + page + "?key=wrong")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("wrong key: status = %d, want 401", resp.StatusCode)
			}
			if len(resp.Cookies()) != 0 {
				t.Errorf("wrong key set cookies: %v", resp.Cookies())
			}

			resp, err = client.Get(ts.URL + page + "?key=" + testPresenterKey)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("right key: status = %d, want 303", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != page {
				t.Errorf("redirect Location = %q, want %q (key stripped)", loc, page)
			}
			var got *http.Cookie
			for _, c := range resp.Cookies() {
				if c.Name == gateway.PresenterCookieName {
					got = c
				}
			}
			if got == nil {
				t.Fatalf("no %s cookie set", gateway.PresenterCookieName)
			}
			if !got.HttpOnly || got.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie HttpOnly=%v SameSite=%v, want HttpOnly and Strict", got.HttpOnly, got.SameSite)
			}

			req, _ := http.NewRequest(http.MethodGet, ts.URL+page, nil)
			req.AddCookie(got)
			resp, err = client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("with cookie: status = %d, want 200", resp.StatusCode)
			}
		})
	}
}

func TestChatPage_IsPublic(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000")
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/chat")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPresenterAPIs_RequirePresenter(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", gateway.WithSimulationController(&mockSimulationController{}))
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	endpoints := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/reset", ""},
		{http.MethodGet, "/api/simulation", ""},
		{http.MethodPost, "/api/simulation", `{"action":"stop"}`},
		{http.MethodGet, "/api/stream", ""},
		{http.MethodGet, "/api/feed/history", ""},
		{http.MethodGet, "/api/actor/attendee-01", ""},
		{http.MethodGet, "/api/stage/telemetry", ""},
	}
	auths := []struct {
		name    string
		apply   func(*http.Request)
		allowed bool
	}{
		{"none", func(*http.Request) {}, false},
		{"attendee-token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+gw.MintToken("attendee-01")) }, false},
		{"wrong-cookie", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: gateway.PresenterCookieName, Value: "nope"})
		}, false},
		{"cookie", func(r *http.Request) { r.AddCookie(presenterCookie()) }, true},
		{"header", func(r *http.Request) { r.Header.Set(gateway.PresenterHeaderName, testPresenterKey) }, true},
	}
	for _, ep := range endpoints {
		for _, a := range auths {
			t.Run(ep.method+" "+ep.path+"/"+a.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, ep.method, ts.URL+ep.path, strings.NewReader(ep.body))
				if ep.body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				a.apply(req)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if a.allowed && resp.StatusCode == http.StatusUnauthorized {
					t.Errorf("status = 401, want allowed")
				}
				if !a.allowed && resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401", resp.StatusCode)
				}
			})
		}
	}
}

func getSession(t *testing.T, url string, withPresenter bool) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if withPresenter {
		req.AddCookie(presenterCookie())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d", resp.StatusCode)
	}
	var sess struct {
		AttendeeID string `json:"attendee_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatal(err)
	}
	return sess.AttendeeID
}

func TestSession_AttendeeOverride(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", gateway.WithMaxActors(10))
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	if got := getSession(t, ts.URL+"/api/session?attendee_id=attendee-07", true); got != "attendee-07" {
		t.Errorf("presenter override: got %q, want attendee-07", got)
	}
	if got := getSession(t, ts.URL+"/api/session?attendee_id=attendee-07", false); got == "attendee-07" {
		t.Errorf("non-presenter was allowed to claim attendee-07")
	}
	for _, bad := range []string{"attendee-00", "attendee-11", "attendee-7x", "attendee-../x", "attendee-"} {
		if got := getSession(t, ts.URL+"/api/session?attendee_id="+bad, true); got == bad {
			t.Errorf("presenter override accepted invalid id %q", bad)
		}
	}
}

func TestEvents_RequireOwnToken(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000")
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"no-token", "?attendee_id=attendee-01", http.StatusUnauthorized},
		{"other-attendee", "?attendee_id=attendee-01&token=" + gw.MintToken("attendee-02"), http.StatusUnauthorized},
		{"garbage", "?attendee_id=attendee-01&token=abc", http.StatusUnauthorized},
		{"own-token", "?attendee_id=attendee-01&token=" + gw.MintToken("attendee-01"), http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events"+c.query, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.want)
			}
		})
	}
}

func TestDefaultSecretIsRandom(t *testing.T) {
	a := newTestGateway(t, "http://localhost:8000")
	b := newTestGateway(t, "http://localhost:8000")
	if _, err := b.VerifyToken(a.MintToken("attendee-01")); err == nil {
		t.Errorf("gateways without WithSecret accepted each other's tokens")
	}
	old := newTestGateway(t, "http://localhost:8000", gateway.WithSecret("conf-demo-secret-key"))
	if _, err := a.VerifyToken(old.MintToken("attendee-01")); err == nil {
		t.Errorf("default gateway accepted a token signed with the old hardcoded secret")
	}
}

func TestDefaultPresenterKeyIsRandom(t *testing.T) {
	a := gateway.NewGateway("http://localhost:8000")
	t.Cleanup(a.Close)
	b := gateway.NewGateway("http://localhost:8000")
	t.Cleanup(b.Close)
	if len(a.PresenterKey()) < 32 || a.PresenterKey() == b.PresenterKey() {
		t.Errorf("presenter keys %q and %q are not long random values", a.PresenterKey(), b.PresenterKey())
	}
}

func signTestIDToken(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	pay, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pay)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestPresenter_GoogleSignIn(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": "k1",
				"n":   base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.PublicKey.E)).Bytes()),
			}},
		})
	}))
	t.Cleanup(jwksSrv.Close)

	gw := newTestGateway(t, "http://localhost:8000", gateway.WithGoogleAuth(auth.GoogleAuthConfig{
		AllowedDomains:    []string{"google.com", "event.goog", "cloud-demos.goog", "gcdemos.goog"},
		ClientID:          "demo-client.apps.googleusercontent.com",
		FirebaseAPIKey:    "AIzaFakeKey",
		FirebaseProjectID: "demo-proj",
		GoogleJWKSURL:     jwksSrv.URL,
		FirebaseJWKSURL:   jwksSrv.URL,
	}))
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	// Unauthenticated /stage renders the Google Sign-In gate listing all 4 allowed domains.
	resp, err := http.Get(ts.URL + "/stage")
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /stage status = %d, want 401", resp.StatusCode)
	}
	body := string(bodyBytes)
	for _, dom := range []string{"@google.com", "@event.goog", "@cloud-demos.goog", "@gcdemos.goog"} {
		if !strings.Contains(body, dom) {
			t.Errorf("GET /stage sign-in page missing domain chip %q", dom)
		}
	}

	// Disallowed domain (@gmail.com) is rejected on POST /api/auth/google.
	badTok := signTestIDToken(t, priv, "k1", map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            "demo-client.apps.googleusercontent.com",
		"email":          "intruder@gmail.com",
		"email_verified": true,
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(time.Hour).Unix(),
	})
	badResp, err := http.Post(ts.URL+"/api/auth/google", "application/json", strings.NewReader(`{"id_token":"`+badTok+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusForbidden {
		t.Errorf("gmail token status = %d, want 403", badResp.StatusCode)
	}

	// Each of the 4 allowed domains succeeds and sets the conf_presenter cookie.
	for _, email := range []string{"a@google.com", "b@event.goog", "c@cloud-demos.goog", "d@gcdemos.goog"} {
		tok := signTestIDToken(t, priv, "k1", map[string]any{
			"iss":            "https://securetoken.google.com/demo-proj",
			"aud":            "demo-proj",
			"email":          email,
			"email_verified": true,
			"iat":            time.Now().Unix(),
			"exp":            time.Now().Add(time.Hour).Unix(),
		})
		okResp, err := http.Post(ts.URL+"/api/auth/google", "application/json", strings.NewReader(`{"id_token":"`+tok+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		var cookie *http.Cookie
		for _, c := range okResp.Cookies() {
			if c.Name == gateway.PresenterCookieName {
				cookie = c
			}
		}
		okResp.Body.Close()
		if okResp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/auth/google (%s) status = %d, want 200", email, okResp.StatusCode)
		}
		if cookie == nil {
			t.Fatalf("POST /api/auth/google (%s) did not set %s cookie", email, gateway.PresenterCookieName)
		}

		// Cookie unlocks /stage and presenter APIs.
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/stage", nil)
		req.AddCookie(cookie)
		stageResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		stageResp.Body.Close()
		if stageResp.StatusCode != http.StatusOK {
			t.Errorf("GET /stage with Google auth cookie (%s) status = %d, want 200", email, stageResp.StatusCode)
		}
	}

	// Logout clears the cookie.
	logoutResp, err := http.Post(ts.URL+"/api/auth/logout", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusOK {
		t.Errorf("POST /api/auth/logout status = %d, want 200", logoutResp.StatusCode)
	}
}

func TestChat_RateLimit10RPM(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:      "msg-1",
			Sender:  types.SenderAssistant,
			Content: "ok",
		})
	}))
	t.Cleanup(upstream.Close)

	gw := newTestGateway(t, upstream.URL, gateway.WithChatRateLimitRPM(10))
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	tok1 := gw.MintToken("attendee-01")
	tok2 := gw.MintToken("attendee-02")

	for i := 1; i <= 10; i++ {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok1)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d for attendee-01: status = %d, want 200", i, resp.StatusCode)
		}
	}

	// 11th request within the minute for attendee-01 is rate-limited (429).
	req11, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"spam"}`))
	req11.Header.Set("Content-Type", "application/json")
	req11.Header.Set("Authorization", "Bearer "+tok1)
	resp11, err := http.DefaultClient.Do(req11)
	if err != nil {
		t.Fatal(err)
	}
	resp11.Body.Close()
	if resp11.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th request for attendee-01: status = %d, want 429", resp11.StatusCode)
	}

	// attendee-02 has its own independent 10 RPM bucket and still succeeds.
	reqOther, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"hello"}`))
	reqOther.Header.Set("Content-Type", "application/json")
	reqOther.Header.Set("Authorization", "Bearer "+tok2)
	respOther, err := http.DefaultClient.Do(reqOther)
	if err != nil {
		t.Fatal(err)
	}
	respOther.Body.Close()
	if respOther.StatusCode != http.StatusOK {
		t.Fatalf("attendee-02 status = %d, want 200", respOther.StatusCode)
	}
}

func TestSession_RateLimit10RPM(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", gateway.WithSessionRateLimitRPM(10))
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	for i := 1; i <= 10; i++ {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/session", nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.10")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("session request %d: status = %d, want 200", i, resp.StatusCode)
		}
	}

	// 11th request from the same IP is rate-limited (429).
	req11, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/session", nil)
	req11.Header.Set("X-Forwarded-For", "198.51.100.10")
	resp11, err := http.DefaultClient.Do(req11)
	if err != nil {
		t.Fatal(err)
	}
	resp11.Body.Close()
	if resp11.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th session request: status = %d, want 429", resp11.StatusCode)
	}

	// Another attendee IP still succeeds.
	reqOtherIP, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/session", nil)
	reqOtherIP.Header.Set("X-Forwarded-For", "198.51.100.99")
	respOtherIP, err := http.DefaultClient.Do(reqOtherIP)
	if err != nil {
		t.Fatal(err)
	}
	respOtherIP.Body.Close()
	if respOtherIP.StatusCode != http.StatusOK {
		t.Fatalf("other IP session status = %d, want 200", respOtherIP.StatusCode)
	}

	// Presenter-authenticated requests are exempt from the session rate limit.
	reqPres, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/session", nil)
	reqPres.Header.Set("X-Forwarded-For", "198.51.100.10")
	reqPres.Header.Set(gateway.PresenterHeaderName, testPresenterKey)
	respPres, err := http.DefaultClient.Do(reqPres)
	if err != nil {
		t.Fatal(err)
	}
	respPres.Body.Close()
	if respPres.StatusCode != http.StatusOK {
		t.Fatalf("presenter session status = %d, want 200", respPres.StatusCode)
	}
}

func TestSession_ProtectsSlotWithPendingApproval(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == types.ClearHistoryPath {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			ID:      "msg-appr",
			Sender:  types.SenderAssistant,
			Content: "Requires approval",
			Approval: &types.ApprovalRequest{
				ApprovalID: "appr-1",
				Summary:    "Run script",
				Code:       "print(1)",
				Language:   "python",
				RiskLevel:  "LOW",
			},
		})
	}))
	t.Cleanup(upstream.Close)

	gw := newTestGateway(t, upstream.URL, gateway.WithMaxActors(3))
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	// Mint 3 sessions: attendee-01, attendee-02, attendee-03.
	if got := getSession(t, ts.URL+"/api/session", false); got != "attendee-01" {
		t.Fatalf("slot 1 = %q, want attendee-01", got)
	}
	if got := getSession(t, ts.URL+"/api/session", false); got != "attendee-02" {
		t.Fatalf("slot 2 = %q, want attendee-02", got)
	}
	if got := getSession(t, ts.URL+"/api/session", false); got != "attendee-03" {
		t.Fatalf("slot 3 = %q, want attendee-03", got)
	}

	// Put attendee-01 into AWAITING_APPROVAL.
	tok1 := gw.MintToken("attendee-01")
	chatReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/chat", strings.NewReader(`{"message":"scan"}`))
	chatReq.Header.Set("Content-Type", "application/json")
	chatReq.Header.Set("Authorization", "Bearer "+tok1)
	chatResp, err := http.DefaultClient.Do(chatReq)
	if err != nil {
		t.Fatal(err)
	}
	chatResp.Body.Close()

	// Next session wrap-around would normally hit attendee-01, but attendee-01
	// has a pending approval so it must be skipped in favor of attendee-02!
	if got := getSession(t, ts.URL+"/api/session", false); got != "attendee-02" {
		t.Fatalf("wrapped session = %q, want attendee-02 (skipping active attendee-01)", got)
	}
}

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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
)

const testPresenterKey = "test-presenter-key"

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func presenterCookie() *http.Cookie {
	return &http.Cookie{Name: gateway.PresenterCookieName, Value: testPresenterKey}
}

func TestPresenterPages_RequireKey(t *testing.T) {
	gw := newTestGateway(t, "http://localhost:8000", "")
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)
	client := noRedirectClient()

	for _, page := range []string{"/stage", "/feed", "/"} {
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
	gw := newTestGateway(t, "http://localhost:8000", "")
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
	gw := newTestGateway(t, "http://localhost:8000", "", gateway.WithSimulationController(&mockSimulationController{}))
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
	gw := newTestGateway(t, "http://localhost:8000", "", gateway.WithMaxActors(10))
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
	gw := newTestGateway(t, "http://localhost:8000", "")
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

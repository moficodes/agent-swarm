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

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSigner_MintVerifyRoundTrip(t *testing.T) {
	s := NewSigner([]byte("secret"))
	claims, err := s.Verify(s.Mint("attendee-07"))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Sub != "attendee-07" || claims.Scope != "agent:chat" {
		t.Errorf("claims = %+v, want sub attendee-07 and scope agent:chat", claims)
	}
}

func TestSigner_RejectsTamperedToken(t *testing.T) {
	s := NewSigner([]byte("secret"))
	other := strings.Split(s.Mint("attendee-02"), ".")
	parts := strings.Split(s.Mint("attendee-01"), ".")
	// Payload of one token with the signature of another.
	parts[1] = other[1]
	if _, err := s.Verify(strings.Join(parts, ".")); err == nil {
		t.Fatal("Verify accepted a token whose payload was swapped")
	}
	if _, err := s.Verify("not-a-jwt"); err == nil {
		t.Fatal("Verify accepted a malformed token")
	}
}

func TestSigner_RejectsWrongSecret(t *testing.T) {
	tok := NewSigner([]byte("secret-a")).Mint("attendee-01")
	if _, err := NewSigner([]byte("secret-b")).Verify(tok); err == nil {
		t.Fatal("Verify accepted a token signed with another secret")
	}
}

func TestSigner_Authenticate(t *testing.T) {
	s := NewSigner([]byte("secret"))
	tok := s.Mint("attendee-03")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	if got, err := s.Authenticate(r); err != nil || got != "attendee-03" {
		t.Fatalf("Authenticate = %q, %v; want attendee-03", got, err)
	}

	r.Header.Set("X-Attendee-ID", "attendee-04")
	if _, err := s.Authenticate(r); err == nil {
		t.Fatal("Authenticate accepted a mismatched X-Attendee-ID")
	}

	if _, err := s.Authenticate(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
		t.Fatal("Authenticate accepted a request without a token")
	}
}

func TestValidAttendeeID(t *testing.T) {
	for id, want := range map[string]bool{
		"attendee-01":  true,
		"attendee-50":  true,
		"attendee-51":  false,
		"attendee-00":  false,
		"attendee-1":   false,
		"attendee-001": false,
		"attendee-xx":  false,
		"bob":          false,
	} {
		if got := ValidAttendeeID(id, 50); got != want {
			t.Errorf("ValidAttendeeID(%q, 50) = %v, want %v", id, got, want)
		}
	}
}

func TestPresenterOK(t *testing.T) {
	const key = "presenter-key"
	withHeader := func(v string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set(PresenterHeaderName, v)
		return r
	}
	withCookie := httptest.NewRequest(http.MethodGet, "/", nil)
	withCookie.AddCookie(&http.Cookie{Name: PresenterCookieName, Value: key})

	cases := []struct {
		name string
		r    *http.Request
		key  string
		want bool
	}{
		{"header", withHeader(key), key, true},
		{"cookie", withCookie, key, true},
		{"wrong key", withHeader("nope"), key, false},
		{"no key", httptest.NewRequest(http.MethodGet, "/", nil), key, false},
		{"empty presenter key", httptest.NewRequest(http.MethodGet, "/", nil), "", false},
	}
	for _, c := range cases {
		if got := PresenterOK(c.r, c.key); got != c.want {
			t.Errorf("%s: PresenterOK = %v, want %v", c.name, got, c.want)
		}
	}
}

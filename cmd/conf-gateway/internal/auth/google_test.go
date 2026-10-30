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

package auth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
)

func signTestRS256(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdrBytes, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
		"kid": kid,
	})
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hdrBytes) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func startTestJWKSServer(t *testing.T, kid string, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	payload := map[string]any{
		"keys": []map[string]string{
			{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": kid,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGoogleVerifier_VerifyIDToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwksSrv := startTestJWKSServer(t, "key-1", &priv.PublicKey)
	now := time.Unix(1700000000, 0)

	v := auth.NewGoogleVerifier(auth.GoogleAuthConfig{
		AllowedDomains:    []string{"google.com", "@Event.goog", "cloud-demos.goog, gcdemos.goog"},
		ClientID:          "test-client-id.apps.googleusercontent.com",
		FirebaseProjectID: "demo-proj",
		GoogleJWKSURL:     jwksSrv.URL,
		FirebaseJWKSURL:   jwksSrv.URL,
		Now:               func() time.Time { return now },
	})
	if v == nil {
		t.Fatal("expected non-nil GoogleVerifier")
	}

	validDomains := []string{
		"alice@google.com",
		"bob@event.goog",
		"carol@cloud-demos.goog",
		"dave@gcdemos.goog",
		"UpperCase@Google.COM",
	}
	for _, email := range validDomains {
		tok := signTestRS256(t, priv, "key-1", map[string]any{
			"iss":            "https://accounts.google.com",
			"aud":            "test-client-id.apps.googleusercontent.com",
			"email":          email,
			"email_verified": true,
			"iat":            now.Unix(),
			"exp":            now.Add(time.Hour).Unix(),
		})
		got, err := v.VerifyIDToken(context.Background(), tok)
		if err != nil {
			t.Errorf("VerifyIDToken(%q) unexpected error: %v", email, err)
		}
		if got != strings.ToLower(email) {
			t.Errorf("VerifyIDToken(%q) = %q, want %q", email, got, strings.ToLower(email))
		}
	}

	// Firebase ID token issuer & audience should also succeed.
	firebaseTok := signTestRS256(t, priv, "key-1", map[string]any{
		"iss":            "https://securetoken.google.com/demo-proj",
		"aud":            "demo-proj",
		"email":          "presenter@event.goog",
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	})
	if got, err := v.VerifyIDToken(context.Background(), firebaseTok); err != nil || got != "presenter@event.goog" {
		t.Errorf("Firebase token: got (%q, %v), want (presenter@event.goog, nil)", got, err)
	}

	rejectCases := []struct {
		name   string
		claims map[string]any
	}{
		{
			name: "disallowed-gmail",
			claims: map[string]any{
				"iss":            "https://accounts.google.com",
				"aud":            "test-client-id.apps.googleusercontent.com",
				"email":          "user@gmail.com",
				"email_verified": true,
				"iat":            now.Unix(),
				"exp":            now.Add(time.Hour).Unix(),
			},
		},
		{
			name: "unverified-email",
			claims: map[string]any{
				"iss":            "https://accounts.google.com",
				"aud":            "test-client-id.apps.googleusercontent.com",
				"email":          "user@google.com",
				"email_verified": false,
				"iat":            now.Unix(),
				"exp":            now.Add(time.Hour).Unix(),
			},
		},
		{
			name: "mismatched-hd",
			claims: map[string]any{
				"iss":            "https://accounts.google.com",
				"aud":            "test-client-id.apps.googleusercontent.com",
				"email":          "user@google.com",
				"hd":             "other.com",
				"email_verified": true,
				"iat":            now.Unix(),
				"exp":            now.Add(time.Hour).Unix(),
			},
		},
		{
			name: "expired",
			claims: map[string]any{
				"iss":            "https://accounts.google.com",
				"aud":            "test-client-id.apps.googleusercontent.com",
				"email":          "user@google.com",
				"email_verified": true,
				"iat":            now.Add(-2 * time.Hour).Unix(),
				"exp":            now.Add(-time.Minute).Unix(),
			},
		},
		{
			name: "wrong-audience",
			claims: map[string]any{
				"iss":            "https://accounts.google.com",
				"aud":            "other-client.apps.googleusercontent.com",
				"email":          "user@google.com",
				"email_verified": true,
				"iat":            now.Unix(),
				"exp":            now.Add(time.Hour).Unix(),
			},
		},
		{
			name: "wrong-issuer",
			claims: map[string]any{
				"iss":            "https://evil.example.com",
				"aud":            "test-client-id.apps.googleusercontent.com",
				"email":          "user@google.com",
				"email_verified": true,
				"iat":            now.Unix(),
				"exp":            now.Add(time.Hour).Unix(),
			},
		},
	}

	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			tok := signTestRS256(t, priv, "key-1", tc.claims)
			if got, err := v.VerifyIDToken(context.Background(), tok); err == nil {
				t.Errorf("expected error for %s, got email %q", tc.name, got)
			}
		})
	}
}

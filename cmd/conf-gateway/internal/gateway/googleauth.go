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

package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
)

// WithGoogleAuth configures Google Sign-In / OIDC authentication for presenter
// pages (/stage and /feed). When cfg.Enabled() is false, presenter pages fall
// back to the PRESENTER_TOKEN prompt.
func WithGoogleAuth(cfg auth.GoogleAuthConfig) Option {
	return func(g *Gateway) {
		g.googleVerifier = auth.NewGoogleVerifier(cfg)
	}
}

type googleAuthRequest struct {
	IDToken string `json:"id_token"`
}

func (g *Gateway) handleGoogleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.googleVerifier == nil {
		http.Error(w, "Google Sign-In is not configured", http.StatusNotFound)
		return
	}

	var req googleAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.IDToken) == "" {
		http.Error(w, "missing id_token", http.StatusBadRequest)
		return
	}

	email, err := g.googleVerifier.VerifyIDToken(r.Context(), req.IDToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.PresenterCookieName,
		Value:    g.presenterKey,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	g.mu.Lock()
	g.addEventLocked(fmt.Sprintf("Presenter signed in (%s)", email))
	g.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"email":  email,
	})
}

func (g *Gateway) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.PresenterCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

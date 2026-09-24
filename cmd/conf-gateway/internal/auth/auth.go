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

// Package auth signs and verifies attendee tokens and checks the presenter key.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// PresenterCookieName is the cookie a presenter page sets once the
	// presenter key is supplied.
	PresenterCookieName = "conf_presenter"
	// PresenterHeaderName carries the presenter key for scripts.
	PresenterHeaderName = "X-Presenter-Key"
)

// Claims represents the decoded JWT payload.
type Claims struct {
	Sub   string `json:"sub"`
	Exp   int64  `json:"exp"`
	Scope string `json:"scope,omitempty"`
}

// Signer mints and verifies HS256 attendee tokens.
type Signer struct {
	secret []byte
}

// NewSigner returns a Signer that signs with secret.
func NewSigner(secret []byte) *Signer {
	return &Signer{secret: secret}
}

// Mint creates a signed HS256 JWT for the specified attendee ID.
func (s *Signer) Mint(attendeeID string) string {
	headerJSON := `{"alg":"HS256","typ":"JWT"}`
	header := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	claims := Claims{
		Sub:   attendeeID,
		Exp:   time.Now().Add(2 * time.Hour).Unix(),
		Scope: "agent:chat",
	}
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)

	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature
}

// Verify validates an HS256 JWT string and returns the decoded claims.
func (s *Signer) Verify(tokenStr string) (*Claims, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format: must contain 3 parts")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(signingInput))
	expectedSig := mac.Sum(nil)

	actualSig, err := decodeBase64URL(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid token signature encoding: %w", err)
	}

	if !hmac.Equal(actualSig, expectedSig) {
		return nil, errors.New("invalid token signature")
	}

	payloadBytes, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid token payload encoding: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("invalid token claims: %w", err)
	}

	if claims.Exp <= 0 || claims.Exp <= time.Now().Unix() {
		return nil, errors.New("token expired")
	}

	return &claims, nil
}

// Authenticate returns the attendee a request's token was minted for. The
// token comes from a Bearer Authorization header or X-Attendee-Token; an
// X-Attendee-ID header, when present, must name the same attendee.
func (s *Signer) Authenticate(r *http.Request) (string, error) {
	var tokenStr string
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "Bearer ") {
		tokenStr = strings.TrimPrefix(authHdr, "Bearer ")
	} else if tok := r.Header.Get("X-Attendee-Token"); tok != "" {
		tokenStr = tok
	}

	if tokenStr == "" {
		return "", errors.New("missing authorization token")
	}

	claims, err := s.Verify(tokenStr)
	if err != nil {
		return "", fmt.Errorf("invalid token: %w", err)
	}

	if claims.Sub == "" {
		return "", errors.New("token missing subject")
	}

	if claims.Exp <= 0 || claims.Exp <= time.Now().Unix() {
		return "", errors.New("token expired")
	}

	if reqAttendeeID := r.Header.Get("X-Attendee-ID"); reqAttendeeID != "" {
		if reqAttendeeID != claims.Sub {
			return "", fmt.Errorf("mismatched attendee ID: header=%s, token=%s", reqAttendeeID, claims.Sub)
		}
	}

	return claims.Sub, nil
}

func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// ValidAttendeeID reports whether id is a canonical "attendee-NN" name for one
// of the maxActors pre-provisioned actors.
func ValidAttendeeID(id string, maxActors int) bool {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "attendee-"))
	return err == nil && n >= 1 && n <= maxActors && id == fmt.Sprintf("attendee-%02d", n)
}

// PresenterOK reports whether the request carries the presenter key, either in
// the cookie set by a presenter page or in the PresenterHeaderName header.
func PresenterOK(r *http.Request, presenterKey string) bool {
	key := r.Header.Get(PresenterHeaderName)
	if key == "" {
		if c, err := r.Cookie(PresenterCookieName); err == nil {
			key = c.Value
		}
	}
	return key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(presenterKey)) == 1
}

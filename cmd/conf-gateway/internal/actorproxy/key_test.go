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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestModelKey_DiffersFromJWTSigningInput(t *testing.T) {
	secret := []byte("secret")
	a := ModelKey(secret, "space", "attendee-01")
	if !strings.HasPrefix(a, "attendee-01.") {
		t.Fatalf("ModelKey = %q, want actor prefix", a)
	}
	if a == ModelKey(secret, "space", "attendee-02") {
		t.Error("keys for different actors are equal")
	}
	if a == ModelKey(secret, "other", "attendee-01") {
		t.Error("keys for different atespaces are equal")
	}

	h := hmac.New(sha256.New, secret)
	h.Write([]byte("space/attendee-01"))
	undomained := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	if got := strings.TrimPrefix(a, "attendee-01."); got == undomained {
		t.Errorf("ModelKey MAC %q equals HMAC over the undomained JWT signing input", got)
	}
}

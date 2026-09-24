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
)

// ModelKey returns one actor's key: "<actor>.<mac>". The gateway sends it on
// every request so the actor can check that later calls, such as
// ClearHistory, come from the same gateway. The MAC input is prefixed so the
// key never matches another use of the secret, such as the HMAC that signs
// attendee JWTs.
func ModelKey(secret []byte, atespace, actor string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("model-key/" + atespace + "/" + actor))
	return actor + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

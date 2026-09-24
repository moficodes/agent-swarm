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

package modelproxy_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/geminitest"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const (
	testAtespace = "ate-demo-conf"
	goodPath     = "/model/v1beta1/projects/proj/locations/loc/publishers/google/models/gemini-2.5-flash:generateContent"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func newProxy(t *testing.T, upstream string, mutate ...func(*modelproxy.Config)) *modelproxy.Proxy {
	t.Helper()
	cfg := modelproxy.Config{
		Secret:      testSecret,
		Atespace:    testAtespace,
		Project:     "proj",
		Location:    "loc",
		Models:      []string{"gemini-2.5-flash"},
		Upstream:    upstream,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"}),
	}
	for _, m := range mutate {
		m(&cfg)
	}
	p, err := modelproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func call(p http.Handler, method, path, bearer string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestProxy_ForwardsWithADCTokenAndStripsActorKey(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	req := httptest.NewRequest(http.MethodPost, goodPath+"?alt=sse", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(types.ModelKeyHeader, key)
	req.Header.Set("Cookie", "session=x")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := vertex.Requests()[0]
	if got.Authorization != "Bearer adc-token" {
		t.Errorf("upstream Authorization = %q, want the ADC token", got.Authorization)
	}
	if got.ModelKey != "" {
		t.Errorf("upstream saw %s = %q", types.ModelKeyHeader, got.ModelKey)
	}
	if want := strings.TrimPrefix(goodPath, "/model"); got.Path != want {
		t.Errorf("upstream path = %q, want %q", got.Path, want)
	}
}

func TestProxy_RejectsBadKeys(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	good := modelproxy.Key(testSecret, testAtespace, "attendee-01")
	otherSpace := modelproxy.Key(testSecret, "other-space", "attendee-01")
	otherSecret := modelproxy.Key([]byte("another-secret-another-secret-00"), testAtespace, "attendee-01")
	forgedActor := "attendee-02" + good[strings.LastIndex(good, "."):]

	for name, bearer := range map[string]string{
		"missing":      "",
		"garbage":      "not-a-key",
		"other space":  otherSpace,
		"other secret": otherSecret,
		"forged actor": forgedActor,
		"no actor":     good[strings.LastIndex(good, "."):],
	} {
		t.Run(name, func(t *testing.T) {
			if rec := call(p, http.MethodPost, goodPath, bearer, []byte(`{}`)); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
	if n := len(vertex.Requests()); n != 0 {
		t.Errorf("upstream got %d requests, want 0", n)
	}
}

func TestProxy_AllowsOnlyConfiguredGenerateContent(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	for name, tc := range map[string]struct{ method, path string }{
		"GET":               {http.MethodGet, goodPath},
		"other project":     {http.MethodPost, strings.Replace(goodPath, "/proj/", "/evil/", 1)},
		"other location":    {http.MethodPost, strings.Replace(goodPath, "/loc/", "/us-east1/", 1)},
		"model not allowed": {http.MethodPost, strings.Replace(goodPath, "gemini-2.5-flash", "gemini-2.5-pro", 1)},
		"streaming":         {http.MethodPost, strings.Replace(goodPath, ":generateContent", ":streamGenerateContent", 1)},
		"other method":      {http.MethodPost, strings.Replace(goodPath, ":generateContent", ":countTokens", 1)},
		"no prefix":         {http.MethodPost, strings.TrimPrefix(goodPath, "/model")},
		"encoded slash":     {http.MethodPost, strings.Replace(goodPath, "/models/", "/models%2F", 1)},
		"traversal":         {http.MethodPost, "/model/v1beta1/projects/proj/locations/loc/publishers/google/models/../../x:generateContent"},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := call(p, tc.method, tc.path, key, []byte(`{}`)); rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
		})
	}
	if n := len(vertex.Requests()); n != 0 {
		t.Errorf("upstream got %d requests, want 0", n)
	}
}

func TestProxy_BodyCap(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL, func(c *modelproxy.Config) { c.MaxBody = 16 })
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	if rec := call(p, http.MethodPost, goodPath, key, bytes.Repeat([]byte("a"), 17)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

// blockingUpstream holds every request until release is closed.
func blockingUpstream(t *testing.T) (url string, entered <-chan struct{}, release chan struct{}) {
	t.Helper()
	in := make(chan struct{}, 8)
	rel := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in <- struct{}{}
		<-rel
		_, _ = w.Write([]byte(geminitest.TextResponse("ok")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-rel:
		default:
			close(rel)
		}
	})
	return srv.URL, in, rel
}

func TestProxy_PerActorConcurrencyCap(t *testing.T) {
	url, entered, release := blockingUpstream(t)
	p := newProxy(t, url, func(c *modelproxy.Config) { c.MaxInFlight = 1 })
	key1 := modelproxy.Key(testSecret, testAtespace, "attendee-01")
	key2 := modelproxy.Key(testSecret, testAtespace, "attendee-02")

	done := make(chan int)
	go func() { done <- call(p, http.MethodPost, goodPath, key1, []byte(`{}`)).Code }()
	<-entered

	if rec := call(p, http.MethodPost, goodPath, key1, []byte(`{}`)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second call for same actor = %d, want 429", rec.Code)
	}
	go func() { done <- call(p, http.MethodPost, goodPath, key2, []byte(`{}`)).Code }()
	<-entered // another actor is not limited by attendee-01's calls

	close(release)
	for range 2 {
		if code := <-done; code != http.StatusOK {
			t.Errorf("held call = %d, want 200", code)
		}
	}
}

func TestProxy_RecordsUsagePerActor(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	key := modelproxy.Key(testSecret, testAtespace, "attendee-07")

	call(p, http.MethodPost, goodPath, key, []byte(`{}`))
	call(p, http.MethodPost, goodPath, key, []byte(`{}`))

	u := p.Usage("attendee-07")
	if u.Calls != 2 || u.PromptTokens != 22 || u.CandidateTokens != 14 {
		t.Errorf("Usage = %+v, want 2 calls, 22 prompt, 14 candidate tokens", u)
	}
	if other := p.Usage("attendee-01"); other.Calls != 0 {
		t.Errorf("attendee-01 usage = %+v, want zero", other)
	}
	if total := p.TotalUsage(); total.Calls != 2 {
		t.Errorf("TotalUsage = %+v", total)
	}
	p.ResetUsage()
	if u := p.Usage("attendee-07"); u.Calls != 0 {
		t.Errorf("after ResetUsage = %+v", u)
	}
}

func TestProxy_DisabledWithoutProject(t *testing.T) {
	p := newProxy(t, "http://unused", func(c *modelproxy.Config) { c.Project = ""; c.TokenSource = nil })
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	if p.Enabled() {
		t.Error("Enabled() = true, want false")
	}
	if rec := call(p, http.MethodPost, goodPath, key, []byte(`{}`)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestNew_RequiresSecretAndTokenSource(t *testing.T) {
	if _, err := modelproxy.New(modelproxy.Config{Project: "p", Location: "l", TokenSource: oauth2.StaticTokenSource(&oauth2.Token{})}); err == nil {
		t.Error("New without Secret succeeded")
	}
	if _, err := modelproxy.New(modelproxy.Config{Secret: testSecret, Project: "p", Location: "l"}); err == nil {
		t.Error("New without TokenSource succeeded while enabled")
	}
}

// TestKey_DiffersFromJWTSigningInput asserts true domain separation: Key
// must not equal an HMAC over the undomained "atespace/actor" input, since
// the same HMAC_SECRET also signs attendee JWTs.
func TestKey_DiffersFromJWTSigningInput(t *testing.T) {
	a := modelproxy.Key(testSecret, testAtespace, "attendee-01")
	if !strings.HasPrefix(a, "attendee-01.") {
		t.Fatalf("Key = %q, want actor prefix", a)
	}
	if a == modelproxy.Key(testSecret, testAtespace, "attendee-02") {
		t.Error("keys for different actors are equal")
	}

	h := hmac.New(sha256.New, testSecret)
	h.Write([]byte(testAtespace + "/attendee-01"))
	undomained := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	if got := strings.TrimPrefix(a, "attendee-01."); got == undomained {
		t.Errorf("Key MAC %q equals HMAC over the undomained JWT signing input; domain separation is broken", got)
	}
}

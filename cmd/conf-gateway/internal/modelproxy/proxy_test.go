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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/agent-swarm/internal/workload/geminitest"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const goodPath = "/v1beta1/projects/proj/locations/loc/publishers/google/models/gemini-3.8-flash:generateContent"

func newProxy(t *testing.T, upstream string, mutate ...func(*modelproxy.Config)) *modelproxy.Proxy {
	t.Helper()
	cfg := modelproxy.Config{
		Project:     "proj",
		Location:    "loc",
		Models:      []string{"gemini-3.8-flash"},
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

func call(p *modelproxy.Proxy, actor, path string, body []byte) types.ModelResult {
	return p.Call(context.Background(), actor, types.ModelCall{CallID: "c1", Path: path, Body: body})
}

func TestCall_ForwardsWithADCToken(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)

	res := call(p, "attendee-01", goodPath, []byte(`{"contents":[]}`))

	if res.Status != http.StatusOK || res.CallID != "c1" || !strings.Contains(string(res.Body), `"ok"`) {
		t.Fatalf("result = %d %s %s", res.Status, res.CallID, res.Body)
	}
	got := vertex.Requests()[0]
	if got.Authorization != "Bearer adc-token" {
		t.Errorf("upstream Authorization = %q, want the ADC token", got.Authorization)
	}
	if got.Method != http.MethodPost || got.Path != goodPath || string(got.Body) != `{"contents":[]}` {
		t.Errorf("upstream request = %s %s %s", got.Method, got.Path, got.Body)
	}
}

func TestCall_PassesUpstreamErrorsThrough(t *testing.T) {
	vertex := geminitest.NewStatus(t, http.StatusTooManyRequests)
	p := newProxy(t, vertex.URL)
	if res := call(p, "attendee-01", goodPath, []byte(`{}`)); res.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want Vertex's 429", res.Status)
	}
}

func TestCall_AllowsOnlyConfiguredGenerateContent(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)

	for name, path := range map[string]string{
		"other project":     strings.Replace(goodPath, "/proj/", "/evil/", 1),
		"other location":    strings.Replace(goodPath, "/loc/", "/us-east1/", 1),
		"model not allowed": strings.Replace(goodPath, "gemini-3.8-flash", "gemini-3.8-pro", 1),
		"streaming":         strings.Replace(goodPath, ":generateContent", ":streamGenerateContent", 1),
		"other method":      strings.Replace(goodPath, ":generateContent", ":countTokens", 1),
		"old proxy prefix":  "/model" + goodPath,
		"encoded slash":     strings.Replace(goodPath, "/models/", "/models%2F", 1),
		"encoded model":     strings.Replace(goodPath, "gemini-3.8-flash", "gemini-3.8-flash%3F", 1),
		"query":             goodPath + "?alt=sse",
		"fragment":          goodPath + "#x",
		"traversal":         "/v1beta1/projects/proj/locations/loc/publishers/google/models/../../x:generateContent",
		"empty":             "",
	} {
		t.Run(name, func(t *testing.T) {
			if res := call(p, "attendee-01", path, []byte(`{}`)); res.Status != http.StatusForbidden {
				t.Errorf("status = %d, want 403", res.Status)
			}
		})
	}
	if n := len(vertex.Requests()); n != 0 {
		t.Errorf("upstream got %d requests, want 0", n)
	}
}

func TestCall_BodyCap(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL, func(c *modelproxy.Config) { c.MaxBody = 16 })

	if res := call(p, "attendee-01", goodPath, bytes.Repeat([]byte("a"), 17)); res.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.Status)
	}
	if n := len(vertex.Requests()); n != 0 {
		t.Errorf("upstream got %d requests, want 0", n)
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

func TestCall_PerActorConcurrencyCap(t *testing.T) {
	url, entered, release := blockingUpstream(t)
	p := newProxy(t, url, func(c *modelproxy.Config) { c.MaxInFlight = 1 })

	done := make(chan int)
	go func() { done <- call(p, "attendee-01", goodPath, []byte(`{}`)).Status }()
	<-entered

	if res := call(p, "attendee-01", goodPath, []byte(`{}`)); res.Status != http.StatusTooManyRequests {
		t.Errorf("second call for same actor = %d, want 429", res.Status)
	}
	go func() { done <- call(p, "attendee-02", goodPath, []byte(`{}`)).Status }()
	<-entered // another actor is not limited by attendee-01's calls

	close(release)
	for range 2 {
		if code := <-done; code != http.StatusOK {
			t.Errorf("held call = %d, want 200", code)
		}
	}
}

func TestCall_RecordsUsagePerActor(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)

	call(p, "attendee-07", goodPath, []byte(`{}`))
	call(p, "attendee-07", goodPath, []byte(`{}`))

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

func TestCall_DisabledWithoutProject(t *testing.T) {
	p := newProxy(t, "http://unused", func(c *modelproxy.Config) { c.Project = ""; c.TokenSource = nil })

	if p.Enabled() {
		t.Error("Enabled() = true, want false")
	}
	if res := call(p, "attendee-01", goodPath, []byte(`{}`)); res.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.Status)
	}
}

func TestNew_RequiresTokenSourceWhenEnabled(t *testing.T) {
	if _, err := modelproxy.New(modelproxy.Config{Project: "p", Location: "l"}); err == nil {
		t.Error("New without TokenSource succeeded while enabled")
	}
}

func TestNew_ValidatesLocationAndUpstream(t *testing.T) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{})
	for _, tc := range []struct {
		location, upstream string
		ok                 bool
	}{
		{"global", "", true},
		{"us-central1", "", true},
		{"europe-west4", "https://europe-west4-aiplatform.googleapis.com", true},
		{"loc", "http://127.0.0.1:8080", true},
		{"x.evil.com/#", "", false},
		{"US-CENTRAL1", "", false},
		{"us-central1", "http://aiplatform.example.com", false},
		{"us-central1", "https://", false},
		{"us-central1", "https://host/path", false},
		{"us-central1", "https://user@host", false},
		{"us-central1", "https://host?q=1", false},
	} {
		_, err := modelproxy.New(modelproxy.Config{Project: "p", Location: tc.location, Upstream: tc.upstream, TokenSource: ts})
		if (err == nil) != tc.ok {
			t.Errorf("New(location %q, upstream %q) error = %v, want ok=%v", tc.location, tc.upstream, err, tc.ok)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCall_DefaultUpstreamMatchesLocation(t *testing.T) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"})
	for _, tc := range []struct {
		location string
		wantHost string
	}{
		{"global", "aiplatform.googleapis.com"},
		{"us-central1", "us-central1-aiplatform.googleapis.com"},
	} {
		t.Run(tc.location, func(t *testing.T) {
			var gotScheme, gotHost, gotPath string
			p, err := modelproxy.New(modelproxy.Config{
				Project:     "proj",
				Location:    tc.location,
				Models:      []string{"gemini-3.8-flash"},
				TokenSource: ts,
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					gotScheme, gotHost, gotPath = r.URL.Scheme, r.URL.Host, r.URL.Path
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       http.NoBody,
						Header:     make(http.Header),
					}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			path := "/v1beta1/projects/proj/locations/" + tc.location + "/publishers/google/models/gemini-3.8-flash:generateContent"
			if res := call(p, "attendee-01", path, []byte(`{}`)); res.Status != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", res.Status, res.Body)
			}
			if gotScheme != "https" || gotHost != tc.wantHost || gotPath != path {
				t.Errorf("upstream URL = %s://%s%s, want https://%s%s", gotScheme, gotHost, gotPath, tc.wantHost, path)
			}
		})
	}
}

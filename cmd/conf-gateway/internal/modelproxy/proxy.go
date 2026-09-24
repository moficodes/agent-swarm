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

package modelproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const (
	defaultTimeout     = 30 * time.Second
	defaultMaxBody     = 256 << 10
	defaultMaxInFlight = 2
	maxResponseBody    = 8 << 20
)

// locationPattern matches a GCP region such as us-central1. The location
// becomes part of the host that receives the gateway's access token.
var locationPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

var generatePath = regexp.MustCompile(`^/model/(v1[a-z0-9]*)/projects/([^/]+)/locations/([^/]+)/publishers/google/models/([^/:]+):generateContent$`)

// Config configures a Proxy.
type Config struct {
	Secret      []byte
	Atespace    string
	Project     string
	Location    string
	Models      []string
	Upstream    string
	TokenSource oauth2.TokenSource
	Timeout     time.Duration
	MaxBody     int64
	MaxInFlight int
	Transport   http.RoundTripper
}

// Proxy authenticates actors by model key and forwards allowlisted
// generateContent calls to Vertex AI.
type Proxy struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	inFlight map[string]int
	usage    map[string]types.ModelUsage
}

// New validates cfg and returns a Proxy. An empty Project yields a disabled
// proxy that answers 503.
func New(cfg Config) (*Proxy, error) {
	if len(cfg.Secret) == 0 {
		return nil, errors.New("modelproxy: secret is required")
	}
	if cfg.Project != "" {
		if cfg.Location == "" {
			return nil, errors.New("modelproxy: location is required with a project")
		}
		if cfg.TokenSource == nil {
			return nil, errors.New("modelproxy: token source is required with a project")
		}
		if !locationPattern.MatchString(cfg.Location) {
			return nil, fmt.Errorf("modelproxy: location %q is not a GCP region", cfg.Location)
		}
		if cfg.Upstream != "" {
			if err := validUpstream(cfg.Upstream); err != nil {
				return nil, err
			}
		}
	}
	if cfg.Upstream == "" {
		cfg.Upstream = "https://" + cfg.Location + "-aiplatform.googleapis.com"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = defaultMaxBody
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = defaultMaxInFlight
	}
	return &Proxy{
		cfg:      cfg,
		client:   &http.Client{Transport: cfg.Transport, Timeout: cfg.Timeout},
		inFlight: map[string]int{},
		usage:    map[string]types.ModelUsage{},
	}, nil
}

// validUpstream requires a bare https origin, or http on a loopback host for
// local fakes, so the access token cannot be sent anywhere else.
func validUpstream(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("modelproxy: upstream %q: %w", raw, err)
	}
	loopback := false
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	} else {
		loopback = u.Hostname() == "localhost"
	}
	switch {
	case u.Host == "" || u.User != nil || strings.TrimSuffix(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "":
		return fmt.Errorf("modelproxy: upstream %q must be a scheme and host only", raw)
	case u.Scheme != "https" && !(u.Scheme == "http" && loopback):
		return fmt.Errorf("modelproxy: upstream %q must use https", raw)
	}
	return nil
}

// Enabled reports whether the proxy forwards requests.
func (p *Proxy) Enabled() bool { return p.cfg.Project != "" }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.Enabled() {
		http.Error(w, "model proxy disabled: no Vertex project configured", http.StatusServiceUnavailable)
		return
	}
	actor, ok := actorFromKey(p.cfg.Secret, p.cfg.Atespace, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !ok {
		http.Error(w, "invalid model key", http.StatusUnauthorized)
		return
	}
	if !p.allowed(r) {
		http.Error(w, "only allowlisted generateContent calls are permitted", http.StatusForbidden)
		return
	}
	if !p.acquire(actor) {
		http.Error(w, "too many model calls in flight for this actor", http.StatusTooManyRequests)
		return
	}
	defer p.release(actor)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.cfg.MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "reading request body failed", http.StatusBadRequest)
		return
	}
	tok, err := p.cfg.TokenSource.Token()
	if err != nil {
		http.Error(w, "gateway credentials unavailable", http.StatusBadGateway)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), p.cfg.Timeout)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.cfg.Upstream, "/")+strings.TrimPrefix(r.URL.Path, "/model"), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "building upstream request failed", http.StatusInternalServerError)
		return
	}
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	start := time.Now()
	resp, err := p.client.Do(out)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "Vertex AI call failed", status)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		http.Error(w, "reading Vertex AI response failed", http.StatusBadGateway)
		return
	}
	if resp.StatusCode == http.StatusOK {
		p.record(actor, respBody, time.Since(start))
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (p *Proxy) allowed(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.RawPath != "" || strings.Contains(r.URL.Path, "..") {
		return false
	}
	m := generatePath.FindStringSubmatch(r.URL.Path)
	return m != nil && m[2] == p.cfg.Project && m[3] == p.cfg.Location && slices.Contains(p.cfg.Models, m[4])
}

func (p *Proxy) acquire(actor string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[actor] >= p.cfg.MaxInFlight {
		return false
	}
	p.inFlight[actor]++
	return true
}

func (p *Proxy) release(actor string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[actor]--; p.inFlight[actor] <= 0 {
		delete(p.inFlight, actor)
	}
}

func (p *Proxy) record(actor string, body []byte, latency time.Duration) {
	var parsed struct {
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	_ = json.Unmarshal(body, &parsed)
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.usage[actor]
	u.Calls++
	u.PromptTokens += parsed.UsageMetadata.PromptTokenCount
	u.CandidateTokens += parsed.UsageMetadata.CandidatesTokenCount
	u.LastLatencyMs = latency.Milliseconds()
	u.TotalLatencyMs += u.LastLatencyMs
	p.usage[actor] = u
}

// Usage returns one actor's accumulated model usage.
func (p *Proxy) Usage(actor string) types.ModelUsage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.usage[actor]
}

// TotalUsage sums usage across all actors.
func (p *Proxy) TotalUsage() types.ModelUsage {
	p.mu.Lock()
	defer p.mu.Unlock()
	var t types.ModelUsage
	for _, u := range p.usage {
		t.Calls += u.Calls
		t.PromptTokens += u.PromptTokens
		t.CandidateTokens += u.CandidateTokens
		t.TotalLatencyMs += u.TotalLatencyMs
		t.LastLatencyMs = max(t.LastLatencyMs, u.LastLatencyMs)
	}
	return t
}

// ResetUsage clears all usage counters.
func (p *Proxy) ResetUsage() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usage = map[string]types.ModelUsage{}
}

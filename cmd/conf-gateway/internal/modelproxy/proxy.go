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

// Package modelproxy makes actors' Gemini calls to Vertex AI with the
// gateway's own credentials, so no model credential lives in an actor and
// actors need no egress.
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

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const (
	defaultTimeout     = 30 * time.Second
	defaultMaxBody     = 256 << 10
	defaultMaxInFlight = 2
	maxResponseBody    = 8 << 20
)

// locationPattern matches "global" or a GCP region such as us-central1.
// Regional locations become part of the host that receives the gateway's access
// token, while "global" targets https://aiplatform.googleapis.com.
var locationPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

var generatePath = regexp.MustCompile(`^/(v1[a-z0-9]*)/projects/([^/]+)/locations/([^/]+)/publishers/google/models/([^/:]+):generateContent$`)

// Config configures a Proxy.
type Config struct {
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

// Proxy makes the allowlisted generateContent calls actors hand to the
// gateway.
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
		if cfg.Location == "global" {
			cfg.Upstream = "https://aiplatform.googleapis.com"
		} else {
			cfg.Upstream = "https://" + cfg.Location + "-aiplatform.googleapis.com"
		}
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

// Enabled reports whether the proxy makes calls.
func (p *Proxy) Enabled() bool { return p.cfg.Project != "" }

// Call makes one actor's model call and returns Vertex AI's answer, or the
// proxy's own refusal as an error status.
func (p *Proxy) Call(ctx context.Context, actor string, call types.ModelCall) types.ModelResult {
	status, body := p.call(ctx, actor, call)
	return types.ModelResult{CallID: call.CallID, Status: status, Body: body}
}

func (p *Proxy) call(ctx context.Context, actor string, call types.ModelCall) (int, []byte) {
	if !p.Enabled() {
		return errorBody(http.StatusServiceUnavailable, "model proxy disabled: no Vertex project configured")
	}
	if !p.allowed(call.Path) {
		return errorBody(http.StatusForbidden, "only allowlisted generateContent calls are permitted")
	}
	if int64(len(call.Body)) > p.cfg.MaxBody {
		return errorBody(http.StatusRequestEntityTooLarge, "request body too large")
	}
	if !p.acquire(actor) {
		return errorBody(http.StatusTooManyRequests, "too many model calls in flight for this actor")
	}
	defer p.release(actor)

	tok, err := p.cfg.TokenSource.Token()
	if err != nil {
		return errorBody(http.StatusBadGateway, "gateway credentials unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.cfg.Upstream, "/")+call.Path, bytes.NewReader(call.Body))
	if err != nil {
		return errorBody(http.StatusInternalServerError, "building upstream request failed")
	}
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	start := time.Now()
	resp, err := p.client.Do(out)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errorBody(http.StatusGatewayTimeout, "Vertex AI call timed out")
		}
		return errorBody(http.StatusBadGateway, "Vertex AI call failed")
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return errorBody(http.StatusBadGateway, "reading Vertex AI response failed")
	}
	if resp.StatusCode == http.StatusOK {
		p.record(actor, respBody, time.Since(start))
	}
	return resp.StatusCode, respBody
}

// errorBody is a refusal in Vertex AI's error shape, so the genai SDK reports
// the message.
func errorBody(status int, msg string) (int, []byte) {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": status, "message": msg}})
	return status, b
}

// allowed accepts only a plain generateContent path for the configured
// project, location and models.
func (p *Proxy) allowed(path string) bool {
	if strings.ContainsAny(path, "%?#\\") || strings.Contains(path, "..") {
		return false
	}
	m := generatePath.FindStringSubmatch(path)
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

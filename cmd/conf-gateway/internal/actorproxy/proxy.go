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

// Package actorproxy forwards attendee requests to their actors through
// atenet-router, makes the model calls actors hand back, and binds approval
// decisions to the approval shown.
package actorproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

type attendeeContextKey struct{}
type mockContextKey struct{}

// WithAttendee returns r marked as a request from attendee, so the proxy
// routes it to that attendee's actor.
func WithAttendee(r *http.Request, attendee string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), attendeeContextKey{}, attendee))
}

// WithMock returns r marked as a simulated mock turn so the proxy never
// forwards model calls upstream for it.
func WithMock(r *http.Request, mock bool) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), mockContextKey{}, mock))
}

func attendeeOf(r *http.Request) string {
	attendeeID, _ := r.Context().Value(attendeeContextKey{}).(string)
	return attendeeID
}

func mockOf(r *http.Request) bool {
	mock, _ := r.Context().Value(mockContextKey{}).(bool)
	return mock
}

// Hooks report the outcome of each proxied attendee request. Each runs only
// for requests marked with WithAttendee.
type Hooks struct {
	// OnActorError runs when the actor answers with status 400 or above.
	OnActorError func(attendee string)
	// OnReply runs for any other actor answer, once the body is buffered so
	// the client still receives it. msg is nil when the body is not a
	// ChatMessage.
	OnReply func(attendee string, status int, msg *types.ChatMessage)
	// OnReplyWithContext is like OnReply, passing the inbound request context
	// so mock turn holds abort immediately if the caller disconnects or cancels.
	OnReplyWithContext func(ctx context.Context, attendee string, status int, msg *types.ChatMessage)
	// OnError runs when the router cannot be reached, before the 502 is
	// written.
	OnError func(attendee string, err error)
}

// maxModelCalls bounds the model calls the gateway makes for one request.
const maxModelCalls = 4

// maxStepBody bounds an actor answer the gateway buffers.
const maxStepBody = 12 << 20

// ModelCaller makes a model call an actor handed to the gateway.
type ModelCaller interface {
	Call(ctx context.Context, actor string, call types.ModelCall) types.ModelResult
}

// Proxy forwards attendee requests to atenet-router.
type Proxy struct {
	rp                *httputil.ReverseProxy
	hooks             Hooks
	model             ModelCaller
	target            *url.URL
	atespace          string
	secret            []byte
	client            *http.Client
	routerInFlight    atomic.Int64
	simRouterInFlight atomic.Int64
	*approvals
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// New returns a Proxy to the router at target. A nil target leaves the
// request URL unchanged. A nil transport uses http.DefaultTransport. A nil
// model answers every model call with 503.
func New(target *url.URL, atespace string, secret []byte, transport http.RoundTripper, model ModelCaller, hooks Hooks) *Proxy {
	baseTransport := transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	// Isolate live human attendee traffic (and postResult / ClearHistory calls)
	// onto a dedicated transport pool so simulated load bursts can never starve
	// or head-of-line block real /chat attendees.
	humanTransport := baseTransport
	if httpTr, ok := baseTransport.(*http.Transport); ok {
		cloned := httpTr.Clone()
		cloned.MaxConnsPerHost = 0
		if cloned.MaxIdleConns < 256 {
			cloned.MaxIdleConns = 256
		}
		if cloned.MaxIdleConnsPerHost < 256 {
			cloned.MaxIdleConnsPerHost = 256
		}
		humanTransport = cloned
	}

	p := &Proxy{
		hooks:     hooks,
		model:     model,
		target:    target,
		atespace:  atespace,
		secret:    secret,
		client:    &http.Client{Transport: humanTransport},
		approvals: newApprovals(),
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			if target != nil {
				pr.SetURL(target)
				pr.Out.Host = target.Host
			}
			attendeeID := attendeeOf(pr.In)
			// Gateway credentials stay here; the actor gets only its own key,
			// and routing comes only from the authenticated attendee.
			for _, h := range []string{
				"Authorization", "Cookie", auth.PresenterHeaderName,
				auth.AttendeeTokenHeader, auth.AttendeeIDHeader,
				types.ModelKeyHeader, "ate-target-actor",
			} {
				pr.Out.Header.Del(h)
			}
			if attendeeID != "" {
				pr.Out.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", atespace, attendeeID))
				pr.Out.Header.Set(types.ModelKeyHeader, ModelKey(secret, atespace, attendeeID))
			}
		},
		ModifyResponse: p.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if attendeeID := attendeeOf(req); attendeeID != "" && p.hooks.OnError != nil {
				p.hooks.OnError(attendeeID, err)
			}
			http.Error(w, fmt.Sprintf("Bad Gateway: %v", err), http.StatusBadGateway)
		},
	}
	p.rp.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		p.routerInFlight.Add(1)
		defer p.routerInFlight.Add(-1)
		if mockOf(req) {
			p.simRouterInFlight.Add(1)
			defer p.simRouterInFlight.Add(-1)
			return baseTransport.RoundTrip(req)
		}
		return roundTripWithEnvoyRetry(req, humanTransport)
	})
	return p
}

func isEnvoyResetBeforeHeaders(statusCode int, body []byte) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	s := strings.TrimSpace(string(body))
	return strings.HasPrefix(s, "upstream connect error or disconnect/reset before headers")
}

func inspectRetryable503(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable || resp.Body == nil {
		return false
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if err == nil && isEnvoyResetBeforeHeaders(resp.StatusCode, bodyBytes) {
		return true
	}
	setBody(resp, bodyBytes)
	return false
}

func sleepRetry(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func roundTripWithEnvoyRetry(req *http.Request, tr http.RoundTripper) (*http.Response, error) {
	var bodyBytes []byte
	hasBody := req.Body != nil
	if hasBody {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		bodyBytes = b
	}

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		attemptReq := req
		if attempt > 0 {
			attemptReq = req.Clone(req.Context())
		}
		if hasBody {
			attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			attemptReq.ContentLength = int64(len(bodyBytes))
		}
		resp, err := tr.RoundTrip(attemptReq)
		if err != nil {
			return nil, err
		}
		if attempt+1 < maxAttempts && inspectRetryable503(resp) {
			if !sleepRetry(req.Context(), 150*time.Millisecond) {
				return nil, req.Context().Err()
			}
			continue
		}
		return resp, nil
	}
	return nil, errors.New("exhausted router retry attempts")
}

// RouterInFlight returns the number of HTTP requests from this proxy currently
// awaiting an initial response from atenet-router (the window in which the
// router may park a request while resuming its actor). It excludes gateway
// model calls and follow-up model-result posts to already-awake actors.
func (p *Proxy) RouterInFlight() int {
	return int(max(0, p.routerInFlight.Load()))
}

// SimRouterInFlight returns the number of simulated (mock) HTTP requests from
// this proxy currently awaiting an initial response from atenet-router.
func (p *Proxy) SimRouterInFlight() int {
	return int(max(0, p.simRouterInFlight.Load()))
}

// ClearHistory asks the attendee's actor, through the router, to drop its
// conversation history and pending approval.
func (p *Proxy) ClearHistory(ctx context.Context, attendee string) error {
	if p.target == nil {
		return fmt.Errorf("no router configured")
	}
	u := p.target.JoinPath(types.ClearHistoryPath).String()
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", p.atespace, attendee))
		req.Header.Set(types.ModelKeyHeader, ModelKey(p.secret, p.atespace, attendee))
		p.routerInFlight.Add(1)
		resp, err := p.client.Do(req)
		p.routerInFlight.Add(-1)
		if err != nil {
			return err
		}
		if attempt+1 < maxAttempts && inspectRetryable503(resp) {
			if !sleepRetry(ctx, 150*time.Millisecond) {
				return ctx.Err()
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("clear history: actor answered %s", resp.Status)
		}
		return nil
	}
	return errors.New("clear history: exhausted retry attempts")
}

// ServeHTTP forwards r to the router.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.rp.ServeHTTP(w, r)
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	resp.Header.Del(types.ModelKeyHeader)
	attendeeID := attendeeOf(resp.Request)
	if attendeeID == "" {
		return nil
	}
	if err := p.relay(resp, attendeeID); err != nil {
		return err
	}
	resp.Header.Del(types.ModelKeyHeader)

	if resp.StatusCode >= 400 {
		if p.hooks.OnActorError != nil {
			p.hooks.OnActorError(attendeeID)
		}
		return nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	setBody(resp, bodyBytes)

	var msg *types.ChatMessage
	var chatMsg types.ChatMessage
	if err := json.Unmarshal(bodyBytes, &chatMsg); err == nil {
		msg = &chatMsg
	}
	if p.hooks.OnReplyWithContext != nil {
		p.hooks.OnReplyWithContext(resp.Request.Context(), attendeeID, resp.StatusCode, msg)
	} else if p.hooks.OnReply != nil {
		p.hooks.OnReply(attendeeID, resp.StatusCode, msg)
	}
	return nil
}

// relay makes each model call the actor answers with, posts the result back
// through the router, and leaves the actor's final answer in resp.
func (p *Proxy) relay(resp *http.Response, attendee string) error {
	for calls := 0; ; calls++ {
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxStepBody))
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		setBody(resp, body)
		var step types.ModelCallStep
		if json.Unmarshal(body, &step) != nil || step.ModelCall == nil {
			return nil
		}
		if calls == maxModelCalls {
			return fmt.Errorf("actor asked for more than %d model calls", maxModelCalls)
		}

		ctx := resp.Request.Context()
		res := types.ModelResult{CallID: step.ModelCall.CallID, Status: http.StatusServiceUnavailable}
		if p.model != nil && !mockOf(resp.Request) {
			res = p.model.Call(ctx, attendee, *step.ModelCall)
		}
		next, err := p.postResult(ctx, attendee, res)
		if err != nil {
			return err
		}
		resp.StatusCode, resp.Status = next.StatusCode, next.Status
		resp.Header = next.Header
		resp.Body = next.Body
	}
}

func (p *Proxy) postResult(ctx context.Context, attendee string, res types.ModelResult) (*http.Response, error) {
	if p.target == nil {
		return nil, errors.New("no router configured")
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	u := p.target.JoinPath(types.ModelResultPath).String()
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", p.atespace, attendee))
		req.Header.Set(types.ModelKeyHeader, ModelKey(p.secret, p.atespace, attendee))
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		if attempt+1 < maxAttempts && inspectRetryable503(resp) {
			if !sleepRetry(ctx, 150*time.Millisecond) {
				return nil, ctx.Err()
			}
			continue
		}
		return resp, nil
	}
	return nil, errors.New("post model result: exhausted retry attempts")
}

func setBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

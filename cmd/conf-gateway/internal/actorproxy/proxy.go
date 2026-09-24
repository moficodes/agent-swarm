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
// atenet-router and binds approval decisions to the approval shown.
package actorproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type attendeeContextKey struct{}

// WithAttendee returns r marked as a request from attendee, so the proxy
// routes it to that attendee's actor.
func WithAttendee(r *http.Request, attendee string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), attendeeContextKey{}, attendee))
}

func attendeeOf(r *http.Request) string {
	attendeeID, _ := r.Context().Value(attendeeContextKey{}).(string)
	return attendeeID
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
	// OnError runs when the router cannot be reached, before the 502 is
	// written.
	OnError func(attendee string, err error)
}

// Proxy forwards attendee requests to atenet-router.
type Proxy struct {
	rp    *httputil.ReverseProxy
	hooks Hooks
	*approvals
}

// New returns a Proxy to the router at target. A nil target leaves the
// request URL unchanged. A nil transport uses http.DefaultTransport.
func New(target *url.URL, atespace string, secret []byte, transport http.RoundTripper, hooks Hooks) *Proxy {
	p := &Proxy{
		hooks:     hooks,
		approvals: newApprovals(),
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			if target != nil {
				pr.SetURL(target)
				pr.Out.Host = target.Host
			}
			attendeeID := attendeeOf(pr.In)
			// Gateway credentials stay here; the actor gets only its own model key.
			for _, h := range []string{"Authorization", "Cookie", auth.PresenterHeaderName, types.ModelKeyHeader} {
				pr.Out.Header.Del(h)
			}
			if attendeeID != "" {
				pr.Out.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", atespace, attendeeID))
				pr.Out.Header.Set(types.ModelKeyHeader, modelproxy.Key(secret, atespace, attendeeID))
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
	if transport != nil {
		p.rp.Transport = transport
	}
	return p
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
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))
	resp.Header.Set("Content-Length", strconv.Itoa(len(bodyBytes)))

	var msg *types.ChatMessage
	var chatMsg types.ChatMessage
	if err := json.Unmarshal(bodyBytes, &chatMsg); err == nil {
		msg = &chatMsg
	}
	if p.hooks.OnReply != nil {
		p.hooks.OnReply(attendeeID, resp.StatusCode, msg)
	}
	return nil
}

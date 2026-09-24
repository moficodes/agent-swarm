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

// Package relay hands an actor's model calls to the gateway. Actors have no
// egress, so the gateway makes each Vertex AI call and posts the result back
// while the actor's turn waits.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// BaseURL is the base the genai SDK builds request URLs on. Only the path
// and body leave the actor.
const BaseURL = "http://gateway.relay/"

var (
	// ErrNoTurn means a model call was made outside Turn.Run.
	ErrNoTurn = errors.New("relay: model call outside a relayed turn")
	// ErrUnknownCall means no model call is waiting for the result's call ID.
	ErrUnknownCall = errors.New("relay: no model call is waiting for this result")
)

// Step is what a turn has next for the gateway: a model call to make, or the
// turn's final reply.
type Step struct {
	Call  *types.ModelCall
	Reply *types.ChatMessage
}

// Turn relays one chat turn's model calls, one at a time.
type Turn struct {
	steps chan Step

	mu     sync.Mutex
	callID string
	result chan types.ModelResult
}

// NewTurn returns an idle turn.
func NewTurn() *Turn {
	return &Turn{steps: make(chan Step, 1)}
}

type turnKey struct{}

// Run calls fn in the background with a context whose model calls, made
// through Transport, go to the gateway. fn's reply is the turn's last step.
func (t *Turn) Run(ctx context.Context, fn func(context.Context) *types.ChatMessage) {
	go func() {
		t.steps <- Step{Reply: fn(context.WithValue(ctx, turnKey{}, t))}
	}()
}

// Next waits for the turn's next step.
func (t *Turn) Next(ctx context.Context) (Step, error) {
	select {
	case s := <-t.steps:
		return s, nil
	case <-ctx.Done():
		return Step{}, ctx.Err()
	}
}

// Deliver answers the model call waiting under res.CallID. Each call takes
// exactly one result.
func (t *Turn) Deliver(res types.ModelResult) error {
	t.mu.Lock()
	if res.CallID == "" || res.CallID != t.callID {
		t.mu.Unlock()
		return ErrUnknownCall
	}
	ch := t.result
	t.callID, t.result = "", nil
	t.mu.Unlock()
	ch <- res
	return nil
}

// Transport is the genai SDK's transport inside a turn. Each request becomes
// a model call step, and the response is the result the gateway delivers.
type Transport struct{}

// RoundTrip hands r to the gateway and waits for its result.
func (Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx := r.Context()
	t, _ := ctx.Value(turnKey{}).(*Turn)
	if t == nil {
		return nil, ErrNoTurn
	}
	if r.Method != http.MethodPost {
		return nil, fmt.Errorf("relay: %s is not a model call", r.Method)
	}
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("relay: reading request: %w", err)
		}
		body = b
	}
	id, err := newCallID()
	if err != nil {
		return nil, err
	}

	ch := make(chan types.ModelResult, 1)
	t.mu.Lock()
	t.callID, t.result = id, ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		if t.callID == id {
			t.callID, t.result = "", nil
		}
		t.mu.Unlock()
	}()

	select {
	case t.steps <- Step{Call: &types.ModelCall{CallID: id, Path: r.URL.Path, Body: body}}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case res := <-ch:
		return response(r, res), nil
	case <-ctx.Done():
		// Withdraw the call if the gateway never collected it, so the final
		// reply has room.
		select {
		case <-t.steps:
		default:
		}
		return nil, ctx.Err()
	}
}

func response(r *http.Request, res types.ModelResult) *http.Response {
	status := res.Status
	if status == 0 {
		status = http.StatusBadGateway
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(res.Body)),
		ContentLength: int64(len(res.Body)),
		Request:       r,
	}
}

func newCallID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("relay: call ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

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

// Package localactors stands in for the atenet router in local runs and
// tests: it routes on the ate-target-actor header to one in-process actor
// per name, limits how many run at once, and reports parked requests.
package localactors

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

const targetHeader = "ate-target-actor"

var actorName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Option configures a Router.
type Option func(*Router)

// WithSlots limits how many requests run at once; the rest wait, counted
// as parked. Zero means no limit.
func WithSlots(n int) Option {
	return func(r *Router) {
		if n > 0 {
			r.slots = make(chan struct{}, n)
		}
	}
}

// Router dispatches requests to per-actor handlers.
type Router struct {
	atespace string
	newActor func(string) http.Handler
	slots    chan struct{}
	parked   atomic.Int64

	mu     sync.Mutex
	actors map[string]http.Handler
}

// New returns a Router that builds each actor's handler with newActor.
func New(atespace string, newActor func(name string) http.Handler, opts ...Option) *Router {
	r := &Router{atespace: atespace, newActor: newActor, actors: map[string]http.Handler{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/statusz" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"parking": map[string]int{"active": r.Parked()}})
		return
	}
	space, name, ok := strings.Cut(req.Header.Get(targetHeader), "/")
	if !ok || space != r.atespace || !actorName.MatchString(name) {
		http.NotFound(w, req)
		return
	}
	if r.slots != nil {
		select {
		case r.slots <- struct{}{}:
		default:
			r.parked.Add(1)
			select {
			case r.slots <- struct{}{}:
				r.parked.Add(-1)
			case <-req.Context().Done():
				r.parked.Add(-1)
				return
			}
		}
		defer func() { <-r.slots }()
	}
	r.actor(name).ServeHTTP(w, req)
}

func (r *Router) actor(name string) http.Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.actors[name]
	if !ok {
		h = r.newActor(name)
		r.actors[name] = h
	}
	return h
}

// Parked returns how many requests are waiting for a slot.
func (r *Router) Parked() int { return int(r.parked.Load()) }

// Forget drops an actor so its next request starts a fresh instance. It does
// not wait for requests already dispatched to the old instance, which keep
// running against it; call it only while the actor has none in flight.
func (r *Router) Forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.actors, name)
}

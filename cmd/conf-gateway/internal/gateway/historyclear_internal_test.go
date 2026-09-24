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

package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// clearRouter holds each history clear until open is called, answering
// with status, and logs the order in which requests reach it.
type clearRouter struct {
	status  int
	reached chan struct{}
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	order []string
}

func newClearRouter(t *testing.T, status int) (*clearRouter, *httptest.Server) {
	c := &clearRouter{status: status, reached: make(chan struct{}, 4), release: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.order = append(c.order, r.URL.Path)
		c.mu.Unlock()
		if r.URL.Path == types.ClearHistoryPath {
			c.reached <- struct{}{}
			<-c.release
			w.WriteHeader(c.status)
			return
		}
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Sender: types.SenderAssistant, Content: "ok"})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(c.open) // runs before srv.Close, which waits for held handlers
	return c, srv
}

func (c *clearRouter) open() { c.once.Do(func() { close(c.release) }) }

func (c *clearRouter) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.order)
}

func TestTurnWaitsForHistoryClear(t *testing.T) {
	router, srv := newClearRouter(t, http.StatusNoContent)
	ctrl := &recordingControl{suspended: make(chan string, 4)}
	gw := newTurnGateway(t, srv.URL, ctrl, 0)
	waiting := make(chan struct{}, 1)
	gw.waitingForHistoryClear = func(string) { waiting <- struct{}{} }

	gw.forgetAttendee("attendee-07") // returns without waiting for the actor
	<-router.reached
	_ = gw.hasSuspendTimer("attendee-07") // g.mu is free while the clear is in flight

	done := make(chan struct{})
	go func() { defer close(done); chat(gw, "attendee-07", "hi") }()
	select {
	case <-waiting:
	case <-done:
		t.Fatal("the turn finished without waiting for the clear")
	}
	if got := router.seen(); !slices.Equal(got, []string{types.ClearHistoryPath}) {
		t.Fatalf("router saw %q while the clear was in flight, want only the clear", got)
	}

	router.open()
	<-done
	if got := router.seen(); !slices.Equal(got, []string{types.ClearHistoryPath, "/api/chat"}) {
		t.Errorf("router saw %q, want the clear then the chat", got)
	}
	if got := <-ctrl.suspended; got != "attendee-07" {
		t.Errorf("suspended %q, want attendee-07", got)
	}
	gw.Close()
	if n := len(ctrl.suspended); n != 0 {
		t.Errorf("%d extra suspends, want one after the turn", n)
	}
}

func TestHistoryClearSuspendsActorItResumed(t *testing.T) {
	router, srv := newClearRouter(t, http.StatusNoContent)
	ctrl := &recordingControl{suspended: make(chan string, 4)}
	gw := newTurnGateway(t, srv.URL, ctrl, 0)

	gw.forgetAttendee("attendee-08")
	<-router.reached
	router.open()
	if got := <-ctrl.suspended; got != "attendee-08" {
		t.Errorf("suspended %q, want attendee-08", got)
	}
}

func TestFailedHistoryClearIsReportedAndTurnProceeds(t *testing.T) {
	router, srv := newClearRouter(t, http.StatusForbidden)
	router.open()
	gw := newTurnGateway(t, srv.URL, nil, 0)

	gw.forgetAttendee("attendee-09")
	chat(gw, "attendee-09", "hi") // settles the failed clear first
	if got := router.seen(); !slices.Equal(got, []string{types.ClearHistoryPath, "/api/chat"}) {
		t.Errorf("router saw %q, want one clear then the chat", got)
	}
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	if !slices.ContainsFunc(gw.recentEvents, func(ev string) bool { return strings.Contains(ev, "clear history") }) {
		t.Errorf("events %q lack the failed clear", gw.recentEvents)
	}
}

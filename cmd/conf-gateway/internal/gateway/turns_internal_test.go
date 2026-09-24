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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// recordingControl lists no actors and records every Suspend call.
type recordingControl struct{ suspended chan string }

func (c *recordingControl) List(context.Context) ([]actorstate.Status, error) { return nil, nil }

func (c *recordingControl) Suspend(_ context.Context, name string) error {
	c.suspended <- name
	return nil
}

// heldActor answers each chat once the test releases it. Every request
// announces itself on reached with the message it carries, then waits on
// that message's release channel.
type heldActor struct {
	reached chan string
	release map[string]chan struct{}
}

func newHeldActor(t *testing.T, messages ...string) (*heldActor, *httptest.Server) {
	a := &heldActor{reached: make(chan string, len(messages)), release: map[string]chan struct{}{}}
	for _, m := range messages {
		a.release[m] = make(chan struct{})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req types.ChatRequest
		_ = json.Unmarshal(body, &req)
		a.reached <- req.Message
		<-a.release[req.Message]
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Sender: types.SenderAssistant, Content: "ok"})
	}))
	t.Cleanup(srv.Close)
	return a, srv
}

func newTurnGateway(t *testing.T, actorURL string, ctrl actorstate.ActorControl, suspendAfter time.Duration) *Gateway {
	gw := NewGateway(actorURL, WithActorControl(ctrl), WithSuspendAfter(suspendAfter))
	t.Cleanup(gw.Close)
	return gw
}

// chat sends one chat turn as attendee and returns once the handler, and
// with it the turn's bookkeeping, has finished.
func chat(gw *Gateway, attendee, message string) {
	body, _ := json.Marshal(types.ChatRequest{Message: message})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gw.MintToken(attendee))
	gw.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

func (g *Gateway) hasSuspendTimer(attendee string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.suspendTimers[attendee]
	return ok
}

func TestTurnInFlightCancelsPendingSuspend(t *testing.T) {
	actor, srv := newHeldActor(t, "one", "two")
	ctrl := &recordingControl{suspended: make(chan string, 4)}
	gw := newTurnGateway(t, srv.URL, ctrl, time.Hour)

	close(actor.release["one"])
	chat(gw, "attendee-01", "one")
	if !gw.hasSuspendTimer("attendee-01") {
		t.Fatal("no suspend scheduled after the first turn")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		chat(gw, "attendee-01", "two")
	}()
	<-actor.reached // "one"
	<-actor.reached // "two" is now at the actor
	if gw.hasSuspendTimer("attendee-01") {
		t.Error("suspend still scheduled while the second turn is in flight")
	}

	close(actor.release["two"])
	<-done
	if !gw.hasSuspendTimer("attendee-01") {
		t.Error("suspend not re-armed after the second turn")
	}
	gw.Close()
	if n := len(ctrl.suspended); n != 0 {
		t.Errorf("%d suspends ran, want none before SuspendAfter", n)
	}
}

func TestConcurrentTurnsSuspendOnceAfterLast(t *testing.T) {
	actor, srv := newHeldActor(t, "a", "b")
	ctrl := &recordingControl{suspended: make(chan string, 4)}
	gw := newTurnGateway(t, srv.URL, ctrl, 0)

	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() { defer close(doneA); chat(gw, "attendee-02", "a") }()
	go func() { defer close(doneB); chat(gw, "attendee-02", "b") }()
	<-actor.reached
	<-actor.reached // both turns are at the actor

	close(actor.release["a"])
	<-doneA
	gw.mu.Lock()
	inFlight := gw.turnsInFlight["attendee-02"]
	gw.mu.Unlock()
	if inFlight != 1 {
		t.Errorf("turns in flight = %d after the first completed, want 1", inFlight)
	}
	select {
	case name := <-ctrl.suspended:
		t.Fatalf("suspended %q while a turn was still in flight", name)
	default:
	}

	close(actor.release["b"])
	<-doneB
	if got := <-ctrl.suspended; got != "attendee-02" {
		t.Errorf("suspended %q, want attendee-02", got)
	}
	gw.Close() // waits for every scheduled suspend
	if n := len(ctrl.suspended); n != 0 {
		t.Errorf("%d extra suspends, want exactly one after the last turn", n)
	}
}

func TestFiredSuspendTimerRemovesItself(t *testing.T) {
	actor, srv := newHeldActor(t, "one")
	close(actor.release["one"])
	ctrl := &recordingControl{suspended: make(chan string, 4)}
	gw := newTurnGateway(t, srv.URL, ctrl, time.Millisecond)

	chat(gw, "attendee-03", "one")
	<-ctrl.suspended // the timer callback removes its entry before suspending
	if gw.hasSuspendTimer("attendee-03") {
		t.Error("fired suspend timer is still tracked")
	}
}

// blockingControl holds every Suspend until its context ends.
type blockingControl struct{ started chan struct{} }

func (c *blockingControl) List(context.Context) ([]actorstate.Status, error) { return nil, nil }

func (c *blockingControl) Suspend(ctx context.Context, _ string) error {
	c.started <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func TestSuspendCanceledByCloseIsNotReported(t *testing.T) {
	actor, srv := newHeldActor(t, "one")
	close(actor.release["one"])
	ctrl := &blockingControl{started: make(chan struct{}, 1)}
	gw := newTurnGateway(t, srv.URL, ctrl, 0)

	chat(gw, "attendee-04", "one")
	<-ctrl.started
	gw.Close()
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, ev := range gw.recentEvents {
		if strings.Contains(ev, "suspend actor") {
			t.Errorf("suspend canceled by Close was reported: %q", ev)
		}
	}
}

// holdSuspendCheck makes each scheduled suspend announce itself on the
// returned channel and wait for release to close before it re-checks for
// new turns.
func holdSuspendCheck(gw *Gateway) (reached chan string, release chan struct{}) {
	reached, release = make(chan string, 4), make(chan struct{})
	gw.beforeSuspendCheck = func(attendee string) {
		reached <- attendee
		<-release
	}
	return reached, release
}

func TestScheduledSuspendSkippedWhenTurnInFlight(t *testing.T) {
	for _, tc := range []struct {
		name         string
		suspendAfter time.Duration
	}{
		{"immediate", 0},
		{"fired timer", time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, srv := newHeldActor(t, "one", "two")
			ctrl := &recordingControl{suspended: make(chan string, 4)}
			gw := newTurnGateway(t, srv.URL, ctrl, tc.suspendAfter)
			reached, release := holdSuspendCheck(gw)

			close(actor.release["one"])
			chat(gw, "attendee-05", "one")
			<-reached // the suspend is scheduled and past any timer

			done := make(chan struct{})
			go func() { defer close(done); chat(gw, "attendee-05", "two") }()
			<-actor.reached // "one"
			<-actor.reached // "two" is in flight

			close(release)
			gw.Close() // waits for the scheduled suspend
			if n := len(ctrl.suspended); n != 0 {
				t.Errorf("%d suspends ran while a turn was in flight, want none", n)
			}
			close(actor.release["two"])
			<-done
		})
	}
}

func TestScheduledSuspendSkippedAfterNewerTurn(t *testing.T) {
	actor, srv := newHeldActor(t, "one", "two")
	ctrl := &recordingControl{suspended: make(chan string, 4)}
	gw := newTurnGateway(t, srv.URL, ctrl, 0)
	reached, release := holdSuspendCheck(gw)

	close(actor.release["one"])
	close(actor.release["two"])
	chat(gw, "attendee-06", "one")
	<-reached
	chat(gw, "attendee-06", "two") // starts and ends inside the window
	<-reached

	close(release)
	gw.Close()
	if n := len(ctrl.suspended); n != 1 {
		t.Errorf("%d suspends ran, want only the newer turn's", n)
	}
}

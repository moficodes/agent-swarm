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

package localactors_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/internal/localactors"
)

func echoActor(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, name) })
}

func send(h http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	if target != "" {
		req.Header.Set("ate-target-actor", target)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouter_DispatchesByTargetHeader(t *testing.T) {
	r := localactors.New("ate-demo-conf", echoActor)
	if got := send(r, "ate-demo-conf/attendee-03").Body.String(); got != "attendee-03" {
		t.Errorf("body = %q", got)
	}
}

func TestRouter_ReusesActorInstance(t *testing.T) {
	var mu sync.Mutex
	created := map[string]int{}
	r := localactors.New("ate-demo-conf", func(name string) http.Handler {
		mu.Lock()
		created[name]++
		mu.Unlock()
		return echoActor(name)
	})
	send(r, "ate-demo-conf/attendee-01")
	send(r, "ate-demo-conf/attendee-01")
	if created["attendee-01"] != 1 {
		t.Errorf("created %d instances, want 1", created["attendee-01"])
	}
	r.Forget("attendee-01")
	send(r, "ate-demo-conf/attendee-01")
	if created["attendee-01"] != 2 {
		t.Errorf("after Forget created %d instances, want 2", created["attendee-01"])
	}
}

func TestRouter_RejectsBadTargets(t *testing.T) {
	r := localactors.New("ate-demo-conf", echoActor)
	for _, target := range []string{"", "attendee-01", "other/attendee-01", "ate-demo-conf/", "ate-demo-conf/../x", "ate-demo-conf/A"} {
		if rec := send(r, target); rec.Code != http.StatusNotFound {
			t.Errorf("target %q: status %d, want 404", target, rec.Code)
		}
	}
}

func TestRouter_ParksBeyondSlotsAndReportsStatusz(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	r := localactors.New("ate-demo-conf", func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			entered <- struct{}{}
			<-release
		})
	}, localactors.WithSlots(1))

	done := make(chan struct{})
	go func() { send(r, "ate-demo-conf/attendee-01"); done <- struct{}{} }()
	<-entered
	go func() { send(r, "ate-demo-conf/attendee-02"); done <- struct{}{} }()

	// Parked() becomes 1 once the second request is waiting for a slot.
	for r.Parked() != 1 {
		runtime.Gosched()
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statusz?format=json", nil))
	var sz struct{ Parking struct{ Active int } }
	_ = json.Unmarshal(rec.Body.Bytes(), &sz)
	if sz.Parking.Active != 1 {
		t.Errorf("statusz parking.active = %d, want 1", sz.Parking.Active)
	}

	close(release) // finish attendee-01; attendee-02 takes the slot
	<-entered
	<-done
	<-done
	if r.Parked() != 0 {
		t.Errorf("Parked() = %d after drain", r.Parked())
	}
}

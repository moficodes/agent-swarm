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

package simulation

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type fakeTarget struct {
	ctx   context.Context
	chats chan string
}

func (f *fakeTarget) Context() context.Context         { return f.ctx }
func (f *fakeTarget) MintToken(attendee string) string { return "tok-" + attendee }
func (f *fakeTarget) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-"+r.Header.Get("X-Attendee-ID") {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		select {
		case f.chats <- r.Header.Get("X-Attendee-ID"):
		default:
		}
		_ = json.NewEncoder(w).Encode(types.ChatMessage{ID: "m", Sender: types.SenderAssistant, Content: "ok"})
	})
}

func TestRunner_StartDrivesTargetAndStop(t *testing.T) {
	f := &fakeTarget{ctx: context.Background(), chats: make(chan string, 16)}
	r := NewRunner(f, WithSimulationDelays(time.Millisecond, time.Millisecond))
	if err := r.Start(2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if running, count := r.Status(); !running || count != 2 {
		t.Fatalf("Status = %v, %d; want true, 2", running, count)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		seen[<-f.chats] = true
	}
	if !seen["attendee-01"] || !seen["attendee-02"] {
		t.Fatalf("chats came from %v, want attendee-01 and attendee-02", seen)
	}
	r.Stop()
	if running, count := r.Status(); running || count != 0 {
		t.Fatalf("Status after Stop = %v, %d; want false, 0", running, count)
	}
}

func TestRunner_StartRejectsBadCountAndClosedTarget(t *testing.T) {
	f := &fakeTarget{ctx: context.Background(), chats: make(chan string, 1)}
	if err := NewRunner(f).Start(0); err == nil {
		t.Error("Start(0) succeeded")
	}
	if err := NewRunner(f).Start(101); err == nil {
		t.Error("Start(101) succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewRunner(&fakeTarget{ctx: ctx}).Start(1); err == nil {
		t.Error("Start succeeded on a closed target")
	}
}

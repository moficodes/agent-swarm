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

package feed

import (
	"fmt"
	"testing"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

func TestLog_RecordMergesByIDAndCaps(t *testing.T) {
	l := NewLog(3)
	l.Record(types.ActionLogEntry{ID: "a", Prompt: "hi", Activity: types.ActivityThinking})
	got := l.Record(types.ActionLogEntry{ID: "a", ModelResponse: "hello", Activity: types.ActivityIdle})
	if got.Prompt != "hi" || got.ModelResponse != "hello" || got.Activity != types.ActivityIdle {
		t.Fatalf("merged entry = %+v", got)
	}
	if h := l.History(); len(h) != 1 {
		t.Fatalf("History has %d entries after a merge, want 1", len(h))
	}

	for i := range 4 {
		l.Record(types.ActionLogEntry{ID: fmt.Sprintf("n%d", i)})
	}
	h := l.History()
	if len(h) != 3 || h[0].ID != "n1" || h[2].ID != "n3" {
		t.Fatalf("History = %+v, want the newest 3 entries n1..n3", h)
	}

	l.Reset()
	if h := l.History(); len(h) != 0 {
		t.Fatalf("History after Reset = %+v, want empty", h)
	}
}

func TestLog_RecordFillsIDAndTimestamp(t *testing.T) {
	got := NewLog(10).Record(types.ActionLogEntry{})
	if got.ID == "" || got.Timestamp == 0 {
		t.Fatalf("Record left ID or Timestamp empty: %+v", got)
	}
}

func TestBroker_SendToAttendeeAndBroadcast(t *testing.T) {
	b := NewBroker()
	a := b.Subscribe("attendee-01")
	other := b.Subscribe("attendee-02")
	reset := b.SubscribeReset()
	defer b.Unsubscribe("attendee-01", a)
	defer b.Unsubscribe("attendee-02", other)
	defer b.UnsubscribeReset(reset)

	b.SendToAttendee("attendee-01", "actor_view", map[string]string{"k": "v"})
	if e := <-a; e.Name != "actor_view" || string(e.Data) != `{"k":"v"}` {
		t.Fatalf("attendee-01 got %q %s", e.Name, e.Data)
	}
	select {
	case e := <-other:
		t.Fatalf("attendee-02 got another attendee's event %q", e.Name)
	default:
	}

	b.Broadcast("reset", struct{}{})
	for _, ch := range []chan Event{a, other} {
		if e := <-ch; e.Name != "reset" {
			t.Fatalf("broadcast delivered %q, want reset", e.Name)
		}
	}
	<-reset
}

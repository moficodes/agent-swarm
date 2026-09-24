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

package relay_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/agent-swarm/internal/workload/relay"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// modelTurn runs a turn that makes one model call and replies with the
// status and body it got back.
func modelTurn(t *testing.T, turn *relay.Turn, ctx context.Context) {
	t.Helper()
	turn.Run(ctx, func(ctx context.Context) *types.ChatMessage {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, relay.BaseURL+"v1beta1/models/m:generateContent", strings.NewReader(`{"q":1}`))
		resp, err := relay.Transport{}.RoundTrip(req)
		if err != nil {
			return &types.ChatMessage{Content: "error: " + err.Error()}
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return &types.ChatMessage{Content: resp.Status + " " + string(b)}
	})
}

func next(t *testing.T, turn *relay.Turn) relay.Step {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := turn.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return s
}

func TestTurn_ModelCallRoundTrip(t *testing.T) {
	turn := relay.NewTurn()
	modelTurn(t, turn, context.Background())

	step := next(t, turn)
	if step.Call == nil || step.Reply != nil {
		t.Fatalf("first step = %+v, want a model call", step)
	}
	if step.Call.Path != "/v1beta1/models/m:generateContent" || string(step.Call.Body) != `{"q":1}` {
		t.Errorf("call = %s %s", step.Call.Path, step.Call.Body)
	}
	if len(step.Call.CallID) < 32 {
		t.Errorf("CallID = %q, want 128 random bits", step.Call.CallID)
	}

	if err := turn.Deliver(types.ModelResult{CallID: step.Call.CallID, Status: 200, Body: []byte("answer")}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	step = next(t, turn)
	if step.Reply == nil || step.Reply.Content != "200 OK answer" {
		t.Fatalf("final step = %+v, want the reply built from the result", step)
	}
}

func TestTurn_CallIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		turn := relay.NewTurn()
		modelTurn(t, turn, context.Background())
		id := next(t, turn).Call.CallID
		if seen[id] {
			t.Fatalf("CallID %q repeated", id)
		}
		seen[id] = true
		_ = turn.Deliver(types.ModelResult{CallID: id, Status: 200})
		next(t, turn)
	}
}

func TestTurn_RejectsUnknownAndReplayedCallIDs(t *testing.T) {
	turn := relay.NewTurn()
	modelTurn(t, turn, context.Background())
	id := next(t, turn).Call.CallID

	for _, bad := range []string{"", "forged", id + "x"} {
		if err := turn.Deliver(types.ModelResult{CallID: bad, Status: 200}); !errors.Is(err, relay.ErrUnknownCall) {
			t.Errorf("Deliver(%q) = %v, want ErrUnknownCall", bad, err)
		}
	}
	if err := turn.Deliver(types.ModelResult{CallID: id, Status: 200}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := turn.Deliver(types.ModelResult{CallID: id, Status: 200}); !errors.Is(err, relay.ErrUnknownCall) {
		t.Errorf("replayed Deliver = %v, want ErrUnknownCall", err)
	}
	next(t, turn)
}

func TestTurn_MissingStatusIsBadGateway(t *testing.T) {
	turn := relay.NewTurn()
	modelTurn(t, turn, context.Background())
	_ = turn.Deliver(types.ModelResult{CallID: next(t, turn).Call.CallID})
	if got := next(t, turn).Reply.Content; !strings.HasPrefix(got, "502 ") {
		t.Errorf("reply = %q, want a 502 response", got)
	}
}

func TestTurn_UnansweredCallTimesOutAndTurnStillFinishes(t *testing.T) {
	turn := relay.NewTurn()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	modelTurn(t, turn, ctx)

	// The gateway never collects the call; the turn must still end.
	time.Sleep(100 * time.Millisecond)
	step := next(t, turn)
	if step.Reply == nil || !strings.Contains(step.Reply.Content, "deadline exceeded") {
		t.Fatalf("step = %+v, want the final reply carrying the timeout", step)
	}
}

func TestTurn_CollectedButUnansweredCallTimesOut(t *testing.T) {
	turn := relay.NewTurn()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	modelTurn(t, turn, ctx)

	id := next(t, turn).Call.CallID
	step := next(t, turn)
	if step.Reply == nil || !strings.Contains(step.Reply.Content, "deadline exceeded") {
		t.Fatalf("step = %+v, want the final reply carrying the timeout", step)
	}
	if err := turn.Deliver(types.ModelResult{CallID: id, Status: 200}); !errors.Is(err, relay.ErrUnknownCall) {
		t.Errorf("late Deliver = %v, want ErrUnknownCall", err)
	}
}

func TestTurn_NextHonorsItsContext(t *testing.T) {
	turn := relay.NewTurn()
	block := make(chan struct{})
	defer close(block)
	turn.Run(context.Background(), func(context.Context) *types.ChatMessage {
		<-block
		return &types.ChatMessage{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := turn.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Next = %v, want DeadlineExceeded", err)
	}
}

func TestTransport_OutsideATurnFails(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, relay.BaseURL+"v1/x", nil)
	if _, err := (relay.Transport{}).RoundTrip(req); !errors.Is(err, relay.ErrNoTurn) {
		t.Errorf("RoundTrip = %v, want ErrNoTurn", err)
	}
}

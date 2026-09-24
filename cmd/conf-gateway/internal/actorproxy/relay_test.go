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

package actorproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// fakeModel answers every call with status 200 and "r-<call ID>".
type fakeModel struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeModel) Call(_ context.Context, actor string, call types.ModelCall) types.ModelResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, actor+" "+call.CallID+" "+call.Path)
	return types.ModelResult{CallID: call.CallID, Status: http.StatusOK, Body: []byte("r-" + call.CallID)}
}

type resultPost struct {
	target, key string
	res         types.ModelResult
}

// relayRouter plays an actor whose turn makes the given model calls in order
// and then replies. Once calls run out it keeps asking if forever is set.
func relayRouter(t *testing.T, callIDs []string, forever bool, results chan<- resultPost) *url.URL {
	t.Helper()
	next := 0
	step := func(w http.ResponseWriter) {
		if next < len(callIDs) || forever {
			id := "c" + string(rune('0'+next%10))
			if next < len(callIDs) {
				id = callIDs[next]
			}
			next++
			_ = json.NewEncoder(w).Encode(types.ModelCallStep{ModelCall: &types.ModelCall{CallID: id, Path: "/v1/p/" + id, Body: []byte("{}")}})
			return
		}
		w.Header().Set(types.ModelKeyHeader, "leaked")
		_ = json.NewEncoder(w).Encode(types.ChatMessage{ID: "final", Content: "done"})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			step(w)
		case types.ModelResultPath:
			var res types.ModelResult
			if err := json.NewDecoder(r.Body).Decode(&res); err != nil || r.Method != http.MethodPost {
				http.Error(w, "bad result", http.StatusBadRequest)
				return
			}
			if results != nil {
				results <- resultPost{r.Header.Get("ate-target-actor"), r.Header.Get(types.ModelKeyHeader), res}
			}
			step(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u
}

func chat(p *Proxy) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, WithAttendee(req, "attendee-01"))
	return rec
}

func TestProxy_RelaysModelCallsUntilTheReply(t *testing.T) {
	results := make(chan resultPost, 8)
	model := &fakeModel{}
	var reply *types.ChatMessage
	p := New(relayRouter(t, []string{"a", "b"}, false, results), "space", []byte("secret"), nil, model, Hooks{
		OnReply: func(_ string, _ int, msg *types.ChatMessage) { reply = msg },
	})

	rec := chat(p)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"final"`) || strings.Contains(rec.Body.String(), "model_call") {
		t.Fatalf("client got %d %q, want only the final reply", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(types.ModelKeyHeader) != "" {
		t.Error("model key header reached the client")
	}
	if reply == nil || reply.ID != "final" {
		t.Errorf("OnReply got %+v, want the final reply", reply)
	}
	if want := []string{"attendee-01 a /v1/p/a", "attendee-01 b /v1/p/b"}; strings.Join(model.calls, ",") != strings.Join(want, ",") {
		t.Errorf("model calls = %q, want %q", model.calls, want)
	}
	close(results)
	var ids []string
	for r := range results {
		if r.target != "space/attendee-01" || r.key != ModelKey([]byte("secret"), "space", "attendee-01") {
			t.Errorf("result post target %q key %q", r.target, r.key)
		}
		if r.res.Status != http.StatusOK || string(r.res.Body) != "r-"+r.res.CallID {
			t.Errorf("posted result = %+v", r.res)
		}
		ids = append(ids, r.res.CallID)
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("posted results for %q, want a,b", ids)
	}
}

func TestProxy_CapsModelCallsPerTurn(t *testing.T) {
	model := &fakeModel{}
	var routerErr error
	p := New(relayRouter(t, nil, true, nil), "space", []byte("secret"), nil, model, Hooks{
		OnError: func(_ string, err error) { routerErr = err },
	})

	rec := chat(p)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if len(model.calls) != maxModelCalls {
		t.Errorf("model calls = %d, want %d", len(model.calls), maxModelCalls)
	}
	if routerErr == nil {
		t.Error("OnError did not run")
	}
}

func TestProxy_WithoutModelCallerRelaysUnavailable(t *testing.T) {
	results := make(chan resultPost, 1)
	p := New(relayRouter(t, []string{"a"}, false, results), "space", []byte("secret"), nil, nil, Hooks{})

	if rec := chat(p); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the actor's reply", rec.Code)
	}
	if r := <-results; r.res.CallID != "a" || r.res.Status != http.StatusServiceUnavailable {
		t.Errorf("posted result = %+v, want a 503 for call a", r.res)
	}
}

func TestProxy_ActorRefusingTheResultIsAnActorError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == types.ModelResultPath {
			http.Error(w, "no such call", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(types.ModelCallStep{ModelCall: &types.ModelCall{CallID: "a", Path: "/v1/x"}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	actorErrs := 0
	p := New(u, "space", []byte("secret"), nil, &fakeModel{}, Hooks{
		OnActorError: func(string) { actorErrs++ },
		OnReply:      func(string, int, *types.ChatMessage) { t.Error("OnReply ran for a refused result") },
	})

	rec := chat(p)

	if rec.Code != http.StatusConflict || actorErrs != 1 {
		t.Errorf("status = %d, actor errors = %d, want 409 and 1", rec.Code, actorErrs)
	}
}

func TestProxy_NonAttendeeResponsesAreNotRelayed(t *testing.T) {
	model := &fakeModel{}
	p := New(relayRouter(t, []string{"a"}, false, nil), "space", []byte("secret"), nil, model, Hooks{})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat", nil))

	if len(model.calls) != 0 || !strings.Contains(rec.Body.String(), "model_call") {
		t.Errorf("model calls = %d, body %q; want the step passed through untouched", len(model.calls), rec.Body.String())
	}
}

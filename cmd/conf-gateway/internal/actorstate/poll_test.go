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

package actorstate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// scriptedControl returns queued results from List, one per call.
type scriptedControl struct {
	results chan listResult
}

type listResult struct {
	statuses []actorstate.Status
	err      error
}

func (s *scriptedControl) List(ctx context.Context) ([]actorstate.Status, error) {
	select {
	case r := <-s.results:
		return r.statuses, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *scriptedControl) Suspend(context.Context, string) error { return nil }

func TestPoll_UpdatesBoardAndReportsErrors(t *testing.T) {
	ctrl := &scriptedControl{results: make(chan listResult)}
	board := actorstate.NewBoard(func(types.ActorView) {})
	observed := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		actorstate.Poll(ctx, ctrl, time.Millisecond, board, func(_ time.Duration, err error) { observed <- err })
		close(done)
	}()

	ctrl.results <- listResult{statuses: []actorstate.Status{{Name: "a", State: types.StateRunning}}}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	if v, _ := board.View("a"); v.State != types.StateRunning {
		t.Errorf("after good poll: %+v", v)
	}

	ctrl.results <- listResult{err: errors.New("ateapi unreachable")}
	if err := <-observed; err == nil {
		t.Fatal("observe got nil error")
	}
	if v, _ := board.View("a"); v.State != types.StateUnavailable {
		t.Errorf("after failed poll: %+v", v)
	}

	cancel()
	<-done
}

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
	"os"
	"slices"
	"testing"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

func TestParseActorList_Fixture(t *testing.T) {
	data, err := os.ReadFile("testdata/actors.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := actorstate.ParseActorList(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []actorstate.Status{
		{Name: "attendee-01", State: types.StateSuspended},
		{Name: "attendee-02", State: types.StateRunning, WorkerPod: "conf-pool-7d9f-x2k4p"},
		{Name: "attendee-03", State: types.StateSuspended, SnapshotURI: "gs://conf-snapshots/ate-demo-conf/attendee-03/9"},
		{Name: "attendee-04", State: types.StateCrashed},
		{Name: "attendee-05", State: types.StateUnknown},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParseActorList =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseActorList_Empty(t *testing.T) {
	got, err := actorstate.ParseActorList([]byte(`{}`))
	if err != nil || len(got) != 0 {
		t.Errorf("ParseActorList({}) = %v, %v", got, err)
	}
}

func TestParseActorList_RejectsGarbage(t *testing.T) {
	if _, err := actorstate.ParseActorList([]byte("Error: unauthorized")); err == nil {
		t.Error("ParseActorList accepted non-JSON")
	}
}

type recordedCall struct {
	name string
	args []string
}

func recorder(out []byte, err error) (actorstate.Runner, *[]recordedCall) {
	var calls []recordedCall
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, recordedCall{name, args})
		return out, err
	}, &calls
}

func TestKubectlATE_ListArgs(t *testing.T) {
	run, calls := recorder([]byte(`{"actors":[]}`), nil)
	k := actorstate.NewKubectlATEWithRunner("/bin/kubectl-ate", "api.ate-system.svc:443", "ate-demo-conf", run)
	if _, err := k.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"--endpoint", "api.ate-system.svc:443", "get", "actors", "-a", "ate-demo-conf", "-o", "json"}
	if c := (*calls)[0]; c.name != "/bin/kubectl-ate" || !slices.Equal(c.args, want) {
		t.Errorf("call = %s %v, want %v", c.name, c.args, want)
	}
}

func TestKubectlATE_NoEndpointFlagWhenEmpty(t *testing.T) {
	run, calls := recorder(nil, nil)
	k := actorstate.NewKubectlATEWithRunner("kubectl-ate", "", "ate-demo-conf", run)
	if err := k.Suspend(context.Background(), "attendee-02"); err != nil {
		t.Fatal(err)
	}
	want := []string{"suspend", "actor", "attendee-02", "-a", "ate-demo-conf"}
	if got := (*calls)[0].args; !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestKubectlATE_SuspendRejectsBadName(t *testing.T) {
	run, calls := recorder(nil, nil)
	k := actorstate.NewKubectlATEWithRunner("kubectl-ate", "", "ate-demo-conf", run)
	if err := k.Suspend(context.Background(), "--endpoint=evil"); err == nil {
		t.Error("Suspend accepted a flag-shaped name")
	}
	if len(*calls) != 0 {
		t.Errorf("runner called %d times", len(*calls))
	}
}

func TestKubectlATE_ListPropagatesErrors(t *testing.T) {
	run, _ := recorder([]byte("boom"), errors.New("exit status 1"))
	k := actorstate.NewKubectlATEWithRunner("kubectl-ate", "", "ate-demo-conf", run)
	if _, err := k.List(context.Background()); err == nil {
		t.Error("List returned nil error")
	}
}

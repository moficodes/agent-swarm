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

package actorstate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

var actorName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs the command with os/exec; stderr is folded into the error.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// KubectlATE implements ActorControl with the kubectl-ate CLI.
type KubectlATE struct {
	bin, endpoint, atespace string
	run                     Runner
}

// NewKubectlATE returns an ActorControl that runs bin. An empty endpoint
// lets kubectl-ate use its own default.
func NewKubectlATE(bin, endpoint, atespace string) *KubectlATE {
	return NewKubectlATEWithRunner(bin, endpoint, atespace, ExecRunner)
}

// NewKubectlATEWithRunner is NewKubectlATE with a custom Runner, for tests.
func NewKubectlATEWithRunner(bin, endpoint, atespace string, run Runner) *KubectlATE {
	return &KubectlATE{bin: bin, endpoint: endpoint, atespace: atespace, run: run}
}

func (k *KubectlATE) args(rest ...string) []string {
	if k.endpoint == "" {
		return rest
	}
	return append([]string{"--endpoint", k.endpoint}, rest...)
}

// List returns every actor in the atespace.
func (k *KubectlATE) List(ctx context.Context) ([]Status, error) {
	out, err := k.run(ctx, k.bin, k.args("get", "actors", "-a", k.atespace, "-o", "json")...)
	if err != nil {
		return nil, err
	}
	return ParseActorList(out)
}

// Suspend asks ateapi to suspend one actor.
func (k *KubectlATE) Suspend(ctx context.Context, name string) error {
	if !actorName.MatchString(name) {
		return fmt.Errorf("invalid actor name %q", name)
	}
	_, err := k.run(ctx, k.bin, k.args("suspend", "actor", name, "-a", k.atespace)...)
	return err
}

// Revert asks ateapi to roll one actor back to its last good snapshot.
func (k *KubectlATE) Revert(ctx context.Context, name string) error {
	if !actorName.MatchString(name) {
		return fmt.Errorf("invalid actor name %q", name)
	}
	_, err := k.run(ctx, k.bin, k.args("revert", "actor", name, "-a", k.atespace)...)
	return err
}

// ParseActorList decodes `kubectl-ate get actors -o json` output.
func ParseActorList(data []byte) ([]Status, error) {
	type snapshot struct {
		Generation      int32 `json:"generation"`
		DurableSnapshot *struct {
			State  string `json:"state"`
			Object struct {
				SnapshotURI string `json:"snapshotUri"`
			} `json:"object"`
		} `json:"durableSnapshot"`
	}
	var list struct {
		Actors []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				State            string `json:"state"`
				WorkerAssignment struct {
					WorkerPod string `json:"workerPod"`
				} `json:"workerAssignment"`
				Snapshots []snapshot `json:"snapshots"`
			} `json:"status"`
		} `json:"actors"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decoding actor list: %w", err)
	}
	out := make([]Status, 0, len(list.Actors))
	for _, a := range list.Actors {
		state := types.SubstrateState(strings.TrimPrefix(a.Status.State, "ACTOR_STATE_"))
		if state == "" || state == "UNSPECIFIED" {
			state = types.StateUnknown
		}
		var (
			snapshotURI   string
			snapshotGen   int32 = -1
			inProgressURI string
			inProgressGen int32 = -1
		)
		for _, snap := range a.Status.Snapshots {
			if snap.DurableSnapshot == nil {
				continue
			}
			uri := snap.DurableSnapshot.Object.SnapshotURI
			switch snap.DurableSnapshot.State {
			case "SNAPSHOT_STORAGE_STATE_COMPLETED":
				if snap.Generation >= snapshotGen {
					snapshotGen = snap.Generation
					snapshotURI = uri
				}
			case "SNAPSHOT_STORAGE_STATE_PENDING":
				if snap.Generation >= inProgressGen {
					inProgressGen = snap.Generation
					inProgressURI = uri
				}
			}
		}
		out = append(out, Status{
			Name:                  a.Metadata.Name,
			State:                 state,
			WorkerPod:             a.Status.WorkerAssignment.WorkerPod,
			InProgressSnapshotURI: inProgressURI,
			SnapshotURI:           snapshotURI,
		})
	}
	return out, nil
}

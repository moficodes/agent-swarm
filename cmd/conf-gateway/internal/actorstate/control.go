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

// Package actorstate reads actor state from ateapi through kubectl-ate and
// keeps the per-actor views the UI renders.
package actorstate

import (
	"context"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// Status is one actor as ateapi reports it.
type Status struct {
	Name                  string
	State                 types.SubstrateState
	WorkerPod             string
	InProgressSnapshotURI string
	SnapshotURI           string
	VolumeType            string
	VolumeShare           string
	VolumeURI             string
}

// ActorControl lists, suspends and reverts the demo's actors.
type ActorControl interface {
	List(ctx context.Context) ([]Status, error)
	Suspend(ctx context.Context, name string) error
	// Revert rolls a CRASHED actor back to its last good snapshot, leaving
	// it SUSPENDED. Anything since that snapshot is lost.
	Revert(ctx context.Context, name string) error
}

// ActiveSeatLister is an optional extension implemented by gRPC-backed
// ActorControl clients to list only actors currently occupying worker seats
// (via ListWorkers + ListWorkerActorAssignments) instead of paging through
// millions of idle suspended actors on every poll.
type ActiveSeatLister interface {
	ListActiveSeats(ctx context.Context) ([]Status, error)
}

// SingleActorGetter is an optional extension implemented by gRPC-backed
// ActorControl clients to fetch one actor's full status on demand.
type SingleActorGetter interface {
	Get(ctx context.Context, name string) (Status, error)
}

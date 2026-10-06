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
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

func (g *Gateway) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var pods types.WorkerPods
	if p := g.workerPods.Load(); p != nil {
		pods = *p
	}

	views := g.board.Views()
	names := slices.Sorted(maps.Keys(views))
	var workers []types.WorkerStatus
	podIndex := make(map[string]int)
	for _, podName := range pods.PodNames {
		if podName == "" {
			continue
		}
		if _, ok := podIndex[podName]; !ok {
			podIndex[podName] = len(workers)
			workers = append(workers, types.WorkerStatus{PodName: podName})
		}
	}
	for _, name := range names {
		v := views[name]
		if v.WorkerPod == "" {
			continue
		}
		i, ok := podIndex[v.WorkerPod]
		if !ok {
			i = len(workers)
			podIndex[v.WorkerPod] = i
			workers = append(workers, types.WorkerStatus{PodName: v.WorkerPod})
		}
		workers[i].Actors = append(workers[i].Actors, types.WorkerActor{ActorID: v.ActorID, State: v.State})
	}
	slices.SortFunc(workers, func(a, b types.WorkerStatus) int { return strings.Compare(a.PodName, b.PodName) })

	g.mu.RLock()
	eventsCopy := slices.Clone(g.recentEvents)
	g.mu.RUnlock()

	var parked int
	var parkingOK bool
	if snap := g.parking.Load(); snap != nil {
		parked, parkingOK = snap.Parked, snap.Available
	}

	telem := types.StageTelemetry{
		ParkedRequests:   parked,
		ParkingAvailable: parkingOK,
		Workers:          workers,
		Actors:           views,
		RecentEvents:     eventsCopy,
		MockLLM:          g.mockLLM,
		StatePollMs:      g.statePollMs.Load(),
		WorkerPods:       pods,
		ActorCount:       g.maxActors,
		SeatsPerWorker:   g.seatsPerWorker,
	}
	if g.usage != nil {
		telem.ModelUsage = g.usage.TotalUsage()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(telem)
}

func (g *Gateway) handleActorDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	actorID := strings.TrimPrefix(r.URL.Path, "/api/actor/")
	actorID = strings.Trim(actorID, "/")
	if actorID == "" || actorID == "api/actor" || strings.Contains(actorID, "/") {
		http.Error(w, "invalid actor ID: must not contain slashes", http.StatusBadRequest)
		return
	}

	view := g.currentView(actorID)

	g.mu.RLock()
	var history []types.ChatMessage
	if h, ok := g.chatHistories[actorID]; ok && len(h) > 0 {
		history = make([]types.ChatMessage, len(h))
		copy(history, h)
	} else {
		history = []types.ChatMessage{}
	}

	pendingApproval := g.proxy.PendingApproval(actorID)

	var lastResult *types.ExecutionResult
	if lr, ok := g.lastResults[actorID]; ok && lr != nil {
		copyLR := *lr
		lastResult = &copyLR
	}
	g.mu.RUnlock()

	details := types.ActorDetails{
		ActorView:       view,
		ChatHistory:     history,
		PendingApproval: pendingApproval,
		LastResult:      lastResult,
	}
	if g.usage != nil {
		details.Usage = g.usage.Usage(actorID)
	}

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(details)
}

// GetLastResult returns the most recent execution result for an actor.
func (g *Gateway) GetLastResult(attendeeID string) (*types.ExecutionResult, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	res, ok := g.lastResults[attendeeID]
	if !ok || res == nil {
		return nil, false
	}
	copyRes := *res
	return &copyRes, true
}

// SetLastResult records the most recent execution result for an actor.
func (g *Gateway) SetLastResult(attendeeID string, res *types.ExecutionResult) {
	if attendeeID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if res == nil {
		delete(g.lastResults, attendeeID)
		return
	}
	copyRes := *res
	g.lastResults[attendeeID] = &copyRes
}

func (g *Gateway) pollStatus(ctx context.Context) {
	defer g.wg.Done()

	if g.routerStatus == nil {
		return
	}

	ticker := time.NewTicker(g.statusInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := g.routerStatus.Fetch(ctx)
			g.parking.Store(&snap)
		}
	}
}

// pollWorkerScale reads the worker pool's size once right away, so the stage
// header fills in without waiting a full interval, then every
// workerScaleInterval.
func (g *Gateway) pollWorkerScale(ctx context.Context) {
	defer g.wg.Done()

	ticker := time.NewTicker(g.workerScaleInterval)
	defer ticker.Stop()

	for {
		pods := g.workerScale.Fetch(ctx)
		g.workerPods.Store(&pods)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *Gateway) addEventLocked(event string) {
	const maxEvents = 50
	g.recentEvents = append(g.recentEvents, event)
	if len(g.recentEvents) > maxEvents {
		g.recentEvents = g.recentEvents[len(g.recentEvents)-maxEvents:]
	}
}

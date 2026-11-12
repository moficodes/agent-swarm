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
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

var telemetryGzipPool = sync.Pool{
	New: func() any {
		gz, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return gz
	},
}

// MetricsHandler returns an HTTP handler serving Prometheus metrics for this
// gateway instance on /metrics (intended for an internal-only metrics port).
func (g *Gateway) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		parked, parkingOK := g.scopedParking()
		inFlight := g.proxy.RouterInFlight()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if parkingOK {
			fmt.Fprintf(w, "# HELP conf_gateway_parked_requests Requests from this gateway currently parked in atenet-router.\n")
			fmt.Fprintf(w, "# TYPE conf_gateway_parked_requests gauge\n")
			fmt.Fprintf(w, "conf_gateway_parked_requests %d\n", parked)
		}
		fmt.Fprintf(w, "# HELP conf_gateway_router_in_flight HTTP requests currently awaiting atenet-router from this gateway.\n")
		fmt.Fprintf(w, "# TYPE conf_gateway_router_in_flight gauge\n")
		fmt.Fprintf(w, "conf_gateway_router_in_flight %d\n", inFlight)
	})
}

func (g *Gateway) scopedParking() (int, bool) {
	snap := g.parking.Load()
	if snap == nil {
		return 0, false
	}
	return min(snap.Parked, g.proxy.RouterInFlight()), snap.Available
}

func (g *Gateway) handleFleetBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	leasedIDs := g.LeasedIDs()
	bin := g.board.FleetBinarySnapshot(g.maxActors, g.leasedChecker(), leasedIDs)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if len(bin) > 4096 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := telemetryGzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		_, _ = gz.Write(bin)
		_ = gz.Close()
		telemetryGzipPool.Put(gz)
		return
	}
	_, _ = w.Write(bin)
}

func (g *Gateway) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var pods types.WorkerPods
	if p := g.workerPods.Load(); p != nil {
		pods = *p
	}

	compactParam := r.URL.Query().Get("compact")
	includeCompact := compactParam != "0" && (compactParam != "auto" || g.maxActors <= actorstate.HyperFleetThreshold)
	leasedIDs := g.LeasedIDs()

	var (
		views           map[string]types.ActorView
		fleetCompact    string
		runningActors   int
		suspendedActors int
		activeCohort    int
		snapshotted     int
		leasedCount     int
		workers         []types.WorkerStatus
		fastHyperDone   bool
	)

	if g.maxActors > actorstate.HyperFleetThreshold && !includeCompact && g.board.ActiveSeatCount() > 8192 {
		if podNums, compactActors, rCnt, sCnt, acCnt, snapCnt, lCnt, ok := g.board.HyperWorkerTelemetrySnapshot(
			g.maxActors, g.seatsPerWorker, g.leasedChecker(), leasedIDs,
		); ok {
			views = compactActors
			runningActors = rCnt
			suspendedActors = sCnt
			activeCohort = acCnt
			snapshotted = snapCnt
			leasedCount = lCnt
			podIndex := make(map[string]int, len(pods.PodNames)+len(podNums))
			workers = make([]types.WorkerStatus, 0, len(pods.PodNames)+16)
			for _, podName := range pods.PodNames {
				if podName == "" {
					continue
				}
				if _, exists := podIndex[podName]; !exists {
					podIndex[podName] = len(workers)
					workers = append(workers, types.WorkerStatus{
						PodName:   podName,
						ActorNums: podNums[podName],
					})
				}
			}
			for podName, nums := range podNums {
				if _, exists := podIndex[podName]; !exists {
					podIndex[podName] = len(workers)
					workers = append(workers, types.WorkerStatus{
						PodName:   podName,
						ActorNums: nums,
					})
				}
			}
			slices.SortFunc(workers, func(a, b types.WorkerStatus) int { return strings.Compare(a.PodName, b.PodName) })
			fastHyperDone = true
		}
	}

	if !fastHyperDone {
		views, fleetCompact, runningActors, suspendedActors, activeCohort, snapshotted, leasedCount = g.board.TelemetrySnapshotOptions(
			g.maxActors, g.leasedChecker(), leasedIDs, includeCompact,
		)
		names := slices.Sorted(maps.Keys(views))
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
			workers[i].Actors = append(workers[i].Actors, types.WorkerActor{
				ActorID:  v.ActorID,
				State:    v.State,
				Activity: v.Activity,
				Source:   v.Source,
			})
		}
		if g.maxActors > actorstate.HyperFleetThreshold {
			compactActors := make(map[string]types.ActorView, len(leasedIDs)+4)
			for name, v := range views {
				if v.Leased || v.State == types.StateCrashed || name == "attendee-01" {
					compactActors[name] = v
				}
			}
			if _, hasFirst := compactActors["attendee-01"]; !hasFirst {
				compactActors["attendee-01"] = g.currentView("attendee-01")
			}
			views = compactActors
		}
		if g.maxActors > 256 && g.seatsPerWorker > 0 {
			seatPriority := func(s types.SubstrateState) int {
				switch s {
				case types.StateRunning:
					return 0
				case types.StateResuming:
					return 1
				case types.StateSuspending:
					return 2
				default:
					return 3
				}
			}
			for i := range workers {
				if len(workers[i].Actors) > g.seatsPerWorker {
					slices.SortStableFunc(workers[i].Actors, func(a, b types.WorkerActor) int {
						return seatPriority(a.State) - seatPriority(b.State)
					})
					workers[i].Actors = workers[i].Actors[:g.seatsPerWorker]
				}
			}
		}
		slices.SortFunc(workers, func(a, b types.WorkerStatus) int { return strings.Compare(a.PodName, b.PodName) })
		if g.maxActors > actorstate.HyperFleetThreshold {
			totalWorkerSeats := 0
			for i := range workers {
				totalWorkerSeats += len(workers[i].Actors)
			}
			if totalWorkerSeats > 8192 {
				for i := range workers {
					if len(workers[i].Actors) == 0 {
						continue
					}
					nums := make([]int, 0, len(workers[i].Actors))
					allNumeric := true
					for _, wa := range workers[i].Actors {
						if idx, ok := actorstate.ParseAttendeeIndex(wa.ActorID, g.maxActors); ok {
							nums = append(nums, idx)
						} else {
							allNumeric = false
							break
						}
					}
					if allNumeric {
						workers[i].ActorNums = nums
						workers[i].Actors = nil
					}
				}
			}
		}
	}

	g.mu.RLock()
	eventsCopy := slices.Clone(g.recentEvents)
	g.mu.RUnlock()

	parked, parkingOK := g.scopedParking()

	telem := types.StageTelemetry{
		Atespace:           g.atespace,
		ParkedRequests:     parked,
		ParkingAvailable:   parkingOK,
		Workers:            workers,
		Actors:             views,
		FleetCompact:       fleetCompact,
		RunningActors:      runningActors,
		SuspendedActors:    suspendedActors,
		ActiveCohortActors: activeCohort,
		SnapshottedActors:  snapshotted,
		LeasedActors:       leasedCount,
		RecentEvents:       eventsCopy,
		MockLLM:            g.mockLLM,
		StatePollMs:        g.statePollMs.Load(),
		WorkerPods:         pods,
		ActorCount:         g.maxActors,
		SeatsPerWorker:     g.seatsPerWorker,
		HumanSlotStride:    g.HumanSlotStride(),
	}
	if g.usage != nil {
		telem.ModelUsage = g.usage.TotalUsage()
	}

	w.Header().Set("Content-Type", "application/json")
	if g.maxActors > actorstate.HyperFleetThreshold && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := telemetryGzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		_ = json.NewEncoder(gz).Encode(telem)
		_ = gz.Close()
		telemetryGzipPool.Put(gz)
		return
	}
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
	if g.maxActors > actorstate.HyperFleetThreshold && g.control != nil {
		if getter, hasGet := g.control.(actorstate.SingleActorGetter); hasGet {
			reqCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			if s, err := getter.Get(reqCtx, actorID); err == nil {
				view.State = s.State
				view.StateDetail = ""
				view.WorkerPod = s.WorkerPod
				view.SnapshotURI = s.SnapshotURI
				if s.InProgressSnapshotURI != "" {
					view.SnapshotURI = s.InProgressSnapshotURI
				}
				view.VolumeType, view.VolumeShare, view.VolumeURI = s.VolumeType, s.VolumeShare, s.VolumeURI
				view.NeverStarted = s.State == types.StateSuspended && s.SnapshotURI == "" && s.InProgressSnapshotURI == ""
			}
			cancel()
		}
	}
	view.Leased = g.IsLeased(actorID)

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
		inFlightBefore := g.proxy.RouterInFlight()
		snap := g.routerStatus.Fetch(ctx)
		inFlightAfter := g.proxy.RouterInFlight()
		snap.Parked = min(snap.Parked, max(inFlightBefore, inFlightAfter))
		g.parking.Store(&snap)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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

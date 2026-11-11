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
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/simulation"
	"github.com/agent-substrate/agent-swarm/internal/workload/engine"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

var simMockEngine = engine.NewMockEngine()

// CanAcquireSimulatedSeat reports whether the worker pool has available seat
// headroom for a simulated container turn. It reserves a small cushion for
// live human attendees and in-flight checkpoint drains so simulated bursts can
// pack pods to 13-16 seats/pod without triggering no-capacity errors.
func (g *Gateway) CanAcquireSimulatedSeat() bool {
	if g.maxActors <= 256 {
		return true
	}
	totalSeats := 4096
	if p := g.workerPods.Load(); p != nil && p.Available && p.Current > 0 && g.seatsPerWorker > 0 {
		totalSeats = p.Current * g.seatsPerWorker
	}
	reserve := max(400, totalSeats/9)
	maxSimSeats := max(1, totalSeats-reserve)

	g.mu.RLock()
	localCommitted := len(g.turnsInFlight) + len(g.suspendTimers) + len(g.suspendsInFlight)
	g.mu.RUnlock()
	if localCommitted >= maxSimSeats {
		return false
	}
	if g.board != nil && g.board.ActiveSeatCount() >= maxSimSeats {
		return false
	}
	return true
}

// SeedSimulatedApproval populates a checkpointed actor's human-in-the-loop
// review state (chat history, pending approval, and AwaitingApproval activity)
// while its memory snapshot rests in GCS during the 80-90% idle review window.
func (g *Gateway) SeedSimulatedApproval(attendeeID, prompt string) string {
	if g.IsLeased(attendeeID) {
		return ""
	}
	msg := simMockEngine.GenerateTurn(prompt)
	if msg == nil || msg.Approval == nil {
		return ""
	}
	msg.Source = types.SourceMock
	now := time.Now().Unix()
	userMsg := types.ChatMessage{
		ID:        fmt.Sprintf("user-seed-%s-%d", attendeeID, now),
		Sender:    types.SenderUser,
		Content:   prompt,
		Timestamp: now,
	}

	g.mu.Lock()
	if len(g.chatHistories[attendeeID]) == 0 {
		g.appendChatHistoryLocked(attendeeID, userMsg)
		g.appendChatHistoryLocked(attendeeID, *msg)
	}
	if g.proxy != nil {
		g.proxy.SetPending(attendeeID, msg.Approval)
	}
	g.mu.Unlock()

	if g.board != nil {
		g.board.SeedSimulatedActivity(attendeeID, types.ActivityAwaitingApproval, types.SourceMock)
		if g.control == nil {
			g.board.SetUnavailable(localModeReason)
		}
	}
	return msg.Approval.ApprovalID
}

// SimulationController defines the interface for managing background audience simulations.
type SimulationController = simulation.SimulationController

// SimulationRunner manages background virtual attendees interacting with the conference gateway.
type SimulationRunner = simulation.SimulationRunner

// SimulationOption configures SimulationRunner.
type SimulationOption = simulation.SimulationOption

// WithSimulationDelays sets the reading delay and loop delay for virtual attendees.
func WithSimulationDelays(readingDelay, loopDelay time.Duration) SimulationOption {
	return simulation.WithSimulationDelays(readingDelay, loopDelay)
}

// WithDataGeneratorExecutor configures SimulationRunner to drive turns through
// an external data-generator orchestrator and worker fleet.
func WithDataGeneratorExecutor(orchestratorURL, callbackTurnURL, presenterKey string) SimulationOption {
	return simulation.WithDataGenerator(orchestratorURL, callbackTurnURL, presenterKey)
}

// NewSimulationRunner constructs a SimulationRunner bound to the provided Gateway.
func NewSimulationRunner(gw *Gateway, opts ...SimulationOption) *SimulationRunner {
	var t simulation.Target
	if gw != nil {
		t = gw
	}
	return simulation.NewRunner(t, opts...)
}

func (g *Gateway) handleSimulationTurn(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/simulation/turn" {
		http.NotFound(w, r)
		return
	}
	sr := g.SimulationRunner()
	if sr == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sr.HandleTurn(w, r)
}

// SetSimulationController attaches an active simulation controller to the gateway.
func (g *Gateway) SetSimulationController(sc SimulationController) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.simController = sc
}

// SetSimulationRunner attaches an active simulation controller to the gateway.
// Deprecated: use SetSimulationController instead.
func (g *Gateway) SetSimulationRunner(sc SimulationController) {
	g.SetSimulationController(sc)
}

// SimulationController returns the active simulation controller.
func (g *Gateway) SimulationController() SimulationController {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.simController
}

// SimulationRunner returns the active simulation runner if it implements *SimulationRunner.
func (g *Gateway) SimulationRunner() *SimulationRunner {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if sr, ok := g.simController.(*SimulationRunner); ok {
		return sr
	}
	return nil
}

func (g *Gateway) handleSimulation(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/simulation" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		g.mu.RLock()
		sc := g.simController
		g.mu.RUnlock()

		running, count := false, 0
		if sc != nil {
			running, count = sc.Status()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"running": running,
			"count":   count,
		})

	case http.MethodPost:
		var req types.SimulationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid json payload: %v", err), http.StatusBadRequest)
			return
		}
		if err := req.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Virtual attendees map one-to-one onto actors, and there are no
		// more than maxActors of them.
		if req.Action == "start" && req.Count > g.maxActors {
			http.Error(w, fmt.Sprintf("invalid count: %d (the demo has %d actors)", req.Count, g.maxActors), http.StatusBadRequest)
			return
		}

		switch req.Action {
		case "start":
			g.mu.Lock()
			if g.simController == nil {
				g.simController = NewSimulationRunner(g)
			}
			sc := g.simController
			g.mu.Unlock()

			if err := sc.Start(req.Count); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			g.mu.Lock()
			g.addEventLocked(fmt.Sprintf("Started audience simulation with %d virtual attendees", req.Count))
			g.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"running": true,
				"count":   req.Count,
			})

		case "stop":
			g.mu.Lock()
			sc := g.simController
			g.mu.Unlock()

			if sc != nil {
				sc.Stop()
			}

			g.mu.Lock()
			g.addEventLocked("Stopped audience simulation")
			g.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"running": false,
				"count":   0,
			})
		}

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

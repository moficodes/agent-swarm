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

package simulation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const dataGeneratorSchemaYAML = `name: "agent_swarm_turn"
fields:
  - name: "turn_id"
    type: "uuid"
    primary: true
  - name: "prompt"
    type: "enum"
    options:
      - "Run anomaly analysis on recent telemetry spikes"
      - "Execute security audit on container runtime"
      - "Run benchmark sandbox test"
      - "Analyze conference traffic patterns and resource usage"
      - "Profile memory consumption across active worker pods"
      - "Verify isolation boundaries for tenant workloads"
  - name: "review_delay_ms"
    type: "random_int"
    min: 12000
    max: 20000
  - name: "idle_delay_ms"
    type: "random_int"
    min: 12000
    max: 20000
`

const dataGeneratorResetSchemaYAML = `name: "agent_swarm_reset"
fields:
  - name: "reset_id"
    type: "uuid"
    primary: true
  - name: "mode"
    type: "enum"
    options:
      - "reset"
`

const resetTargetMPS = 256

// WithDataGenerator configures SimulationRunner to delegate load generation to
// an external data-generator orchestrator and worker fleet running in-cluster.
func WithDataGenerator(orchestratorURL, callbackTurnURL, presenterKey string) SimulationOption {
	return func(s *SimulationRunner) {
		s.orchestratorURL = strings.TrimRight(strings.TrimSpace(orchestratorURL), "/")
		s.callbackTurnURL = strings.TrimSpace(callbackTurnURL)
		s.presenterKey = presenterKey
	}
}

// HasDataGenerator reports whether an external data-generator orchestrator is configured.
func (s *SimulationRunner) HasDataGenerator() bool {
	return s.orchestratorURL != ""
}

// WithDataGeneratorHTTPClient sets the HTTP client used to control the external
// data-generator orchestrator.
func WithDataGeneratorHTTPClient(client *http.Client) SimulationOption {
	return func(s *SimulationRunner) {
		if client != nil {
			s.dgClient = client
		}
	}
}

type dataGeneratorHTTPSinkConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
}

type dataGeneratorSinkConfig struct {
	Type string                       `json:"type"`
	HTTP *dataGeneratorHTTPSinkConfig `json:"http,omitempty"`
}

type dataGeneratorOrchestratorPayload struct {
	GlobalTargetMPS int                      `json:"global_target_mps"`
	SchemaYAML      string                   `json:"schema_yaml,omitempty"`
	IsPaused        bool                     `json:"is_paused"`
	SplitMode       bool                     `json:"split_mode"`
	Sink            *dataGeneratorSinkConfig `json:"sink,omitempty"`
}

// TurnPayload is the JSON payload POSTed by data-generator worker pods to
// /api/simulation/turn.
type TurnPayload struct {
	TurnID        string `json:"turn_id"`
	Prompt        string `json:"prompt"`
	ReviewDelayMs int    `json:"review_delay_ms"`
	IdleDelayMs   int    `json:"idle_delay_ms"`
}

func targetMPSForCount(count int, maxOverride int) int {
	switch {
	case count <= 16:
		return 4
	case count <= 256:
		return 25
	case count <= 4096:
		return 160
	case count <= 65536:
		return 240
	case count <= 262144:
		if maxOverride > 0 {
			return max(240, maxOverride/2)
		}
		return 1200
	default:
		if maxOverride > 0 {
			return maxOverride
		}
		return 3600
	}
}

func (s *SimulationRunner) configureDataGenerator(ctx context.Context, count int, paused bool) error {
	targetMPS := 0
	if !paused {
		targetMPS = targetMPSForCount(count, s.maxTargetMPS)
	}
	return s.configureDataGeneratorWithSchema(ctx, targetMPS, dataGeneratorSchemaYAML, count, paused)
}

func (s *SimulationRunner) configureDataGeneratorWithSchema(ctx context.Context, targetMPS int, schemaYAML string, count int, paused bool) error {
	if s.orchestratorURL == "" {
		return nil
	}
	payload := dataGeneratorOrchestratorPayload{
		GlobalTargetMPS: 0,
		IsPaused:        paused,
		SplitMode:       true,
		Sink: &dataGeneratorSinkConfig{
			Type: "stdout",
		},
	}
	if !paused {
		headers := map[string]string{
			"Content-Type": "application/json",
		}
		if s.presenterKey != "" {
			headers["X-Presenter-Key"] = s.presenterKey
		}
		payload.GlobalTargetMPS = targetMPS
		payload.SchemaYAML = schemaYAML
		payload.Sink = &dataGeneratorSinkConfig{
			Type: "http",
			HTTP: &dataGeneratorHTTPSinkConfig{
				URL:     s.callbackTurnURL,
				Method:  http.MethodPost,
				Headers: headers,
				Timeout: "30s",
			},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal data-generator config: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.orchestratorURL+"/api/v1/config", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create data-generator request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.dgClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("calling data-generator orchestrator %s: %w", s.orchestratorURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("data-generator orchestrator returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	log.Printf("[simulation] configured external data-generator orchestrator (%s): paused=%v count=%d target_mps=%d",
		s.orchestratorURL, paused, count, payload.GlobalTargetMPS)
	return nil
}

func (s *SimulationRunner) isResetting() bool {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()
	return s.resetting
}

func (s *SimulationRunner) initResetLocked(crashed, active []string) {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()
	now := time.Now()
	s.resetting = true
	s.resetStartedAt = now
	s.resetSettleAfter = now.Add(s.resetSettleWindow)
	s.resetQueue = make([]resetWorkItem, 0, len(crashed)+len(active))
	s.resetQueued = make(map[string]bool, len(crashed)+len(active))
	s.resetInFlight = make(map[string]bool)
	s.resetDoneAt = make(map[string]time.Time)
	s.resetSuspended = 0
	s.resetReverted = 0
	s.resetErrors = 0

	for _, id := range crashed {
		if id == "" || s.resetQueued[id] {
			continue
		}
		s.resetQueued[id] = true
		s.resetQueue = append(s.resetQueue, resetWorkItem{actorID: id, op: "revert"})
	}
	for _, id := range active {
		if id == "" || s.resetQueued[id] {
			continue
		}
		s.resetQueued[id] = true
		s.resetQueue = append(s.resetQueue, resetWorkItem{actorID: id, op: "suspend"})
	}
}

func (s *SimulationRunner) clearResetLocked() {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()
	s.resetting = false
	s.resetQueue = nil
	s.resetQueued = nil
	s.resetInFlight = nil
	s.resetDoneAt = nil
}

// ResetStatus reports whether an external data-generator reset sweep is active
// and how many queued or in-flight actor reset operations remain.
func (s *SimulationRunner) ResetStatus() (bool, int) {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()
	if !s.resetting {
		return false, 0
	}
	return true, len(s.resetQueue) + len(s.resetInFlight)
}

// StartReset switches the external data-generator fleet into reset mode so its
// worker pods drive cluster-wide Revert and Suspend RPCs back through
// HandleTurn, auto-pausing data-generator once all actors settle in SUSPENDED.
func (s *SimulationRunner) StartReset(crashed, active []string) (bool, error) {
	if s.orchestratorURL == "" {
		return false, nil
	}
	if _, ok := s.target.(ResetHandler); !ok {
		return false, nil
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	var baseCtx context.Context
	if s.target != nil {
		baseCtx = s.target.Context()
	}
	if baseCtx != nil && baseCtx.Err() != nil {
		return false, fmt.Errorf("cannot start reset: gateway is closed: %w", baseCtx.Err())
	}

	s.stopActiveRun(false)

	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(baseCtx)

	s.mu.Lock()
	s.simCtx = ctx
	s.cancel = cancel
	s.runWG = &sync.WaitGroup{}
	s.initResetLocked(crashed, active)
	s.mu.Unlock()

	if err := s.configureDataGeneratorWithSchema(ctx, resetTargetMPS, dataGeneratorResetSchemaYAML, len(crashed)+len(active), false); err != nil {
		s.mu.Lock()
		s.clearResetLocked()
		s.simCtx = nil
		s.cancel = nil
		s.mu.Unlock()
		cancel()
		return false, err
	}
	return true, nil
}

func (s *SimulationRunner) enqueueResetStragglersLocked(now time.Time, rh ResetHandler) int {
	if rh == nil {
		return 0
	}
	crashed, active := rh.ActiveActorsForReset()
	unhandled := 0
	enqueue := func(id, op string) {
		if id == "" {
			return
		}
		if s.resetQueued[id] || s.resetInFlight[id] {
			unhandled++
			return
		}
		if doneAt, ok := s.resetDoneAt[id]; ok && now.Sub(doneAt) < 4*time.Second {
			return
		}
		s.resetQueued[id] = true
		s.resetQueue = append(s.resetQueue, resetWorkItem{actorID: id, op: op})
		unhandled++
	}
	for _, id := range crashed {
		enqueue(id, "revert")
	}
	for _, id := range active {
		enqueue(id, "suspend")
	}
	return unhandled
}

func (s *SimulationRunner) checkResetCompletionLocked(now time.Time, rh ResetHandler) (bool, int, int, int) {
	if !s.resetting {
		return false, 0, 0, 0
	}
	if time.Since(s.resetStartedAt) > 45*time.Second {
		s.resetting = false
		return true, s.resetSuspended, s.resetReverted, s.resetErrors
	}
	if len(s.resetQueue) > 0 || len(s.resetInFlight) > 0 {
		return false, 0, 0, 0
	}
	if s.enqueueResetStragglersLocked(now, rh) > 0 {
		return false, 0, 0, 0
	}
	if now.Before(s.resetSettleAfter) {
		return false, 0, 0, 0
	}
	s.resetting = false
	return true, s.resetSuspended, s.resetReverted, s.resetErrors
}

func (s *SimulationRunner) executeNextResetTurn(ctx context.Context) (bool, string, string, error) {
	rh, ok := s.target.(ResetHandler)
	if !ok {
		return false, "", "", nil
	}

	now := time.Now()
	var item resetWorkItem
	var claimed bool
	var finished bool
	var suspended, reverted, errs int

	s.resetMu.Lock()
	if !s.resetting {
		s.resetMu.Unlock()
		return false, "", "", nil
	}
	if len(s.resetQueue) == 0 {
		s.enqueueResetStragglersLocked(now, rh)
	}
	for i := 0; i < len(s.resetQueue); i++ {
		if !now.Before(s.resetQueue[i].readyAt) {
			item = s.resetQueue[i]
			s.resetQueue = append(s.resetQueue[:i], s.resetQueue[i+1:]...)
			delete(s.resetQueued, item.actorID)
			s.resetInFlight[item.actorID] = true
			claimed = true
			break
		}
	}
	if !claimed {
		finished, suspended, reverted, errs = s.checkResetCompletionLocked(now, rh)
	}
	s.resetMu.Unlock()

	if finished {
		s.finishDataGeneratorReset(rh, suspended, reverted, errs)
		return false, "", "reset_complete", nil
	}
	if !claimed {
		return false, "", "", nil
	}

	var err error
	if item.op == "revert" {
		err = rh.RevertActorForReset(ctx, item.actorID)
	} else {
		err = rh.SuspendActorForReset(ctx, item.actorID)
	}

	afterNow := time.Now()
	s.resetMu.Lock()
	delete(s.resetInFlight, item.actorID)
	if s.resetting {
		if err == nil {
			s.resetDoneAt[item.actorID] = afterNow
			if item.op == "revert" {
				s.resetReverted++
			} else {
				s.resetSuspended++
			}
		} else if (strings.Contains(err.Error(), "another operation is in progress") || strings.Contains(err.Error(), "Aborted")) && item.attempts < 10 {
			item.attempts++
			item.readyAt = afterNow.Add(500 * time.Millisecond)
			if !s.resetQueued[item.actorID] {
				s.resetQueued[item.actorID] = true
				s.resetQueue = append(s.resetQueue, item)
			}
			err = nil
		} else {
			s.resetDoneAt[item.actorID] = afterNow
			s.resetErrors++
		}
		finished, suspended, reverted, errs = s.checkResetCompletionLocked(afterNow, rh)
	}
	s.resetMu.Unlock()

	if finished {
		s.finishDataGeneratorReset(rh, suspended, reverted, errs)
	}
	return true, item.actorID, "reset_" + item.op, err
}

func (s *SimulationRunner) finishDataGeneratorReset(rh ResetHandler, suspended, reverted, errs int) {
	if err := s.configureDataGenerator(context.Background(), 0, true); err != nil {
		log.Printf("[simulation] warning: failed to pause data-generator after reset: %v", err)
	}
	if rh != nil {
		rh.OnResetComplete(suspended, reverted, errs)
	}
}

// HandleTurn processes one simulated agent turn (or one reset operation when in
// reset mode) triggered by an external data-generator worker pod via
// POST /api/simulation/turn.
func (s *SimulationRunner) HandleTurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	running := s.running
	simCount := s.count
	resetting := s.isResetting()
	simCtx := s.simCtx
	runWG := s.runWG
	rDelay := s.readingDelay
	lDelay := s.loopDelay
	active := (running || resetting) && simCtx != nil && simCtx.Err() == nil && runWG != nil
	if active {
		runWG.Add(1)
	}
	s.mu.RUnlock()

	if !active {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var payload TurnPayload
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&payload)
	}

	if !resetting && simCount > 65536 {
		runWG.Done()
		reviewDelay := time.Duration(payload.ReviewDelayMs) * time.Millisecond
		if reviewDelay <= 0 {
			reviewDelay = s.calculateReviewDelay(0, rDelay)
		}
		idleDelay := time.Duration(payload.IdleDelayMs) * time.Millisecond
		if idleDelay <= 0 {
			idleDelay = s.calculateInterTurnDelay(lDelay)
		}
		now := time.Now()
		dispatched := 0
		const hyperBatchFanout = 2
	fanoutLoop:
		for range hyperBatchFanout {
			if s.turnSem != nil {
				select {
				case s.turnSem <- struct{}{}:
				default:
					break fanoutLoop
				}
			}
			if checker, ok := s.target.(SeatLoadChecker); ok && !checker.CanAcquireSimulatedSeat() {
				if s.turnSem != nil {
					<-s.turnSem
				}
				break fanoutLoop
			}
			slotIndex, phase, approvalID, ok := s.claimNextSlot(now)
			if !ok {
				if s.turnSem != nil {
					<-s.turnSem
				}
				break fanoutLoop
			}
			runWG.Add(1)
			dispatched++
			go func(idx int, ph slotPhase, appID, prompt string) {
				defer func() {
					if s.turnSem != nil {
						<-s.turnSem
					}
					runWG.Done()
				}()
				_, _, _, _ = s.runClaimedTurn(simCtx, idx, ph, appID, prompt, reviewDelay, idleDelay)
			}(slotIndex, phase, approvalID, payload.Prompt)
		}
		if dispatched == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"executed": true, "queued": true, "dispatched": dispatched})
		return
	}

	defer runWG.Done()

	if s.turnSem != nil {
		select {
		case s.turnSem <- struct{}{}:
			defer func() { <-s.turnSem }()
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	ctx := r.Context()
	if simCtx != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		stop := context.AfterFunc(simCtx, cancel)
		defer func() {
			stop()
			cancel()
		}()
	}

	var (
		executed   bool
		attendeeID string
		action     string
		err        error
	)
	if resetting {
		executed, attendeeID, action, err = s.executeNextResetTurn(ctx)
	} else {
		reviewDelay := time.Duration(payload.ReviewDelayMs) * time.Millisecond
		if reviewDelay <= 0 {
			reviewDelay = s.calculateReviewDelay(0, rDelay)
		}
		idleDelay := time.Duration(payload.IdleDelayMs) * time.Millisecond
		if idleDelay <= 0 {
			idleDelay = s.calculateInterTurnDelay(lDelay)
		}
		executed, attendeeID, action, err = s.executeNextTurn(ctx, payload.Prompt, reviewDelay, idleDelay)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"executed":    false,
			"attendee_id": attendeeID,
			"action":      action,
			"error":       err.Error(),
		})
		return
	}
	if !executed {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"executed":    true,
		"attendee_id": attendeeID,
		"action":      action,
	})
}

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

// WithDataGenerator configures SimulationRunner to delegate load generation to
// an external data-generator orchestrator and worker fleet running in-cluster.
func WithDataGenerator(orchestratorURL, callbackTurnURL, presenterKey string) SimulationOption {
	return func(s *SimulationRunner) {
		s.orchestratorURL = strings.TrimRight(strings.TrimSpace(orchestratorURL), "/")
		s.callbackTurnURL = strings.TrimSpace(callbackTurnURL)
		s.presenterKey = presenterKey
	}
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

func targetMPSForCount(count int) int {
	switch {
	case count <= 16:
		return 4
	case count <= 256:
		return 25
	case count <= 4096:
		return 96
	default:
		return 160
	}
}

func (s *SimulationRunner) configureDataGenerator(ctx context.Context, count int, paused bool) error {
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
		payload.GlobalTargetMPS = targetMPSForCount(count)
		payload.SchemaYAML = dataGeneratorSchemaYAML
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

// HandleTurn processes one simulated agent turn triggered by an external
// data-generator worker pod via POST /api/simulation/turn.
func (s *SimulationRunner) HandleTurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	running := s.running
	simCtx := s.simCtx
	rDelay := s.readingDelay
	lDelay := s.loopDelay
	if running && simCtx != nil && simCtx.Err() == nil {
		s.wg.Add(1)
	} else {
		running = false
	}
	s.mu.RUnlock()

	if !running {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer s.wg.Done()

	if s.turnSem != nil {
		select {
		case s.turnSem <- struct{}{}:
			defer func() { <-s.turnSem }()
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	var payload TurnPayload
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&payload)
	}

	reviewDelay := time.Duration(payload.ReviewDelayMs) * time.Millisecond
	if reviewDelay <= 0 {
		reviewDelay = s.calculateReviewDelay(0, rDelay)
	}
	idleDelay := time.Duration(payload.IdleDelayMs) * time.Millisecond
	if idleDelay <= 0 {
		idleDelay = s.calculateInterTurnDelay(lDelay)
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

	executed, attendeeID, action, err := s.executeNextTurn(ctx, payload.Prompt, reviewDelay, idleDelay)
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

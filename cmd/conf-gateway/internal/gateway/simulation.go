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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

var simulationPrompts = []string{
	"Run anomaly analysis on recent telemetry spikes",
	"Execute security audit on container runtime",
	"Run benchmark sandbox test",
	"Analyze conference traffic patterns and resource usage",
	"Profile memory consumption across active worker pods",
	"Verify isolation boundaries for tenant workloads",
}

// SimulationOption configures SimulationRunner.
type SimulationOption func(*SimulationRunner)

// WithSimulationDelays sets the reading delay and loop delay for virtual attendees.
func WithSimulationDelays(readingDelay, loopDelay time.Duration) SimulationOption {
	return func(s *SimulationRunner) {
		if readingDelay >= 0 {
			s.readingDelay = readingDelay
		}
		if loopDelay >= 0 {
			s.loopDelay = loopDelay
		}
	}
}

// SimulationRunner manages background virtual attendees interacting with the conference gateway.
type SimulationRunner struct {
	gw           *Gateway
	mu           sync.RWMutex
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	running      bool
	count        int
	readingDelay time.Duration
	loopDelay    time.Duration
}

// NewSimulationRunner constructs a SimulationRunner bound to the provided Gateway.
func NewSimulationRunner(gw *Gateway, opts ...SimulationOption) *SimulationRunner {
	s := &SimulationRunner{
		gw:           gw,
		readingDelay: 2 * time.Second,
		loopDelay:    3 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start begins the audience simulation with the specified number of virtual attendees.
// If a simulation is already running, it halts the previous simulation first.
func (s *SimulationRunner) Start(count int) error {
	if count < 1 || count > 100 {
		return fmt.Errorf("invalid attendee count: %d (must be between 1 and 100)", count)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var baseCtx context.Context
	if s.gw != nil {
		baseCtx = s.gw.ctx
	}
	if baseCtx != nil && baseCtx.Err() != nil {
		return fmt.Errorf("cannot start simulation: gateway is closed: %w", baseCtx.Err())
	}

	if s.running {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
		s.running = false
		s.count = 0
		s.cancel = nil
	}

	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(baseCtx)
	s.cancel = cancel
	s.running = true
	s.count = count

	rDelay := s.readingDelay
	lDelay := s.loopDelay
	for i := 0; i < count; i++ {
		attendeeID := fmt.Sprintf("attendee-%02d", i+1)
		s.wg.Add(1)
		go s.runAttendee(ctx, attendeeID, i, rDelay, lDelay)
	}

	return nil
}

// Stop halts all active virtual attendees and waits for their goroutines to exit.
func (s *SimulationRunner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}
	s.running = false
	s.count = 0
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.wg.Wait()
}

// Status returns whether the simulation is currently active and the number of virtual attendees.
func (s *SimulationRunner) Status() (bool, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running, s.count
}

func (s *SimulationRunner) jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	maxJitterMs := 500
	if base < time.Second {
		maxJitterMs = int(base.Milliseconds())
	}
	if maxJitterMs <= 0 {
		return 0
	}
	return time.Duration(rand.Intn(maxJitterMs)) * time.Millisecond
}

func (s *SimulationRunner) runAttendee(ctx context.Context, attendeeID string, offset int, rDelay, lDelay time.Duration) {
	defer s.wg.Done()

	token := s.gw.MintToken(attendeeID)

	// Stagger attendee startup slightly to distribute requests
	if offset > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration((offset%5)*20) * time.Millisecond):
		}
	}

	promptIdx := offset % len(simulationPrompts)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		prompt := simulationPrompts[promptIdx]
		promptIdx = (promptIdx + 1) % len(simulationPrompts)

		chatMsg, err := s.sendChat(ctx, attendeeID, token, prompt)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		if chatMsg != nil && chatMsg.Approval != nil && chatMsg.Approval.ApprovalID != "" {
			sleepDur := rDelay + s.jitter(rDelay)
			if sleepDur > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(sleepDur):
				}
			}

			_ = s.sendApprove(ctx, attendeeID, token, chatMsg.Approval.ApprovalID)
		}

		sleepDur := lDelay + s.jitter(lDelay)
		if sleepDur > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(sleepDur):
			}
		}
	}
}

func (s *SimulationRunner) sendChat(ctx context.Context, attendeeID, token, prompt string) (*types.ChatMessage, error) {
	chatReq := types.ChatRequest{
		Message: prompt,
	}
	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/chat", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Attendee-ID", attendeeID)

	rec := httptest.NewRecorder()
	s.gw.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(res.Body)
		if len(respBody) > 0 {
			return nil, fmt.Errorf("chat request failed with status %d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
		}
		return nil, fmt.Errorf("chat request failed with status %d", res.StatusCode)
	}

	var chatMsg types.ChatMessage
	if err := json.NewDecoder(res.Body).Decode(&chatMsg); err != nil {
		return nil, err
	}
	return &chatMsg, nil
}

func (s *SimulationRunner) sendApprove(ctx context.Context, attendeeID, token, approvalID string) error {
	appReq := types.ApprovalResponse{
		ApprovalID: approvalID,
		Decision:   types.DecisionApprove,
	}
	bodyBytes, err := json.Marshal(appReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/approve", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Attendee-ID", attendeeID)

	rec := httptest.NewRecorder()
	s.gw.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(res.Body)
		if len(respBody) > 0 {
			return fmt.Errorf("approve request failed with status %d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
		}
		return fmt.Errorf("approve request failed with status %d", res.StatusCode)
	}
	return nil
}

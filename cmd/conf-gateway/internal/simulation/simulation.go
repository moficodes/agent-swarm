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

// Package simulation drives virtual attendees through the gateway's own
// HTTP handler.
package simulation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// SimulationController defines the interface for managing background audience simulations.
type SimulationController interface {
	Start(count int) error
	Stop()
	Status() (bool, int)
}

// Target is the gateway a simulation drives.
type Target interface {
	// Context is canceled when the gateway closes.
	Context() context.Context
	MintToken(attendee string) string
	Handler() http.Handler
}

// LeaseChecker is optionally implemented by Target to report whether an
// attendee slot is reserved or currently leased by a live QR-code user.
type LeaseChecker interface {
	IsReservedForHuman(attendeeID string, slotIndex int) bool
}

// HumanStrideProvider is optionally implemented by Target to report the
// periodic human-reserved attendee slot stride (e.g. 16 on megaswarm, 4096 on hyperswarm).
type HumanStrideProvider interface {
	HumanSlotStride() int
}

// StopCleanup is optionally implemented by Target to reset simulated activity
// on non-leased actors when the simulation stops.
type StopCleanup interface {
	ClearSimulatedActivity()
}

// SeatLoadChecker is optionally implemented by Target to apply backpressure
// when physical worker pod seats are near saturation.
type SeatLoadChecker interface {
	CanAcquireSimulatedSeat() bool
}

// ApprovalSeeder is optionally implemented by Target to seed a checkpointed
// actor's human-review state (AwaitingApproval) during massive oversubscription.
type ApprovalSeeder interface {
	SeedSimulatedApproval(attendeeID, prompt string) string
}

// BulkApprovalSeeder is optionally implemented by Target to bulk-seed
// simulated AwaitingApproval state by 1-based actor indices at 16.78M scale
// without per-actor string formatting or map growth.
type BulkApprovalSeeder interface {
	SeedSimulatedApprovalIndices(indices []int)
}

// ResetHandler is optionally implemented by Target so external data-generator
// workers can drive cluster-wide actor revert and suspend operations during a
// stage reset.
type ResetHandler interface {
	SuspendActorForReset(ctx context.Context, attendeeID string) error
	RevertActorForReset(ctx context.Context, attendeeID string) error
	ActiveActorsForReset() (crashed []string, active []string)
	OnResetComplete(suspended int, reverted int, errors int)
}

type resetWorkItem struct {
	actorID  string
	op       string
	readyAt  time.Time
	attempts int
}

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

// WithResetSettleWindow sets how long data-generator continues sweeping for
// straggler active actors after the reset queue empties before auto-pausing.
func WithResetSettleWindow(d time.Duration) SimulationOption {
	return func(s *SimulationRunner) {
		if d >= 0 {
			s.resetSettleWindow = d
		}
	}
}

// WithMaxConcurrentTurns sets the semaphore cap on concurrent simulated turns.
func WithMaxConcurrentTurns(n int) SimulationOption {
	return func(s *SimulationRunner) {
		if n > 0 {
			s.turnSem = make(chan struct{}, n)
		}
	}
}

// WithMaxTargetMPS overrides the maximum MPS configured on data-generator for >65k runs.
func WithMaxTargetMPS(mps int) SimulationOption {
	return func(s *SimulationRunner) {
		if mps > 0 {
			s.maxTargetMPS = mps
		}
	}
}

type slotPhase uint8

const (
	phaseIdle slotPhase = iota
	phaseChatInFlight
	phaseAwaitingApproval
	phaseApproveInFlight
)

// slotState is intentionally pointer-free (16 bytes) so Go's GC marks a
// 16,777,216-element slice in 0ms as a noscan span. Container approval IDs
// are stored in the sparse containerApprovals map only while hasContainerApproval is true.
type slotState struct {
	readyAtUnixMs        int64
	phase                slotPhase
	hasContainerApproval bool
}

const recentIdleCap = 8192

func gcdUint64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func coprimeStrideForTotal(totalCount int) uint64 {
	n := uint64(totalCount)
	if n <= 1 {
		return 1
	}
	// Golden-ratio Weyl odd multiplier scatters consecutive indices uniformly
	// across 2D 4096x4096 grids without diagonal stripe aliasing.
	const weylOdd = uint64(2654435761)
	if totalCount > 256 && gcdUint64(weylOdd, n) == 1 {
		return weylOdd
	}
	if gcdUint64(4099, n) == 1 {
		return 4099
	}
	return 1
}

// SimulationRunner manages background virtual attendees interacting with the conference gateway.
type SimulationRunner struct {
	target            Target
	opMu              sync.Mutex
	mu                sync.RWMutex
	simCtx            context.Context
	cancel            context.CancelFunc
	runWG             *sync.WaitGroup
	running           bool
	count             int
	readingDelay      time.Duration
	loopDelay         time.Duration
	orchestratorURL   string
	callbackTurnURL   string
	presenterKey      string
	dgClient          *http.Client
	turnSem           chan struct{}
	maxTargetMPS      int
	resetSettleWindow time.Duration

	slotMu             sync.Mutex
	slots              []slotState
	containerApprovals map[int]string
	readyQueue         []int
	recentIdle         []int
	recentHead         int
	recentCount        int
	turnCursor         uint64
	dispatchSeq        uint64

	resetMu          sync.Mutex
	resetting        bool
	resetQueue       []resetWorkItem
	resetQueued      map[string]bool
	resetInFlight    map[string]bool
	resetDoneAt      map[string]time.Time
	resetStartedAt   time.Time
	resetSettleAfter time.Time
	resetSuspended   int
	resetReverted    int
	resetErrors      int
}

// NewRunner constructs a SimulationRunner bound to the provided Target.
func NewRunner(t Target, opts ...SimulationOption) *SimulationRunner {
	s := &SimulationRunner{
		target:            t,
		readingDelay:      2 * time.Second,
		loopDelay:         3 * time.Second,
		turnSem:           make(chan struct{}, 3072),
		resetSettleWindow: 2500 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *SimulationRunner) initSlotsLocked(count int) {
	s.slotMu.Lock()
	defer s.slotMu.Unlock()
	s.slots = make([]slotState, count+1)
	s.containerApprovals = make(map[int]string)
	s.readyQueue = nil
	s.recentIdle = make([]int, recentIdleCap)
	s.recentHead = 0
	s.recentCount = 0
	s.turnCursor = 0
	s.dispatchSeq = 0
}

func (s *SimulationRunner) clearSlotsLocked() {
	s.slotMu.Lock()
	defer s.slotMu.Unlock()
	s.slots = nil
	s.containerApprovals = nil
	s.readyQueue = nil
	s.recentIdle = nil
	s.recentHead = 0
	s.recentCount = 0
	s.turnCursor = 0
	s.dispatchSeq = 0
}

func (s *SimulationRunner) pushRecentIdleLocked(idx int) {
	if len(s.recentIdle) == 0 || idx <= 0 {
		return
	}
	if s.recentCount < len(s.recentIdle) {
		tail := (s.recentHead + s.recentCount) % len(s.recentIdle)
		s.recentIdle[tail] = idx
		s.recentCount++
		return
	}
	s.recentIdle[s.recentHead] = idx
	s.recentHead = (s.recentHead + 1) % len(s.recentIdle)
}

func (s *SimulationRunner) stopActiveRun(pauseOrchestrator bool) bool {
	s.mu.Lock()
	wasActive := s.running || s.isResetting()
	if !wasActive {
		s.mu.Unlock()
		return false
	}
	wasCount := s.count
	wg := s.runWG
	s.runWG = nil
	s.running = false
	s.count = 0
	cancel := s.cancel
	s.cancel = nil
	s.simCtx = nil
	s.clearResetLocked()
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if pauseOrchestrator && s.orchestratorURL != "" {
		if err := s.configureDataGenerator(context.Background(), 0, true); err != nil {
			log.Printf("[simulation] warning: failed to pause external data-generator orchestrator: %v", err)
		}
	}
	if wg != nil {
		if wasCount > 65536 {
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			timer := time.NewTimer(1500 * time.Millisecond)
			select {
			case <-done:
				timer.Stop()
			case <-timer.C:
			}
		} else {
			wg.Wait()
		}
	}
	s.clearSlotsLocked()
	if sc, ok := s.target.(StopCleanup); ok {
		sc.ClearSimulatedActivity()
	}
	return true
}

// Start begins the audience simulation with the specified number of virtual attendees.
// If a simulation is already running, it halts the previous simulation first.
func (s *SimulationRunner) Start(count int) error {
	if count < 1 || count > types.MaxSimulationAttendees {
		return fmt.Errorf("invalid attendee count: %d (must be between 1 and %d)", count, types.MaxSimulationAttendees)
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	var baseCtx context.Context
	if s.target != nil {
		baseCtx = s.target.Context()
	}
	if baseCtx != nil && baseCtx.Err() != nil {
		return fmt.Errorf("cannot start simulation: gateway is closed: %w", baseCtx.Err())
	}

	s.stopActiveRun(false)

	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(baseCtx)
	s.initSlotsLocked(count)

	s.mu.Lock()
	runWG := &sync.WaitGroup{}
	s.simCtx = ctx
	s.cancel = cancel
	s.runWG = runWG
	s.running = true
	s.count = count
	rDelay := s.readingDelay
	lDelay := s.loopDelay
	s.mu.Unlock()

	if s.orchestratorURL != "" {
		if err := s.configureDataGenerator(ctx, count, false); err == nil {
			return nil
		} else {
			log.Printf("[simulation] external data-generator orchestrator unavailable (%v); falling back to in-process simulation workers", err)
		}
	}

	if count <= 256 {
		for i := 0; i < count; i++ {
			attendeeID := fmt.Sprintf("attendee-%02d", i+1)
			runWG.Add(1)
			go s.runAttendee(ctx, runWG, attendeeID, i, rDelay, lDelay)
		}
	} else {
		numWorkers := min(count, 256)
		for w := 0; w < numWorkers; w++ {
			runWG.Add(1)
			go s.runStridedWorker(ctx, runWG, w, rDelay, lDelay)
		}
	}

	return nil
}

// Stop halts all active virtual attendees and waits for their goroutines to exit.
func (s *SimulationRunner) Stop() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.stopActiveRun(true)
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

func (s *SimulationRunner) calculateReviewDelay(offset int, rDelay time.Duration) time.Duration {
	if rDelay < 100*time.Millisecond {
		return rDelay + s.jitter(rDelay)
	}
	// Realistic human-in-the-loop review delay (~12s - 20s) for an 80-90% idle duty cycle:
	// - Fast Reviewers (30%): 12s - 14s
	// - Standard Reviewers (50%): 14s - 18s
	// - Thorough Reviewers (20%): 18s - 20s
	bucket := offset % 10
	switch {
	case bucket < 3:
		return time.Duration(12000+rand.Intn(2000)) * time.Millisecond
	case bucket < 8:
		return time.Duration(14000+rand.Intn(4000)) * time.Millisecond
	default:
		return time.Duration(18000+rand.Intn(2000)) * time.Millisecond
	}
}

func (s *SimulationRunner) calculateInterTurnDelay(lDelay time.Duration) time.Duration {
	if lDelay < 100*time.Millisecond {
		return lDelay + s.jitter(lDelay)
	}
	// Between 12s and 20s idle pause between conversation turns (80-90% idle time)
	return time.Duration(12000+rand.Intn(8000)) * time.Millisecond
}

func (s *SimulationRunner) isReserved(attendeeID string, slotIndex int) bool {
	if lc, ok := s.target.(LeaseChecker); ok {
		return lc.IsReservedForHuman(attendeeID, slotIndex)
	}
	return false
}

func (s *SimulationRunner) humanStrideLocked(totalCount int) int {
	if hp, ok := s.target.(HumanStrideProvider); ok {
		if st := hp.HumanSlotStride(); st > 0 {
			return st
		}
	}
	if totalCount > 65536 {
		return 4096
	}
	if totalCount > 256 {
		return 16
	}
	return 0
}

func (s *SimulationRunner) claimMaturedApprovalLocked(now time.Time, totalCount int) (int, slotPhase, string, bool) {
	nowMs := now.UnixMilli()
	scanLimit := min(len(s.readyQueue), 128)
	for i := 0; i < scanLimit; i++ {
		idx := s.readyQueue[i]
		if idx < 1 || idx > totalCount {
			continue
		}
		st := &s.slots[idx]
		if st.phase == phaseAwaitingApproval && st.hasContainerApproval && nowMs >= st.readyAtUnixMs {
			appID := s.containerApprovals[idx]
			delete(s.containerApprovals, idx)
			st.phase = phaseApproveInFlight
			st.hasContainerApproval = false
			s.readyQueue = append(s.readyQueue[:i], s.readyQueue[i+1:]...)
			return idx, phaseApproveInFlight, appID, true
		}
	}
	return 0, phaseIdle, "", false
}

func (s *SimulationRunner) claimMaturedRecentIdleLocked(nowMs int64, totalCount, hStride int) (int, bool) {
	if s.recentCount == 0 || len(s.recentIdle) == 0 {
		return 0, false
	}
	limit := min(s.recentCount, 16)
	for i := 0; i < limit; i++ {
		pos := (s.recentHead + i) % len(s.recentIdle)
		idx := s.recentIdle[pos]
		if idx < 1 || idx > totalCount || (hStride > 1 && idx%hStride == 0) {
			continue
		}
		st := &s.slots[idx]
		if st.phase != phaseIdle || nowMs < st.readyAtUnixMs {
			continue
		}
		attendeeID := fmt.Sprintf("attendee-%02d", idx)
		if s.isReserved(attendeeID, idx) {
			continue
		}
		// Remove claimed slot by swapping from head.
		s.recentIdle[pos] = s.recentIdle[s.recentHead]
		s.recentHead = (s.recentHead + 1) % len(s.recentIdle)
		s.recentCount--
		st.phase = phaseChatInFlight
		st.hasContainerApproval = false
		return idx, true
	}
	return 0, false
}

func (s *SimulationRunner) claimIdleChatSlotLocked(now time.Time, totalCount int) (int, slotPhase, string, bool) {
	nowMs := now.UnixMilli()
	hStride := s.humanStrideLocked(totalCount)

	// Re-wake matured checkpointed actors on every 3rd dispatch so individual
	// actors visibly cycle repeatedly while fresh actors continue illuminating the fleet.
	if s.dispatchSeq%3 == 0 {
		if idx, ok := s.claimMaturedRecentIdleLocked(nowMs, totalCount, hStride); ok {
			return idx, phaseChatInFlight, "", true
		}
	}

	stride := coprimeStrideForTotal(totalCount)
	probes := min(totalCount, 128)
	for p := 0; p < probes; p++ {
		step := s.turnCursor
		s.turnCursor++
		idx := int((step*stride)%uint64(totalCount)) + 1
		if hStride > 1 && idx%hStride == 0 {
			continue
		}
		st := &s.slots[idx]
		if st.phase != phaseIdle || nowMs < st.readyAtUnixMs {
			continue
		}
		attendeeID := fmt.Sprintf("attendee-%02d", idx)
		if s.isReserved(attendeeID, idx) {
			continue
		}
		st.phase = phaseChatInFlight
		st.hasContainerApproval = false
		return idx, phaseChatInFlight, "", true
	}

	if idx, ok := s.claimMaturedRecentIdleLocked(nowMs, totalCount, hStride); ok {
		return idx, phaseChatInFlight, "", true
	}
	return 0, phaseIdle, "", false
}

func (s *SimulationRunner) claimNextSlot(now time.Time) (int, slotPhase, string, bool) {
	s.slotMu.Lock()
	defer s.slotMu.Unlock()

	totalCount := len(s.slots) - 1
	if totalCount <= 0 {
		return 0, phaseIdle, "", false
	}

	s.dispatchSeq++
	if idx, ph, appID, ok := s.claimMaturedApprovalLocked(now, totalCount); ok {
		return idx, ph, appID, true
	}
	return s.claimIdleChatSlotLocked(now, totalCount)
}

func (s *SimulationRunner) executeNextTurn(ctx context.Context, prompt string, reviewDelay, idleDelay time.Duration) (bool, string, string, error) {
	now := time.Now()
	if checker, ok := s.target.(SeatLoadChecker); ok && !checker.CanAcquireSimulatedSeat() {
		return false, "", "", nil
	}

	slotIndex, phase, approvalID, ok := s.claimNextSlot(now)
	if !ok {
		return false, "", "", nil
	}

	return s.runClaimedTurn(ctx, slotIndex, phase, approvalID, prompt, reviewDelay, idleDelay)
}

func (s *SimulationRunner) runClaimedTurn(ctx context.Context, slotIndex int, phase slotPhase, approvalID, prompt string, _ /* reviewDelay */, idleDelay time.Duration) (bool, string, string, error) {
	attendeeID := fmt.Sprintf("attendee-%02d", slotIndex)
	token := s.target.MintToken(attendeeID)

	if phase == phaseApproveInFlight {
		err := s.sendApprove(ctx, attendeeID, token, approvalID)
		s.slotMu.Lock()
		if slotIndex < len(s.slots) {
			s.slots[slotIndex].phase = phaseIdle
			delete(s.containerApprovals, slotIndex)
			s.slots[slotIndex].hasContainerApproval = false
			s.slots[slotIndex].readyAtUnixMs = time.Now().Add(idleDelay).UnixMilli()
			if err == nil {
				s.pushRecentIdleLocked(slotIndex)
			}
		}
		s.slotMu.Unlock()
		return true, attendeeID, "approve", err
	}

	if prompt == "" {
		prompt = simulationPrompts[slotIndex%len(simulationPrompts)]
	}
	chatMsg, err := s.sendChat(ctx, attendeeID, token, prompt)
	action := "chat"
	if err == nil && chatMsg != nil && chatMsg.Approval != nil && chatMsg.Approval.ApprovalID != "" {
		s.slotMu.Lock()
		if slotIndex < len(s.slots) {
			s.slots[slotIndex].phase = phaseApproveInFlight
		}
		s.slotMu.Unlock()
		err = s.sendApprove(ctx, attendeeID, token, chatMsg.Approval.ApprovalID)
		action = "turn"
	}

	s.slotMu.Lock()
	if slotIndex < len(s.slots) {
		s.slots[slotIndex].phase = phaseIdle
		delete(s.containerApprovals, slotIndex)
		s.slots[slotIndex].hasContainerApproval = false
		backoff := idleDelay
		if err != nil {
			backoff = min(idleDelay, 2*time.Second)
		}
		s.slots[slotIndex].readyAtUnixMs = time.Now().Add(backoff).UnixMilli()
		if err == nil {
			s.pushRecentIdleLocked(slotIndex)
		}
	}
	s.slotMu.Unlock()
	return true, attendeeID, action, err
}

func (s *SimulationRunner) runStridedWorker(ctx context.Context, wg *sync.WaitGroup, workerIdx int, rDelay, lDelay time.Duration) {
	defer wg.Done()

	if workerIdx > 0 {
		stagger := time.Duration((workerIdx%64)*40) * time.Millisecond
		if rDelay < 100*time.Millisecond {
			stagger = time.Duration((workerIdx%8)*2) * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(stagger):
		}
	}

	promptIdx := workerIdx % len(simulationPrompts)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		prompt := simulationPrompts[promptIdx]
		promptIdx = (promptIdx + 1) % len(simulationPrompts)
		reviewDelay := s.calculateReviewDelay(workerIdx, rDelay)
		idleDelay := s.calculateInterTurnDelay(lDelay)

		executed, _, _, err := s.executeNextTurn(ctx, prompt, reviewDelay, idleDelay)
		if !executed || err != nil {
			wait := 50 * time.Millisecond
			if rDelay < 100*time.Millisecond {
				wait = 5 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

func (s *SimulationRunner) runAttendee(ctx context.Context, wg *sync.WaitGroup, attendeeID string, offset int, rDelay, lDelay time.Duration) {
	defer wg.Done()

	if s.isReserved(attendeeID, offset+1) {
		return
	}
	token := s.target.MintToken(attendeeID)

	// Stagger attendee startup slightly to distribute requests
	if offset > 0 {
		stagger := time.Duration((offset%20)*150) * time.Millisecond
		if rDelay < 100*time.Millisecond {
			stagger = time.Duration((offset%5)*5) * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(stagger):
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
			if rDelay > 0 && rDelay < 100*time.Millisecond {
				select {
				case <-ctx.Done():
					return
				case <-time.After(rDelay):
				}
			}
			_ = s.sendApprove(ctx, attendeeID, token, chatMsg.Approval.ApprovalID)
		}

		sleepDur := s.calculateInterTurnDelay(lDelay)
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
		Mock:    true,
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
	s.target.Handler().ServeHTTP(rec, req)

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
		Mock:       true,
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
	s.target.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(res.Body)
		if len(respBody) > 0 {
			return fmt.Errorf("approve request failed with status %d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
		}
		return fmt.Errorf("approve request failed with status %d", res.StatusCode)
	}
	var approveMsg types.ChatMessage
	if err := json.NewDecoder(res.Body).Decode(&approveMsg); err == nil {
		if approveMsg.Execution != nil && approveMsg.Execution.ExitCode != 0 {
			return fmt.Errorf("sandbox execution failed (exit %d): %s", approveMsg.Execution.ExitCode, approveMsg.Execution.Stderr)
		}
	}
	return nil
}

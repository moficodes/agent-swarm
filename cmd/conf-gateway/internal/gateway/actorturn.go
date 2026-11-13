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
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorproxy"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

type turnStartCtxKey struct{}

type turnExecState struct {
	startedAt      time.Time
	asyncThinkHold time.Duration
}

func (g *Gateway) beginHyperTurnActivity(attendeeID string, act types.Activity) func() {
	if g.board == nil || !g.board.IsHyperMode() || g.control == nil {
		g.setActivity(attendeeID, act)
		return func() {}
	}
	g.board.BeginHyperResume(attendeeID, act)
	t := time.AfterFunc(1000*time.Millisecond, func() {
		if g.ctx != nil && g.ctx.Err() != nil {
			return
		}
		g.board.PromoteHyperResume(attendeeID)
	})
	return func() { t.Stop() }
}

func (g *Gateway) canFallbackApprovalToActor(reqCtx context.Context, attendeeID string) bool {
	if g.control == nil || g.board == nil {
		return false
	}
	if g.proxy != nil && g.proxy.PendingApproval(attendeeID) != nil {
		// Fast path: gateway already holds the pending approval in memory, so
		// ClaimApprovalWithFallback will not use actor fallback anyway.
		return false
	}
	if g.maxActors > actorstate.HyperFleetThreshold && !isReservedHumanSlotID(attendeeID, g.maxActors, g.HumanSlotStride()) && !g.IsLeased(attendeeID) {
		return true
	}
	v, ok := g.board.View(attendeeID)
	if ok && ((!v.NeverStarted && !strings.Contains(v.SnapshotURI, "/tags/")) || v.WorkerPod != "" || v.Activity == types.ActivityAwaitingApproval) {
		return true
	}
	if g.maxActors > actorstate.HyperFleetThreshold && (isReservedHumanSlotID(attendeeID, g.maxActors, g.HumanSlotStride()) || g.IsLeased(attendeeID)) {
		if getter, hasGet := g.control.(actorstate.SingleActorGetter); hasGet {
			parentCtx := reqCtx
			if parentCtx == nil {
				parentCtx = g.ctx
			}
			ctx, cancel := context.WithTimeout(parentCtx, 2*time.Second)
			defer cancel()
			if s, err := getter.Get(ctx, attendeeID); err == nil {
				uri := s.SnapshotURI
				if s.InProgressSnapshotURI != "" {
					uri = s.InProgressSnapshotURI
				}
				return uri != "" && !strings.Contains(uri, "/tags/")
			}
		}
	}
	return false
}

func (g *Gateway) proxyToActor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	attendeeID, err := g.authenticateRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if r.URL.Path == "/api/chat" && !g.isPresenter(r) && !g.chatLimiter.allow(attendeeID) {
		http.Error(w, fmt.Sprintf("chat rate limit exceeded (max %d requests per minute)", g.chatRateLimitRPM), http.StatusTooManyRequests)
		return
	}

	var useMock bool
	var sampled bool
	if r.URL.Path == "/api/chat" && r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			var chatReq types.ChatRequest
			if err := json.Unmarshal(bodyBytes, &chatReq); err == nil {
				useMock = chatReq.Mock
				if chatReq.Message != "" {
					userMsg := types.ChatMessage{
						ID:        fmt.Sprintf("user-%d", time.Now().UnixNano()),
						Sender:    types.SenderUser,
						Content:   chatReq.Message,
						Timestamp: time.Now().Unix(),
					}
					actionID := fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())
					sampled = g.shouldRecordAction(attendeeID, actionID, true)
					if !useMock || sampled || g.maxActors <= actorstate.HyperFleetThreshold {
						g.mu.Lock()
						g.appendChatHistoryLocked(attendeeID, userMsg)
						g.currentActionID[attendeeID] = actionID
						g.mu.Unlock()
					}
					if sampled {
						g.RecordActionEntry(types.ActionLogEntry{
							ID:         actionID,
							AttendeeID: attendeeID,
							Timestamp:  time.Now().Unix(),
							Prompt:     chatReq.Message,
							Activity:   types.ActivityThinking,
						})
					}
				}
			}
		}
	}
	wasUnseatedSim := useMock && g.board != nil && g.board.IsHyperMode() && g.currentView(attendeeID).WorkerPod == ""
	if r.URL.Path == "/api/approve" {
		apprReq, release, ok := g.proxy.ClaimApprovalWithFallback(w, r, attendeeID, g.canFallbackApprovalToActor(r.Context(), attendeeID))
		if !ok {
			return
		}
		defer release()
		useMock = apprReq.Mock
		wasUnseatedSim = useMock && g.board != nil && g.board.IsHyperMode() && g.currentView(attendeeID).WorkerPod == ""
		g.mu.RLock()
		actionID := g.currentActionID[attendeeID]
		g.mu.RUnlock()
		if actionID == "" {
			actionID = fmt.Sprintf("act-appr-%s-%d", attendeeID, time.Now().UnixNano())
		}
		sampled = g.shouldRecordAction(attendeeID, actionID, false)
		entry := types.ActionLogEntry{
			ID:         actionID,
			AttendeeID: attendeeID,
			Timestamp:  time.Now().Unix(),
			ApprovalID: apprReq.ApprovalID,
			Decision:   apprReq.Decision,
			Code:       apprReq.Code,
			Language:   apprReq.Language,
			WorkerPod:  g.currentView(attendeeID).WorkerPod,
		}
		if apprReq.Decision == types.DecisionApprove {
			entry.Activity = types.ActivityExecuting
			stopPromote := g.beginHyperTurnActivity(attendeeID, types.ActivityExecuting)
			defer stopPromote()
		}
		if sampled {
			g.RecordActionEntry(entry)
		}
	} else if r.URL.Path != "/api/chat" {
		sampled = true
	}
	if useMock && g.board != nil {
		g.board.SetSource(attendeeID, types.SourceMock)
	}
	stopChatPromote := func() {}
	if r.URL.Path != "/api/approve" {
		stopChatPromote = g.beginHyperTurnActivity(attendeeID, types.ActivityThinking)
	}
	if !useMock && (isReservedHumanSlotID(attendeeID, g.maxActors, g.HumanSlotStride()) || g.IsLeased(attendeeID)) {
		g.LeaseAttendee(attendeeID)
	}
	isApproveTurn := r.URL.Path == "/api/approve"
	turnSeq := g.beginTurn(attendeeID)
	execState := &turnExecState{startedAt: time.Now()}
	defer func() {
		if execState.asyncThinkHold > 0 {
			hold := execState.asyncThinkHold
			time.AfterFunc(hold, func() {
				stopChatPromote()
				g.mu.RLock()
				sameTurn := !g.closed && g.turnSeq[attendeeID] == turnSeq
				g.mu.RUnlock()
				if sameTurn && (g.ctx == nil || g.ctx.Err() == nil) {
					if sr := g.SimulationRunner(); sr == nil {
						g.setActivity(attendeeID, types.ActivityAwaitingApproval)
					} else if running, _ := sr.Status(); running {
						g.setActivity(attendeeID, types.ActivityAwaitingApproval)
					}
				}
				g.endTurnWithOpts(attendeeID, useMock, isApproveTurn)
			})
			return
		}
		stopChatPromote()
		g.endTurnWithOpts(attendeeID, useMock, isApproveTurn)
	}()
	g.settleHistoryClear(r.Context(), attendeeID)

	if sampled {
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Actor %s executing %s", attendeeID, r.URL.Path))
		g.mu.Unlock()
	}

	r = r.WithContext(context.WithValue(r.Context(), turnStartCtxKey{}, execState))
	if wasUnseatedSim {
		g.unseatedSimInFlight.Add(1)
		defer g.unseatedSimInFlight.Add(-1)
	}
	g.proxy.ServeHTTP(w, actorproxy.WithMock(actorproxy.WithAttendee(r, attendeeID), useMock))
}

func (g *Gateway) appendChatHistoryLocked(attendeeID string, msg types.ChatMessage) {
	history := append(g.chatHistories[attendeeID], msg)
	if g.maxActors > 256 && len(history) > 6 {
		history = append([]types.ChatMessage(nil), history[len(history)-6:]...)
	}
	g.chatHistories[attendeeID] = history
}

func (g *Gateway) shouldRecordAction(attendeeID, actionID string, isStart bool) bool {
	if g.maxActors <= 256 || attendeeID == "attendee-01" || g.IsLeased(attendeeID) {
		return true
	}
	g.mu.RLock()
	sc := g.simController
	g.mu.RUnlock()
	if sc == nil {
		return true
	}
	if running, count := sc.Status(); !running || count <= 256 {
		return true
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sampledActions[actionID] {
		return true
	}
	if !isStart {
		return false
	}
	now := time.Now()
	if now.Sub(g.lastActionSample) < 80*time.Millisecond {
		return false
	}
	g.lastActionSample = now
	if len(g.sampledActions) > 512 {
		clear(g.sampledActions)
	}
	g.sampledActions[actionID] = true
	return true
}

// onProxyError records a router that could not be reached.
func (g *Gateway) onProxyError(attendeeID string, err error) {
	if g.proxy != nil && g.proxy.PendingApproval(attendeeID) != nil {
		g.setActivity(attendeeID, types.ActivityAwaitingApproval)
	} else {
		g.setActivity(attendeeID, types.ActivityIdle)
	}
	g.mu.Lock()
	g.addEventLocked(fmt.Sprintf("Router proxy error for %s: %v", attendeeID, err))
	g.mu.Unlock()
}

func (g *Gateway) mockTurnDelay(minD, maxD time.Duration) time.Duration {
	if maxD <= 0 {
		return 0
	}
	if minD <= 0 || maxD <= minD {
		return maxD
	}
	return minD + time.Duration(rand.Int64N(int64(maxD-minD)))
}

// onActorReply records an actor's answer to a turn: its activity, chat
// history, pending approval, and the action feed entry.
func (g *Gateway) onActorReply(attendeeID string, status int, msg *types.ChatMessage) {
	g.onActorReplyWithContext(g.ctx, attendeeID, status, msg)
}

func (g *Gateway) onActorReplyWithContext(reqCtx context.Context, attendeeID string, status int, msg *types.ChatMessage) {
	var isWaitingForApproval bool
	var chatMsg types.ChatMessage
	hasChatMsg := msg != nil
	if hasChatMsg {
		chatMsg = *msg
		if chatMsg.Approval != nil {
			isWaitingForApproval = true
		}
	}

	var execState *turnExecState
	if reqCtx != nil {
		execState, _ = reqCtx.Value(turnStartCtxKey{}).(*turnExecState)
	}

	if hasChatMsg && chatMsg.Source == types.SourceMock && isWaitingForApproval {
		if reqCtx != nil && reqCtx.Err() != nil {
			g.setActivity(attendeeID, types.ActivityIdle)
			return
		}
		rawHold := g.mockTurnDelay(g.mockThinkMin, g.mockThinkMax)
		if g.maxActors > actorstate.HyperFleetThreshold && rawHold > time.Second && execState != nil && !g.IsLeased(attendeeID) && !isReservedHumanSlotID(attendeeID, g.maxActors, g.HumanSlotStride()) {
			targetHold := 2500*time.Millisecond + time.Duration(rand.Int64N(int64(2000*time.Millisecond)))
			if !execState.startedAt.IsZero() {
				targetHold -= time.Since(execState.startedAt)
			}
			if targetHold > 0 {
				execState.asyncThinkHold = targetHold
			}
		} else {
			hold := rawHold
			if execState != nil && !execState.startedAt.IsZero() {
				hold -= time.Since(execState.startedAt)
			}
			if hold > 0 {
				var reqDone <-chan struct{}
				if reqCtx != nil {
					reqDone = reqCtx.Done()
				}
				var gwDone <-chan struct{}
				if g.ctx != nil {
					gwDone = g.ctx.Done()
				}
				timer := time.NewTimer(hold)
				select {
				case <-timer.C:
				case <-reqDone:
					timer.Stop()
					g.setActivity(attendeeID, types.ActivityIdle)
					return
				case <-gwDone:
					timer.Stop()
				}
			}
		}
	}

	if hasChatMsg {
		g.board.SetSource(attendeeID, chatMsg.Source)
		if isWaitingForApproval {
			if execState == nil || execState.asyncThinkHold <= 0 {
				g.setActivity(attendeeID, types.ActivityAwaitingApproval)
			}
		} else if chatMsg.Execution != nil && g.control != nil && g.suspendAfter > 0 && g.board != nil && g.board.IsHyperMode() {
			// Keep ActivityExecuting visible during the SUSPEND_AFTER window until
			// BeginHyperSuspend transitions the actor to StateSuspending.
			g.setActivity(attendeeID, types.ActivityExecuting)
		} else {
			g.setActivity(attendeeID, types.ActivityIdle)
		}
	} else {
		g.setActivity(attendeeID, types.ActivityIdle)
	}
	workerPod := g.currentView(attendeeID).WorkerPod

	g.mu.RLock()
	actionID := g.currentActionID[attendeeID]
	g.mu.RUnlock()
	if actionID == "" {
		actionID = fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())
	}
	sampled := g.shouldRecordAction(attendeeID, actionID, false)
	isHumanOrSampled := chatMsg.Source != types.SourceMock || sampled || g.maxActors <= actorstate.HyperFleetThreshold || attendeeID == "attendee-01" || g.IsLeased(attendeeID) || isReservedHumanSlotID(attendeeID, g.maxActors, g.HumanSlotStride())

	var newExec types.ExecutionResult
	hasNewExec := hasChatMsg && chatMsg.Execution != nil
	if hasNewExec {
		newExec = *chatMsg.Execution
		// The agent already proposed a fix for a corrected run, so the
		// spotlight keeps what it showed.
		if !newExec.Corrected && isHumanOrSampled {
			g.mu.Lock()
			if g.maxActors > actorstate.HyperFleetThreshold && len(g.lastResults) >= 2048 {
				clear(g.lastResults)
			}
			g.lastResults[attendeeID] = &newExec
			g.mu.Unlock()
		}
	}

	if isWaitingForApproval {
		if hasNewExec && newExec.Corrected {
			// The failed run closes the approved action; the fix opens its own,
			// so it does not overwrite the approved card.
			if sampled {
				g.RecordActionEntry(types.ActionLogEntry{
					ID:         actionID,
					AttendeeID: attendeeID,
					Timestamp:  time.Now().Unix(),
					Execution:  &newExec,
					WorkerPod:  workerPod,
					Activity:   types.ActivityIdle,
				})
			}
			actionID = fmt.Sprintf("act-fix-%s-%d", attendeeID, time.Now().UnixNano())
			if isHumanOrSampled {
				g.mu.Lock()
				g.currentActionID[attendeeID] = actionID
				if sampled {
					g.sampledActions[actionID] = true
				}
				g.mu.Unlock()
			}
		}

		g.mu.Lock()
		if g.control == nil || isHumanOrSampled {
			g.proxy.SetPending(attendeeID, chatMsg.Approval)
		}
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") && isHumanOrSampled {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.appendChatHistoryLocked(attendeeID, chatMsg)
			}
		}
		if sampled {
			g.addEventLocked(fmt.Sprintf("Actor %s awaiting human approval", attendeeID))
		}
		g.mu.Unlock()

		if sampled {
			g.RecordActionEntry(types.ActionLogEntry{
				ID:            actionID,
				AttendeeID:    attendeeID,
				Timestamp:     time.Now().Unix(),
				ModelResponse: chatMsg.Content,
				Code:          chatMsg.Approval.Code,
				Language:      chatMsg.Approval.Language,
				RiskLevel:     chatMsg.Approval.RiskLevel,
				ApprovalID:    chatMsg.Approval.ApprovalID,
				WorkerPod:     workerPod,
				Activity:      types.ActivityAwaitingApproval,
				Source:        chatMsg.Source,
			})
		}
	} else if status == http.StatusOK {
		g.mu.Lock()
		delete(g.currentActionID, attendeeID)
		if g.control == nil || isHumanOrSampled {
			g.proxy.ClearPending(attendeeID)
		}
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") && isHumanOrSampled {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.appendChatHistoryLocked(attendeeID, chatMsg)
			}
		}
		if sampled {
			g.addEventLocked(fmt.Sprintf("Actor %s completed turn", attendeeID))
			delete(g.sampledActions, actionID)
		}
		g.mu.Unlock()

		if sampled {
			if hasNewExec {
				entry := types.ActionLogEntry{
					ID:         actionID,
					AttendeeID: attendeeID,
					Timestamp:  time.Now().Unix(),
					Execution:  &newExec,
					WorkerPod:  workerPod,
					Source:     chatMsg.Source,
				}
				if hasChatMsg && chatMsg.Content != "" {
					entry.ModelResponse = chatMsg.Content
				}
				g.RecordActionEntry(entry)
			} else if hasChatMsg {
				g.RecordActionEntry(types.ActionLogEntry{
					ID:            actionID,
					AttendeeID:    attendeeID,
					Timestamp:     time.Now().Unix(),
					ModelResponse: chatMsg.Content,
					WorkerPod:     workerPod,
					Source:        chatMsg.Source,
				})
			}
		}
	}
}

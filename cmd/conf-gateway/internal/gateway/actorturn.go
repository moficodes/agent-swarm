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

func (g *Gateway) canFallbackApprovalToActor(reqCtx context.Context, attendeeID string) bool {
	if g.control == nil || g.board == nil {
		return false
	}
	if g.proxy != nil && g.proxy.PendingApproval(attendeeID) != nil {
		// Fast path: gateway already holds the pending approval in memory, so
		// ClaimApprovalWithFallback will not use actor fallback anyway.
		return false
	}
	v, ok := g.board.View(attendeeID)
	if ok && !v.NeverStarted && !strings.Contains(v.SnapshotURI, "/tags/") {
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
	} else if r.URL.Path == "/api/approve" {
		apprReq, release, ok := g.proxy.ClaimApprovalWithFallback(w, r, attendeeID, g.canFallbackApprovalToActor(r.Context(), attendeeID))
		if !ok {
			return
		}
		defer release()
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
			g.setActivity(attendeeID, types.ActivityExecuting)
		}
		if sampled {
			g.RecordActionEntry(entry)
		}
	} else {
		sampled = true
	}
	if r.URL.Path != "/api/approve" {
		g.setActivity(attendeeID, types.ActivityThinking)
	}
	if !useMock && (isReservedHumanSlotID(attendeeID, g.maxActors, g.HumanSlotStride()) || g.IsLeased(attendeeID)) {
		g.LeaseAttendee(attendeeID)
	}
	g.beginTurn(attendeeID)
	defer g.endTurn(attendeeID)
	g.settleHistoryClear(r.Context(), attendeeID)

	if sampled {
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Actor %s executing %s", attendeeID, r.URL.Path))
		g.mu.Unlock()
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
	g.setActivity(attendeeID, types.ActivityIdle)
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

	if hasChatMsg && chatMsg.Source == types.SourceMock {
		if reqCtx != nil && reqCtx.Err() != nil {
			g.setActivity(attendeeID, types.ActivityIdle)
			return
		}
		var hold time.Duration
		if isWaitingForApproval {
			hold = g.mockTurnDelay(g.mockThinkMin, g.mockThinkMax)
		} else {
			hold = g.mockTurnDelay(g.mockExecMin, g.mockExecMax)
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

	if hasChatMsg {
		g.board.SetSource(attendeeID, chatMsg.Source)
		if isWaitingForApproval {
			g.setActivity(attendeeID, types.ActivityAwaitingApproval)
		} else {
			g.setActivity(attendeeID, types.ActivityIdle)
		}
	} else {
		g.setActivity(attendeeID, types.ActivityIdle)
	}
	workerPod := g.currentView(attendeeID).WorkerPod

	var newExec types.ExecutionResult
	hasNewExec := hasChatMsg && chatMsg.Execution != nil
	if hasNewExec {
		newExec = *chatMsg.Execution
		// The agent already proposed a fix for a corrected run, so the
		// spotlight keeps what it showed.
		if !newExec.Corrected {
			g.mu.Lock()
			g.lastResults[attendeeID] = &newExec
			g.mu.Unlock()
		}
	}

	g.mu.RLock()
	actionID := g.currentActionID[attendeeID]
	g.mu.RUnlock()
	if actionID == "" {
		actionID = fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())
	}
	sampled := g.shouldRecordAction(attendeeID, actionID, false)

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
			g.mu.Lock()
			g.currentActionID[attendeeID] = actionID
			if sampled {
				g.sampledActions[actionID] = true
			}
			g.mu.Unlock()
		}

		g.mu.Lock()
		g.proxy.SetPending(attendeeID, chatMsg.Approval)
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") && (chatMsg.Source != types.SourceMock || sampled || g.maxActors <= actorstate.HyperFleetThreshold) {
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
		g.proxy.ClearPending(attendeeID)
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") && (chatMsg.Source != types.SourceMock || sampled || g.maxActors <= actorstate.HyperFleetThreshold) {
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

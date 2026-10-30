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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorproxy"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

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
					g.mu.Lock()
					g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], userMsg)
					g.currentActionID[attendeeID] = actionID
					g.mu.Unlock()
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
	} else if r.URL.Path == "/api/approve" {
		apprReq, release, ok := g.proxy.ClaimApproval(w, r, attendeeID)
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
		g.RecordActionEntry(entry)
	}
	if r.URL.Path != "/api/approve" {
		g.setActivity(attendeeID, types.ActivityThinking)
	}
	g.beginTurn(attendeeID)
	defer g.endTurn(attendeeID)
	g.settleHistoryClear(r.Context(), attendeeID)

	g.mu.Lock()
	g.addEventLocked(fmt.Sprintf("Actor %s executing %s", attendeeID, r.URL.Path))
	g.mu.Unlock()

	g.proxy.ServeHTTP(w, actorproxy.WithMock(actorproxy.WithAttendee(r, attendeeID), useMock))
}

// onProxyError records a router that could not be reached.
func (g *Gateway) onProxyError(attendeeID string, err error) {
	g.setActivity(attendeeID, types.ActivityIdle)
	g.mu.Lock()
	g.addEventLocked(fmt.Sprintf("Router proxy error for %s: %v", attendeeID, err))
	g.mu.Unlock()
}

// onActorReply records an actor's answer to a turn: its activity, chat
// history, pending approval, and the action feed entry.
func (g *Gateway) onActorReply(attendeeID string, status int, msg *types.ChatMessage) {
	var isWaitingForApproval bool
	var chatMsg types.ChatMessage
	hasChatMsg := msg != nil
	if hasChatMsg {
		chatMsg = *msg
		if chatMsg.Approval != nil {
			isWaitingForApproval = true
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

	if isWaitingForApproval {
		if hasNewExec && newExec.Corrected {
			// The failed run closes the approved action; the fix opens its own,
			// so it does not overwrite the approved card.
			g.RecordActionEntry(types.ActionLogEntry{
				ID:         actionID,
				AttendeeID: attendeeID,
				Timestamp:  time.Now().Unix(),
				Execution:  &newExec,
				WorkerPod:  workerPod,
				Activity:   types.ActivityIdle,
			})
			actionID = fmt.Sprintf("act-fix-%s-%d", attendeeID, time.Now().UnixNano())
			g.mu.Lock()
			g.currentActionID[attendeeID] = actionID
			g.mu.Unlock()
		}

		g.mu.Lock()
		g.proxy.SetPending(attendeeID, chatMsg.Approval)
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], chatMsg)
			}
		}
		g.addEventLocked(fmt.Sprintf("Actor %s awaiting human approval", attendeeID))
		g.mu.Unlock()

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
	} else if status == http.StatusOK {
		g.mu.Lock()
		g.proxy.ClearPending(attendeeID)
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], chatMsg)
			}
		}
		g.addEventLocked(fmt.Sprintf("Actor %s completed turn", attendeeID))
		g.mu.Unlock()

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

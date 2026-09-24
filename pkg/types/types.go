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

package types

import (
	"errors"
	"fmt"
)

// SenderType represents the identity or role of a chat message sender.
type SenderType string

const (
	// SenderUser indicates a human participant.
	SenderUser SenderType = "user"
	// SenderAssistant indicates the AI orchestrator or agent.
	SenderAssistant SenderType = "assistant"
	// SenderSystem indicates a system notification or stage status event.
	SenderSystem SenderType = "system"
)

// RiskLevel represents the assessed danger or sensitivity of proposed code execution.
type RiskLevel string

const (
	// RiskLow denotes low-risk operations, such as pure data inspection.
	RiskLow RiskLevel = "LOW"
	// RiskMedium denotes medium-risk operations.
	RiskMedium RiskLevel = "MEDIUM"
	// RiskHigh denotes high-risk operations requiring explicit human approval.
	RiskHigh RiskLevel = "HIGH"
)

// DecisionType represents the human approval decision for a pending execution.
type DecisionType string

const (
	// DecisionApprove allows the pending execution to proceed.
	DecisionApprove DecisionType = "APPROVE"
	// DecisionReject halts or denies the pending execution.
	DecisionReject DecisionType = "REJECT"
)

// ModelKeyHeader carries an actor's per-actor key from the gateway to the
// actor on every proxied request. The actor accepts gateway-only calls, such
// as ClearHistory, only with the key it last received.
const ModelKeyHeader = "X-Conf-Model-Key"

// ClearHistoryPath is the actor endpoint the gateway POSTs to, through the
// router, to drop the actor's conversation history and pending approval.
const ClearHistoryPath = "/api/history/clear"

// ModelResultPath is the actor endpoint the gateway POSTs a ModelResult to,
// through the router.
const ModelResultPath = "/api/model-result"

// ModelCall is a Vertex AI generateContent request an actor hands to the
// gateway, which makes the call because actors have no egress. Path is the
// Vertex path, starting at the API version.
type ModelCall struct {
	CallID string `json:"call_id"`
	Path   string `json:"path"`
	Body   []byte `json:"body"`
}

// ModelCallStep is an actor's answer, in place of a ChatMessage, when its
// turn needs the gateway to make a model call.
type ModelCallStep struct {
	ModelCall *ModelCall `json:"model_call"`
}

// ModelResult is the gateway's answer to a ModelCall: Vertex AI's status and
// body, or the gateway's own refusal.
type ModelResult struct {
	CallID string `json:"call_id"`
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

// ReplySource says which engine produced an assistant reply.
type ReplySource string

const (
	// SourceGemini is a reply from Gemini, with the gateway making the calls.
	SourceGemini ReplySource = "gemini"
	// SourceMock is a reply from the offline mock engine by configuration.
	SourceMock ReplySource = "mock"
	// SourceMockFallback is a mock reply used because the Gemini call failed.
	SourceMockFallback ReplySource = "mock-fallback"
)

// SubstrateState is an actor's state as reported by ateapi, without the
// ACTOR_STATE_ prefix. StateUnavailable means the gateway could not read it.
type SubstrateState string

const (
	StateUnknown     SubstrateState = "UNKNOWN"
	StateResuming    SubstrateState = "RESUMING"
	StateRunning     SubstrateState = "RUNNING"
	StateSuspending  SubstrateState = "SUSPENDING"
	StateSuspended   SubstrateState = "SUSPENDED"
	StatePausing     SubstrateState = "PAUSING"
	StatePaused      SubstrateState = "PAUSED"
	StateCrashed     SubstrateState = "CRASHED"
	StateDeleting    SubstrateState = "DELETING"
	StateReverting   SubstrateState = "REVERTING"
	StateUnavailable SubstrateState = "UNAVAILABLE"
)

// Activity is what the gateway knows the actor is doing for its attendee.
type Activity string

const (
	ActivityIdle             Activity = ""
	ActivityThinking         Activity = "THINKING"
	ActivityAwaitingApproval Activity = "AWAITING_APPROVAL"
	ActivityExecuting        Activity = "EXECUTING"
)

// ActorView is everything the UI shows about one actor's state.
type ActorView struct {
	ActorID      string         `json:"actor_id"`
	State        SubstrateState `json:"state"`
	NeverStarted bool           `json:"never_started"`
	StateDetail  string         `json:"state_detail,omitempty"`
	Activity     Activity       `json:"activity,omitempty"`
	WorkerPod    string         `json:"worker_pod,omitempty"`
	SnapshotURI  string         `json:"snapshot_uri,omitempty"`
	Source       ReplySource    `json:"source,omitempty"`
}

// ApprovalRequest contains the details and security context of an execution requiring approval.
type ApprovalRequest struct {
	ApprovalID string    `json:"approval_id"`
	Summary    string    `json:"summary"`
	Code       string    `json:"code"`
	Language   string    `json:"language"`
	RiskLevel  RiskLevel `json:"risk_level"`
}

// ApprovalResponse represents a human operator's decision on a pending approval.
type ApprovalResponse struct {
	ApprovalID string       `json:"approval_id"`
	Decision   DecisionType `json:"decision"`
	Code       string       `json:"code,omitempty"`
	Language   string       `json:"language,omitempty"`
}

// Validate checks whether the approval response is well-formed.
func (r *ApprovalResponse) Validate() error {
	if r == nil {
		return errors.New("approval response is nil")
	}
	if r.ApprovalID == "" {
		return errors.New("approval_id is required")
	}
	if r.Decision != DecisionApprove && r.Decision != DecisionReject {
		return fmt.Errorf("invalid decision: %s", r.Decision)
	}
	return nil
}

// ChatMessage represents a single message in the conference chat thread.
type ChatMessage struct {
	ID        string           `json:"id"`
	Sender    SenderType       `json:"sender"`
	Content   string           `json:"content"`
	Timestamp int64            `json:"timestamp"`
	Approval  *ApprovalRequest `json:"approval,omitempty"`
	Source    ReplySource      `json:"source,omitempty"`
	Execution *ExecutionResult `json:"execution,omitempty"`
}

// ChatRequest is the payload sent to initiate a chat or task request.
type ChatRequest struct {
	Message string `json:"message"`
}

// ExecutionResult represents the output from running code in an isolated sandbox.
type ExecutionResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

// StageTelemetry is the stage view of the router, workers and actors.
type StageTelemetry struct {
	ParkedRequests   int                  `json:"parked_requests"`
	ParkingAvailable bool                 `json:"parking_available"`
	Workers          []WorkerStatus       `json:"workers"`
	Actors           map[string]ActorView `json:"actors"`
	RecentEvents     []string             `json:"recent_events"`
	MockLLM          bool                 `json:"mock_llm"`
	ModelUsage       ModelUsage           `json:"model_usage"`
	StatePollMs      int64                `json:"state_poll_ms"`
}

// WorkerStatus is one worker pod and the actor ateapi reports running on it.
type WorkerStatus struct {
	PodName     string         `json:"pod_name"`
	ActiveActor string         `json:"active_actor"`
	State       SubstrateState `json:"state"`
}

// ActorDetails is everything the inspector drawer shows about one actor.
type ActorDetails struct {
	ActorView
	ChatHistory     []ChatMessage    `json:"chat_history"`
	PendingApproval *ApprovalRequest `json:"pending_approval,omitempty"`
	LastResult      *ExecutionResult `json:"last_result,omitempty"`
	Usage           ModelUsage       `json:"usage"`
}

// SimulationRequest specifies actions to control the virtual attendee swarm.
type SimulationRequest struct {
	Action string `json:"action"` // "start" or "stop"
	Count  int    `json:"count,omitempty"`
}

// Validate checks whether the simulation request is valid.
func (s *SimulationRequest) Validate() error {
	if s == nil {
		return errors.New("simulation request is nil")
	}
	if s.Action != "start" && s.Action != "stop" {
		return fmt.Errorf("invalid action: %s (expected 'start' or 'stop')", s.Action)
	}
	if s.Action == "start" && (s.Count < 1 || s.Count > 100) {
		return fmt.Errorf("invalid count: %d (must be between 1 and 100)", s.Count)
	}
	return nil
}

// ModelUsage summarizes one actor's (or the fleet's) Gemini calls made by the gateway.
type ModelUsage struct {
	Calls           int64 `json:"calls"`
	PromptTokens    int64 `json:"prompt_tokens"`
	CandidateTokens int64 `json:"candidate_tokens"`
	TotalLatencyMs  int64 `json:"total_latency_ms"`
	LastLatencyMs   int64 `json:"last_latency_ms"`
}

// ActionLogEntry represents a full conversational or execution event streamed across the fleet.
type ActionLogEntry struct {
	ID            string           `json:"id"`
	AttendeeID    string           `json:"attendee_id"`
	Timestamp     int64            `json:"timestamp"`
	Prompt        string           `json:"prompt"`
	ModelResponse string           `json:"model_response,omitempty"`
	Code          string           `json:"code,omitempty"`
	Language      string           `json:"language,omitempty"`
	RiskLevel     RiskLevel        `json:"risk_level,omitempty"`
	ApprovalID    string           `json:"approval_id,omitempty"`
	Decision      DecisionType     `json:"decision,omitempty"`
	Execution     *ExecutionResult `json:"execution,omitempty"`
	WorkerPod     string           `json:"worker_pod,omitempty"`
	Activity      Activity         `json:"activity,omitempty"`
	Source        ReplySource      `json:"source,omitempty"`
}

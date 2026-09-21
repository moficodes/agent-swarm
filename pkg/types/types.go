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

// ActorLifecycleState represents the lifecycle phase of an interactive agent actor.
type ActorLifecycleState string

const (
	// StateSuspended indicates the actor has no active worker and zero active state.
	StateSuspended ActorLifecycleState = "SUSPENDED"
	// StateParked indicates the actor is checkpointed and awaiting resumption or input.
	StateParked ActorLifecycleState = "PARKED"
	// StateRunning indicates the actor is actively executing on a worker pod.
	StateRunning ActorLifecycleState = "RUNNING"
	// StateAwaitingApproval indicates the actor is paused waiting for human authorization.
	StateAwaitingApproval ActorLifecycleState = "AWAITING_APPROVAL"
)

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

// StageTelemetry represents the operational status of the cluster and running actors.
type StageTelemetry struct {
	ParkedRequests int                            `json:"parked_requests"`
	Workers        []WorkerStatus                 `json:"workers"`
	Actors         map[string]ActorLifecycleState `json:"actors"`
	RecentEvents   []string                       `json:"recent_events"`
}

// WorkerStatus represents the real-time execution state of a worker pod.
type WorkerStatus struct {
	PodName     string `json:"pod_name"`
	ActiveActor string `json:"active_actor"`
	CurrentTask string `json:"current_task"`
	ElapsedMs   int64  `json:"elapsed_ms"`
}

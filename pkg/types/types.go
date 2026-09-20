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

type SenderType string

const (
	SenderUser      SenderType = "user"
	SenderAssistant SenderType = "assistant"
	SenderSystem    SenderType = "system"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "LOW"
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

type DecisionType string

const (
	DecisionApprove DecisionType = "APPROVE"
	DecisionReject  DecisionType = "REJECT"
)

type ActorLifecycleState string

const (
	StateSuspended ActorLifecycleState = "SUSPENDED"
	StateParked    ActorLifecycleState = "PARKED"
	StateRunning   ActorLifecycleState = "RUNNING"
	StateApproving ActorLifecycleState = "AWAITING_APPROVAL"
)

type ApprovalRequest struct {
	ApprovalID string    `json:"approval_id"`
	Summary    string    `json:"summary"`
	Code       string    `json:"code"`
	Language   string    `json:"language"`
	RiskLevel  RiskLevel `json:"risk_level"`
}

type ApprovalResponse struct {
	ApprovalID string       `json:"approval_id"`
	Decision   DecisionType `json:"decision"`
}

func (r *ApprovalResponse) Validate() error {
	if r.ApprovalID == "" {
		return errors.New("approval_id is required")
	}
	if r.Decision != DecisionApprove && r.Decision != DecisionReject {
		return fmt.Errorf("invalid decision: %s", r.Decision)
	}
	return nil
}

type ChatMessage struct {
	ID        string           `json:"id"`
	Sender    SenderType       `json:"sender"`
	Content   string           `json:"content"`
	Timestamp int64            `json:"timestamp"`
	Approval  *ApprovalRequest `json:"approval,omitempty"`
}

type ChatRequest struct {
	Message string `json:"message"`
}

type ExecutionResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Duration int64  `json:"duration_ms"`
}

type StageTelemetry struct {
	ParkedRequests int                            `json:"parked_requests"`
	Workers        []WorkerStatus                 `json:"workers"`
	Actors         map[string]ActorLifecycleState `json:"actors"`
	RecentEvents   []string                       `json:"recent_events"`
}

type WorkerStatus struct {
	PodName     string `json:"pod_name"`
	ActiveActor string `json:"active_actor"`
	CurrentTask string `json:"current_task"`
	ElapsedMs   int64  `json:"elapsed_ms"`
}

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

package types_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func TestMessageSerialization(t *testing.T) {
	t.Run("with approval", func(t *testing.T) {
		msg := types.ChatMessage{
			ID:        "msg-1",
			Sender:    types.SenderAssistant,
			Content:   "Plan created",
			Timestamp: 1789910000,
			Approval: &types.ApprovalRequest{
				ApprovalID: "app-1",
				Summary:    "Execute Python outlier analysis",
				Code:       "import numpy as np\nprint(np.mean([1, 2, 3]))",
				Language:   "python",
				RiskLevel:  types.RiskHigh,
			},
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("failed to marshal ChatMessage: %v", err)
		}

		var decoded types.ChatMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ChatMessage: %v", err)
		}

		if diff := cmp.Diff(msg, decoded); diff != "" {
			t.Fatalf("ChatMessage mismatch (-want +got):\n%s", diff)
		}

		if decoded.ID != msg.ID {
			t.Errorf("expected ID %q, got %q", msg.ID, decoded.ID)
		}
		if decoded.Sender != msg.Sender {
			t.Errorf("expected Sender %q, got %q", msg.Sender, decoded.Sender)
		}
		if decoded.Content != msg.Content {
			t.Errorf("expected Content %q, got %q", msg.Content, decoded.Content)
		}
		if decoded.Timestamp != msg.Timestamp {
			t.Errorf("expected Timestamp %d, got %d", msg.Timestamp, decoded.Timestamp)
		}
		if decoded.Approval == nil {
			t.Fatal("expected non-nil Approval")
		}
		if decoded.Approval.ApprovalID != msg.Approval.ApprovalID {
			t.Errorf("expected ApprovalID %q, got %q", msg.Approval.ApprovalID, decoded.Approval.ApprovalID)
		}
		if decoded.Approval.Summary != msg.Approval.Summary {
			t.Errorf("expected Summary %q, got %q", msg.Approval.Summary, decoded.Approval.Summary)
		}
		if decoded.Approval.Code != msg.Approval.Code {
			t.Errorf("expected Code %q, got %q", msg.Approval.Code, decoded.Approval.Code)
		}
		if decoded.Approval.Language != msg.Approval.Language {
			t.Errorf("expected Language %q, got %q", msg.Approval.Language, decoded.Approval.Language)
		}
		if decoded.Approval.RiskLevel != msg.Approval.RiskLevel {
			t.Errorf("expected RiskLevel %q, got %q", msg.Approval.RiskLevel, decoded.Approval.RiskLevel)
		}
	})

	t.Run("without approval (omitempty)", func(t *testing.T) {
		msg := types.ChatMessage{
			ID:        "msg-2",
			Sender:    types.SenderUser,
			Content:   "Hello from attendee",
			Timestamp: 1789910050,
			Approval:  nil,
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("failed to marshal ChatMessage: %v", err)
		}

		if strings.Contains(string(data), `"approval"`) {
			t.Fatalf("expected approval to be omitted, got %s", string(data))
		}

		var decoded types.ChatMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ChatMessage: %v", err)
		}

		if diff := cmp.Diff(msg, decoded); diff != "" {
			t.Fatalf("ChatMessage mismatch (-want +got):\n%s", diff)
		}
		if decoded.Approval != nil {
			t.Fatalf("expected decoded.Approval to be nil, got %+v", decoded.Approval)
		}
	})
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		resp    *types.ApprovalResponse
		wantErr bool
	}{
		{
			name: "valid APPROVE",
			resp: &types.ApprovalResponse{
				ApprovalID: "app-1",
				Decision:   types.DecisionApprove,
			},
			wantErr: false,
		},
		{
			name: "valid REJECT",
			resp: &types.ApprovalResponse{
				ApprovalID: "app-2",
				Decision:   types.DecisionReject,
			},
			wantErr: false,
		},
		{
			name:    "nil receiver",
			resp:    nil,
			wantErr: true,
		},
		{
			name: "empty approval_id",
			resp: &types.ApprovalResponse{
				ApprovalID: "",
				Decision:   types.DecisionApprove,
			},
			wantErr: true,
		},
		{
			name: "invalid decision",
			resp: &types.ApprovalResponse{
				ApprovalID: "app-3",
				Decision:   "UNKNOWN",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resp.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("ApprovalResponse.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAuxiliaryTypesSerialization(t *testing.T) {
	t.Run("ExecutionResult", func(t *testing.T) {
		exec := types.ExecutionResult{
			Stdout:     "output text",
			Stderr:     "warning text",
			ExitCode:   0,
			DurationMs: 125,
		}

		data, err := json.Marshal(exec)
		if err != nil {
			t.Fatalf("failed to marshal ExecutionResult: %v", err)
		}

		var decoded types.ExecutionResult
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ExecutionResult: %v", err)
		}

		if diff := cmp.Diff(exec, decoded); diff != "" {
			t.Fatalf("ExecutionResult mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("WorkerStatus", func(t *testing.T) {
		status := types.WorkerStatus{
			PodName:     "worker-pod-42",
			ActiveActor: "actor-analysis-1",
			CurrentTask: "running simulation",
			ElapsedMs:   350,
		}

		data, err := json.Marshal(status)
		if err != nil {
			t.Fatalf("failed to marshal WorkerStatus: %v", err)
		}

		var decoded types.WorkerStatus
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal WorkerStatus: %v", err)
		}

		if diff := cmp.Diff(status, decoded); diff != "" {
			t.Fatalf("WorkerStatus mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("StageTelemetry", func(t *testing.T) {
		telemetry := types.StageTelemetry{
			ParkedRequests: 3,
			Workers: []types.WorkerStatus{
				{
					PodName:     "worker-pod-1",
					ActiveActor: "actor-planner",
					CurrentTask: "dispatching subagent",
					ElapsedMs:   120,
				},
			},
			Actors: map[string]types.ActorLifecycleState{
				"actor-planner":  types.StateRunning,
				"actor-executor": types.StateAwaitingApproval,
				"actor-reviewer": types.StateParked,
			},
			RecentEvents: []string{
				"Actor actor-planner resumed",
				"Approval requested for actor-executor",
			},
		}

		data, err := json.Marshal(telemetry)
		if err != nil {
			t.Fatalf("failed to marshal StageTelemetry: %v", err)
		}

		var decoded types.StageTelemetry
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal StageTelemetry: %v", err)
		}

		if diff := cmp.Diff(telemetry, decoded); diff != "" {
			t.Fatalf("StageTelemetry mismatch (-want +got):\n%s", diff)
		}
	})
}

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

	"github.com/agent-substrate/agent-swarm/pkg/types"
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

	t.Run("with source", func(t *testing.T) {
		msg := types.ChatMessage{
			ID:        "msg-3",
			Sender:    types.SenderAssistant,
			Content:   "Falling back to the mock engine",
			Timestamp: 1789910100,
			Source:    types.SourceMockFallback,
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("failed to marshal ChatMessage: %v", err)
		}
		if !strings.Contains(string(data), `"source":"mock-fallback"`) {
			t.Fatalf("expected source in JSON, got %s", string(data))
		}

		var decoded types.ChatMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ChatMessage: %v", err)
		}
		if diff := cmp.Diff(msg, decoded); diff != "" {
			t.Fatalf("ChatMessage mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("with execution", func(t *testing.T) {
		msg := types.ChatMessage{
			ID:        "msg-4",
			Sender:    types.SenderAssistant,
			Content:   "Execution completed in 12ms (exit code 0):\n\nstored\n",
			Timestamp: 1789910200,
			Execution: &types.ExecutionResult{
				Stdout:     "stored\n",
				ExitCode:   0,
				DurationMs: 12,
			},
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("failed to marshal ChatMessage: %v", err)
		}
		if !strings.Contains(string(data), `"execution":`) {
			t.Fatalf("expected execution in JSON, got %s", string(data))
		}

		var decoded types.ChatMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ChatMessage: %v", err)
		}
		if diff := cmp.Diff(msg, decoded); diff != "" {
			t.Fatalf("ChatMessage mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("without approval or execution (omitempty)", func(t *testing.T) {
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
		if strings.Contains(string(data), `"execution"`) {
			t.Fatalf("expected execution to be omitted, got %s", string(data))
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
			State:       types.StateRunning,
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
			ParkedRequests:   3,
			ParkingAvailable: true,
			Workers: []types.WorkerStatus{
				{
					PodName:     "worker-pod-1",
					ActiveActor: "actor-planner",
					State:       types.StateRunning,
				},
			},
			Actors: map[string]types.ActorView{
				"actor-planner":  {ActorID: "actor-planner", State: types.StateRunning, WorkerPod: "worker-pod-1"},
				"actor-executor": {ActorID: "actor-executor", State: types.StateSuspended, Activity: types.ActivityAwaitingApproval},
			},
			MockLLM:     true,
			ModelUsage:  types.ModelUsage{Calls: 2, PromptTokens: 10},
			StatePollMs: 40,
			WorkerPods:  types.WorkerPods{Current: 3, Desired: 5, Max: 5, Available: true},
			ActorCount:  50,
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
		for _, key := range []string{`"worker_pods":{"current":3,"desired":5,"max":5,"available":true}`, `"actor_count":50`} {
			if !strings.Contains(string(data), key) {
				t.Errorf("StageTelemetry JSON lacks %s: %s", key, data)
			}
		}
	})
}

func TestEnhancementTypesSerialization(t *testing.T) {
	t.Run("ActorDetails", func(t *testing.T) {
		details := types.ActorDetails{
			ActorView: types.ActorView{
				ActorID:     "attendee-07",
				State:       types.StateSuspended,
				Activity:    types.ActivityAwaitingApproval,
				SnapshotURI: "gs://test-bucket/ate-demo-conf/actors/attendee-07.snap",
			},
			Usage: types.ModelUsage{Calls: 1},
			ChatHistory: []types.ChatMessage{
				{
					ID:        "msg-1",
					Sender:    types.SenderUser,
					Content:   "Analyze recent survey data",
					Timestamp: 1789910000,
				},
			},
			PendingApproval: &types.ApprovalRequest{
				ApprovalID: "app-1",
				Summary:    "Run script",
				Code:       "print(1)",
				Language:   "python",
				RiskLevel:  types.RiskHigh,
			},
			LastResult: &types.ExecutionResult{
				Stdout:     "output",
				Stderr:     "",
				ExitCode:   0,
				DurationMs: 25,
			},
		}

		data, err := json.Marshal(details)
		if err != nil {
			t.Fatalf("failed to marshal ActorDetails: %v", err)
		}

		var decoded types.ActorDetails
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ActorDetails: %v", err)
		}

		if diff := cmp.Diff(details, decoded); diff != "" {
			t.Fatalf("ActorDetails mismatch (-want +got):\n%s", diff)
		}

		detailsOmit := types.ActorDetails{
			ActorView:   types.ActorView{ActorID: "attendee-08", State: types.StateRunning, WorkerPod: "pool-abc"},
			ChatHistory: []types.ChatMessage{},
		}
		dataOmit, err := json.Marshal(detailsOmit)
		if err != nil {
			t.Fatalf("failed to marshal ActorDetails with omitempty: %v", err)
		}
		if strings.Contains(string(dataOmit), `"pending_approval"`) || strings.Contains(string(dataOmit), `"last_result"`) {
			t.Fatalf("expected pending_approval and last_result to be omitted, got: %s", string(dataOmit))
		}
		var decodedOmit types.ActorDetails
		if err := json.Unmarshal(dataOmit, &decodedOmit); err != nil {
			t.Fatalf("failed to unmarshal ActorDetails: %v", err)
		}
		if diff := cmp.Diff(detailsOmit, decodedOmit); diff != "" {
			t.Fatalf("ActorDetails mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("SimulationRequest", func(t *testing.T) {
		tests := []struct {
			name    string
			req     *types.SimulationRequest
			wantErr bool
		}{
			{
				name:    "valid start count 25",
				req:     &types.SimulationRequest{Action: "start", Count: 25},
				wantErr: false,
			},
			{
				name:    "valid start count min 1",
				req:     &types.SimulationRequest{Action: "start", Count: 1},
				wantErr: false,
			},
			{
				name:    "valid start count max 100",
				req:     &types.SimulationRequest{Action: "start", Count: 100},
				wantErr: false,
			},
			{
				name:    "valid stop",
				req:     &types.SimulationRequest{Action: "stop"},
				wantErr: false,
			},
			{
				name:    "valid stop with count",
				req:     &types.SimulationRequest{Action: "stop", Count: 0},
				wantErr: false,
			},
			{
				name:    "nil receiver",
				req:     nil,
				wantErr: true,
			},
			{
				name:    "invalid action pause",
				req:     &types.SimulationRequest{Action: "pause", Count: 10},
				wantErr: true,
			},
			{
				name:    "invalid action empty",
				req:     &types.SimulationRequest{Action: "", Count: 10},
				wantErr: true,
			},
			{
				name:    "start count 0 out of bounds",
				req:     &types.SimulationRequest{Action: "start", Count: 0},
				wantErr: true,
			},
			{
				name:    "start count negative out of bounds",
				req:     &types.SimulationRequest{Action: "start", Count: -5},
				wantErr: true,
			},
			{
				name:    "start count > 100 out of bounds",
				req:     &types.SimulationRequest{Action: "start", Count: 101},
				wantErr: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := tt.req.Validate()
				if (err != nil) != tt.wantErr {
					t.Errorf("SimulationRequest.Validate() error = %v, wantErr %v", err, tt.wantErr)
				}
			})
		}

		req := types.SimulationRequest{Action: "start", Count: 25}
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("failed to marshal SimulationRequest: %v", err)
		}
		var decoded types.SimulationRequest
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal SimulationRequest: %v", err)
		}
		if diff := cmp.Diff(req, decoded); diff != "" {
			t.Fatalf("SimulationRequest mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("ModelUsage", func(t *testing.T) {
		usage := types.ModelUsage{
			Calls:           4,
			PromptTokens:    44,
			CandidateTokens: 28,
			TotalLatencyMs:  900,
			LastLatencyMs:   250,
		}

		data, err := json.Marshal(usage)
		if err != nil {
			t.Fatalf("failed to marshal ModelUsage: %v", err)
		}

		var decoded types.ModelUsage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ModelUsage: %v", err)
		}

		if diff := cmp.Diff(usage, decoded); diff != "" {
			t.Fatalf("ModelUsage mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestActionLogEntrySerialization(t *testing.T) {
	t.Run("all fields populated", func(t *testing.T) {
		entry := types.ActionLogEntry{
			ID:            "action-101",
			AttendeeID:    "attendee-42",
			Timestamp:     1789910300,
			Prompt:        "Calculate average latency",
			ModelResponse: "Running calculation script",
			Code:          "print(1 + 1)",
			Language:      "python",
			RiskLevel:     types.RiskMedium,
			ApprovalID:    "app-99",
			Decision:      types.DecisionApprove,
			Execution: &types.ExecutionResult{
				Stdout:     "2\n",
				Stderr:     "",
				ExitCode:   0,
				DurationMs: 45,
			},
			WorkerPod: "conf-workers-2",
			Activity:  types.ActivityExecuting,
			Source:    types.SourceGemini,
		}

		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("failed to marshal ActionLogEntry: %v", err)
		}

		var decoded types.ActionLogEntry
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ActionLogEntry: %v", err)
		}

		if diff := cmp.Diff(entry, decoded); diff != "" {
			t.Fatalf("ActionLogEntry mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("optional fields omitted (omitempty)", func(t *testing.T) {
		minimal := types.ActionLogEntry{
			ID:         "action-102",
			AttendeeID: "attendee-43",
			Timestamp:  1789910400,
			Prompt:     "Hello agent",
		}

		data, err := json.Marshal(minimal)
		if err != nil {
			t.Fatalf("failed to marshal ActionLogEntry: %v", err)
		}

		jsonStr := string(data)
		omittedFields := []string{
			`"model_response"`,
			`"code"`,
			`"language"`,
			`"risk_level"`,
			`"approval_id"`,
			`"decision"`,
			`"execution"`,
			`"worker_pod"`,
			`"activity"`,
			`"source"`,
		}
		for _, field := range omittedFields {
			if strings.Contains(jsonStr, field) {
				t.Errorf("expected field %s to be omitted, got %s", field, jsonStr)
			}
		}

		var decoded types.ActionLogEntry
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal ActionLogEntry: %v", err)
		}

		if diff := cmp.Diff(minimal, decoded); diff != "" {
			t.Fatalf("ActionLogEntry mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestActorViewSerialization(t *testing.T) {
	view := types.ActorView{
		ActorID:      "attendee-01",
		State:        types.StateSuspended,
		NeverStarted: true,
	}

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("failed to marshal ActorView: %v", err)
	}

	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"never_started":true`) {
		t.Errorf("expected never_started:true, got %s", jsonStr)
	}
	for _, field := range []string{`"activity"`, `"source"`} {
		if strings.Contains(jsonStr, field) {
			t.Errorf("expected field %s to be omitted, got %s", field, jsonStr)
		}
	}

	var decoded types.ActorView
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal ActorView: %v", err)
	}
	if diff := cmp.Diff(view, decoded); diff != "" {
		t.Fatalf("ActorView mismatch (-want +got):\n%s", diff)
	}
}

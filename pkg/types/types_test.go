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
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func TestMessageSerialization(t *testing.T) {
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

	if decoded.Approval == nil || decoded.Approval.ApprovalID != "app-1" {
		t.Fatalf("expected approval ID 'app-1', got %+v", decoded.Approval)
	}
}

func TestValidation(t *testing.T) {
	validResp := types.ApprovalResponse{
		ApprovalID: "app-1",
		Decision:   types.DecisionApprove,
	}
	if err := validResp.Validate(); err != nil {
		t.Errorf("expected valid approval response, got error: %v", err)
	}

	invalidResp := types.ApprovalResponse{
		ApprovalID: "",
		Decision:   "UNKNOWN",
	}
	if err := invalidResp.Validate(); err == nil {
		t.Errorf("expected error for invalid approval response, got nil")
	}
}

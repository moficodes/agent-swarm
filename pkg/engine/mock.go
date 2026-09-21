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

package engine

import (
	"fmt"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// MockEngine generates deterministic agent turns when LLM access is disabled or unavailable.
type MockEngine struct{}

// NewMockEngine creates a new MockEngine.
func NewMockEngine() *MockEngine {
	return &MockEngine{}
}

// GenerateTurn produces a deterministic ChatMessage requesting human approval for an anomaly script.
func (m *MockEngine) GenerateTurn(prompt string) *types.ChatMessage {
	return &types.ChatMessage{
		ID:        fmt.Sprintf("mock-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   "I analyzed your request. To extract the metrics and generate the visualization, I have prepared this analysis script. Please review and authorize execution in the sandbox:",
		Timestamp: time.Now().Unix(),
		Approval: &types.ApprovalRequest{
			ApprovalID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
			Summary:    "Execute Telemetry Outlier Analysis & ASCII Plotting",
			Code: `def detect_anomalies(data):
    threshold = 2.5
    mean = sum(data) / len(data)
    variance = sum((x - mean) ** 2 for x in data) / len(data)
    std_dev = variance ** 0.5
    anomalies = [x for x in data if abs(x - mean) > threshold * std_dev]
    print(f"[SANDBOX] Mean: {mean:.2f} | StdDev: {std_dev:.2f} | Anomalies: {len(anomalies)}")
    print(" 100 |  *")
    print("  75 |  * *   *")
    print("  50 |  * * * * *")
    print("   0 +-----------")
    return anomalies

detect_anomalies([12, 14, 15, 13, 98, 12, 11, 14, 88, 13])`,
			Language:  "python",
			RiskLevel: types.RiskHigh,
		},
	}
}

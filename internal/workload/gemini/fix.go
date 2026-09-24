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

package gemini

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// ErrNoFix means the model did not propose a corrected script that can run.
var ErrNoFix = errors.New("gemini: no runnable fix proposed")

// maxFixStderr bounds the error output a fix request carries. The tail is
// kept, since a Python traceback ends with the exception.
const maxFixStderr = 2 << 10

// Fix asks the model why an approved script failed and for a corrected one,
// which gets the same check as any proposal. The reply always carries an
// approval; a reply without a runnable script is ErrNoFix.
func (c *Client) Fix(ctx context.Context, history []types.ChatMessage, failed *types.ApprovalRequest, run *types.ExecutionResult) (*types.ChatMessage, error) {
	msg, err := c.run(ctx, BuildContents(history, fixPrompt(failed, run)))
	if err != nil {
		return nil, err
	}
	if msg.Approval == nil {
		return nil, ErrNoFix
	}
	if msg.Content == "" {
		msg.Content = "My first script didn't run here, so I've corrected it. Please review the new version:"
	}
	return msg, nil
}

func fixPrompt(failed *types.ApprovalRequest, run *types.ExecutionResult) string {
	stderr := strings.TrimSpace(run.Stderr)
	if len(stderr) > maxFixStderr {
		stderr = stderr[len(stderr)-maxFixStderr:]
	}
	how := fmt.Sprintf("failed with exit code %d", run.ExitCode)
	if run.ExitCode == -1 {
		how = "was stopped before it finished; it may have run past the 5 second limit"
	}
	return fmt.Sprintf("The script you proposed %s.\n\nScript:\n%s\n\nError output:\n%s\n\n"+
		"In one sentence, say what went wrong. Then call execute_sandbox_code with a corrected script.",
		how, failed.Code, stderr)
}

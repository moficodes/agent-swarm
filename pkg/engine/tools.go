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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// SandboxExecutor manages isolated execution of code scripts inside a workspace directory.
type SandboxExecutor struct {
	WorkspaceDir string
}

// NewSandboxExecutor creates a new SandboxExecutor with the specified workspace directory.
func NewSandboxExecutor(workspaceDir string) *SandboxExecutor {
	if workspaceDir == "" {
		workspaceDir = "/tmp/workspace"
	}
	_ = os.MkdirAll(workspaceDir, 0o755)
	return &SandboxExecutor{WorkspaceDir: workspaceDir}
}

// Execute runs the provided script in the specified language within the workspace.
func (s *SandboxExecutor) Execute(ctx context.Context, lang string, script string) (*types.ExecutionResult, error) {
	start := time.Now()
	var cmd *exec.Cmd

	switch lang {
	case "python", "py", "python3":
		tmpFile, err := os.CreateTemp(s.WorkspaceDir, "script_*.py")
		if err != nil {
			return nil, fmt.Errorf("failed to create script file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.WriteString(script); err != nil {
			_ = tmpFile.Close()
			return nil, fmt.Errorf("failed to write script: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			return nil, fmt.Errorf("failed to close script file: %w", err)
		}

		pythonBin := "python3"
		if _, err := exec.LookPath("python3"); err != nil {
			if _, err := exec.LookPath("python"); err == nil {
				pythonBin = "python"
			}
		}
		cmd = exec.CommandContext(ctx, pythonBin, tmpFile.Name())
	case "bash":
		bashBin := "bash"
		if _, err := exec.LookPath("bash"); err != nil {
			bashBin = "/bin/sh"
		}
		cmd = exec.CommandContext(ctx, bashBin, "-c", script)
	case "sh":
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", script)
	default:
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.Dir = s.WorkspaceDir

	err := cmd.Run()
	duration := time.Since(start).Milliseconds()

	if ctx.Err() != nil {
		return &types.ExecutionResult{
			Stdout:     stdoutBuf.String(),
			Stderr:     stderrBuf.String(),
			ExitCode:   -1,
			DurationMs: duration,
		}, fmt.Errorf("execution context error: %w", ctx.Err())
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("failed to start process: %w", err)
		}
	}

	return &types.ExecutionResult{
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ExitCode:   exitCode,
		DurationMs: duration,
	}, nil
}

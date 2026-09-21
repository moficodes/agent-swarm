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

package engine_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload/internal/engine"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func TestSandboxExecutor_Python(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	res, err := executor.Execute(ctx, "python", "print('hello from isolated sandbox')")
	if err != nil {
		t.Fatalf("unexpected error executing python code: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil execution result")
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello from isolated sandbox") {
		t.Fatalf("expected stdout to contain 'hello from isolated sandbox', got %q", res.Stdout)
	}
	if res.DurationMs < 0 {
		t.Fatalf("expected DurationMs >= 0, got %d", res.DurationMs)
	}
}

func TestSandboxExecutor_Shell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	res, err := executor.Execute(ctx, "sh", "echo 'hello from shell'")
	if err != nil {
		t.Fatalf("unexpected error executing shell script: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil execution result")
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello from shell") {
		t.Fatalf("expected stdout to contain 'hello from shell', got %q", res.Stdout)
	}
	if res.DurationMs < 0 {
		t.Fatalf("expected DurationMs >= 0, got %d", res.DurationMs)
	}
}

func TestSandboxExecutor_Bash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	res, err := executor.Execute(ctx, "bash", "echo 'hello from bash'")
	if err != nil {
		t.Fatalf("unexpected error executing bash script: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil execution result")
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello from bash") {
		t.Fatalf("expected stdout to contain 'hello from bash', got %q", res.Stdout)
	}
	if res.DurationMs < 0 {
		t.Fatalf("expected DurationMs >= 0, got %d", res.DurationMs)
	}
}

func TestSandboxExecutor_StderrSeparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	// Test shell stderr separation
	res, err := executor.Execute(ctx, "sh", "echo 'standard output'; echo 'error output' >&2")
	if err != nil {
		t.Fatalf("unexpected error executing script: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil execution result")
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "standard output") {
		t.Fatalf("expected stdout to contain 'standard output', got %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "error output") {
		t.Fatalf("expected stdout to NOT contain 'error output', got %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "error output") {
		t.Fatalf("expected stderr to contain 'error output', got %q", res.Stderr)
	}
	if strings.Contains(res.Stderr, "standard output") {
		t.Fatalf("expected stderr to NOT contain 'standard output', got %q", res.Stderr)
	}

	// Test python stderr separation
	pyRes, err := executor.Execute(ctx, "python", "import sys\nsys.stdout.write('py-out\\n')\nsys.stderr.write('py-err\\n')")
	if err != nil {
		t.Fatalf("unexpected error executing python script: %v", err)
	}
	if pyRes == nil {
		t.Fatal("expected non-nil execution result for python")
	}
	if pyRes.ExitCode != 0 {
		t.Fatalf("expected python exit code 0, got %d", pyRes.ExitCode)
	}
	if !strings.Contains(pyRes.Stdout, "py-out") {
		t.Fatalf("expected python stdout to contain 'py-out', got %q", pyRes.Stdout)
	}
	if strings.Contains(pyRes.Stdout, "py-err") {
		t.Fatalf("expected python stdout to NOT contain 'py-err', got %q", pyRes.Stdout)
	}
	if !strings.Contains(pyRes.Stderr, "py-err") {
		t.Fatalf("expected python stderr to contain 'py-err', got %q", pyRes.Stderr)
	}
	if strings.Contains(pyRes.Stderr, "py-out") {
		t.Fatalf("expected python stderr to NOT contain 'py-out', got %q", pyRes.Stderr)
	}
}

func TestSandboxExecutor_NonZeroExitCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	res, err := executor.Execute(ctx, "sh", "exit 2")
	if err != nil {
		t.Fatalf("unexpected error executing script: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil execution result")
	}
	if res.ExitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", res.ExitCode)
	}
}

func TestSandboxExecutor_PythonUniqueFilename(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	_, err := executor.Execute(ctx, "python", "print('run 1')")
	if err != nil {
		t.Fatalf("unexpected error executing python script: %v", err)
	}
	_, err = executor.Execute(ctx, "python", "print('run 2')")
	if err != nil {
		t.Fatalf("unexpected error executing python script: %v", err)
	}

	files, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatalf("failed to read workspace dir: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("expected at least 2 distinct script files, got %d", len(files))
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), "script_") || !strings.HasSuffix(f.Name(), ".py") {
			t.Errorf("expected script file to match script_*.py, got %q", f.Name())
		}
	}
}

func TestSandboxExecutor_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	start := time.Now()
	// An infinite loop that must be aborted by context timeout.
	res, err := executor.Execute(ctx, "sh", "while true; do sleep 0.05; done")
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("execution took too long (%v), timeout was not respected", elapsed)
	}
	if err == nil && (res == nil || res.ExitCode == 0) {
		t.Fatalf("expected error or non-zero exit code on cancelled/timed-out execution, got err=%v res=%+v", err, res)
	}
}

func TestSandboxExecutor_UnsupportedLanguage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	executor := engine.NewSandboxExecutor(workspace)

	_, err := executor.Execute(ctx, "unsupported-lang", "print('hello')")
	if err == nil {
		t.Fatal("expected error for unsupported language, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Fatalf("expected error message to mention unsupported language, got: %v", err)
	}
}

func TestMockEngine_GenerateTurn(t *testing.T) {
	mockEngine := engine.NewMockEngine()
	msg := mockEngine.GenerateTurn("Analyze sensor logs for anomalies")

	if msg == nil {
		t.Fatal("expected non-nil ChatMessage")
	}
	if msg.Sender != types.SenderAssistant {
		t.Errorf("expected sender %q, got %q", types.SenderAssistant, msg.Sender)
	}
	if msg.Approval == nil {
		t.Fatal("expected non-nil ApprovalRequest")
	}
	if msg.Approval.Language != "python" {
		t.Errorf("expected language %q, got %q", "python", msg.Approval.Language)
	}
	if msg.Approval.RiskLevel != types.RiskHigh {
		t.Errorf("expected risk level %q, got %q", types.RiskHigh, msg.Approval.RiskLevel)
	}
	if !strings.Contains(msg.Approval.Code, "detect_anomalies") {
		t.Fatalf("expected code snippet to contain 'detect_anomalies', got: %s", msg.Approval.Code)
	}

	// Verify that the code generated by MockEngine is valid executable Python.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executor := engine.NewSandboxExecutor(t.TempDir())
	res, err := executor.Execute(ctx, msg.Approval.Language, msg.Approval.Code)
	if err != nil {
		t.Fatalf("failed to execute mock generated code: %v", err)
	}
	if res == nil || res.ExitCode != 0 {
		t.Fatalf("expected mock code to execute successfully (exit 0), got: %+v", res)
	}
	if !strings.Contains(res.Stdout, "[SANDBOX]") {
		t.Fatalf("expected mock code output to contain '[SANDBOX]', got: %q", res.Stdout)
	}
}

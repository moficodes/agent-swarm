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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/internal/workload/engine"
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

	// Verify sequential executions generate distinct files and assert the script exists during execution.
	res1, err := executor.Execute(ctx, "python", "import os\nprint(__file__)\nassert os.path.exists(__file__)")
	if err != nil {
		t.Fatalf("unexpected error executing python script sequentially: %v", err)
	}
	res2, err := executor.Execute(ctx, "python", "import os\nprint(__file__)\nassert os.path.exists(__file__)")
	if err != nil {
		t.Fatalf("unexpected error executing python script sequentially: %v", err)
	}
	path1 := strings.TrimSpace(res1.Stdout)
	path2 := strings.TrimSpace(res2.Stdout)
	if path1 == path2 {
		t.Fatalf("sequential python executions collided on same filename: %q", path1)
	}

	// Verify concurrent python executions don't collide.
	const numRuns = 5
	var wg sync.WaitGroup
	filenames := make(chan string, numRuns)
	errCh := make(chan error, numRuns)

	for i := 0; i < numRuns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := executor.Execute(ctx, "python", "import os\nprint(__file__)\nassert os.path.exists(__file__)")
			if err != nil {
				errCh <- fmt.Errorf("unexpected execution error: %w", err)
				return
			}
			if res.ExitCode != 0 {
				errCh <- fmt.Errorf("expected exit code 0, got %d (stderr: %s)", res.ExitCode, res.Stderr)
				return
			}
			filenames <- strings.TrimSpace(res.Stdout)
		}()
	}

	wg.Wait()
	close(filenames)
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	seen := make(map[string]bool)
	seen[path1] = true
	seen[path2] = true

	for path := range filenames {
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "script_") || !strings.HasSuffix(base, ".py") {
			t.Errorf("expected script file to match script_*.py, got %q", base)
		}
		if seen[path] {
			t.Errorf("detected collision with script file: %q", path)
		}
		seen[path] = true
	}

	if len(seen) != numRuns+2 {
		t.Fatalf("expected %d distinct script filenames, got %d", numRuns+2, len(seen))
	}

	// Verify that script files are cleaned up after execution completes.
	files, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatalf("failed to read workspace dir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected workspace to be cleaned up, got %d files remaining", len(files))
	}
}

func TestSandboxExecutor_ProcessStartupFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("invalid binary path", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		workspace := t.TempDir()
		executor := engine.NewSandboxExecutor(workspace)

		res, err := executor.Execute(ctx, "python", "print('hello')")
		if err == nil {
			t.Fatal("expected error for missing python binary, got nil")
		}
		if res != nil {
			t.Fatalf("expected nil result on startup failure, got %+v", res)
		}
		if !strings.Contains(err.Error(), "failed to start process") {
			t.Fatalf("expected error to contain 'failed to start process', got: %v", err)
		}
	})

	t.Run("invalid working directory", func(t *testing.T) {
		nonexistentDir := filepath.Join(t.TempDir(), "nonexistent")
		executor := &engine.SandboxExecutor{WorkspaceDir: nonexistentDir}

		res, err := executor.Execute(ctx, "sh", "echo hello")
		if err == nil {
			t.Fatal("expected error for nonexistent working directory, got nil")
		}
		if res != nil {
			t.Fatalf("expected nil result on startup failure, got %+v", res)
		}
		if !strings.Contains(err.Error(), "failed to start process") {
			t.Fatalf("expected error to contain 'failed to start process', got: %v", err)
		}
	})
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

func TestSandboxExecutor_TimeoutKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	executor := engine.NewSandboxExecutor(t.TempDir())
	pidFile := filepath.Join(t.TempDir(), "pid")

	start := time.Now()
	// The backgrounded sleep inherits stdout, so Run would block on it unless the group is killed.
	res, err := executor.Execute(ctx, "bash", fmt.Sprintf("sleep 30 & echo $! > %s; sleep 30", pidFile))
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("execution took %v, want <= ~timeout+1s", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "execution context error") {
		t.Fatalf("expected execution context error, got %v", err)
	}
	if res == nil || res.ExitCode != -1 {
		t.Fatalf("expected ExitCode -1 on timeout, got %+v", res)
	}

	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("failed to read pid file: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if convErr != nil {
		t.Fatalf("invalid pid %q: %v", data, convErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("backgrounded grandchild %d still alive after timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSandboxExecutor_OutputCapped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executor := engine.NewSandboxExecutor(t.TempDir())
	res, err := executor.Execute(ctx, "bash", "yes | head -c 1000000; yes | head -c 1000000 >&2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	for name, out := range map[string]string{"stdout": res.Stdout, "stderr": res.Stderr} {
		if !strings.HasSuffix(out, engine.OutputTruncatedMarker) {
			t.Errorf("expected %s to end with truncation marker, got suffix %q", name, out[max(0, len(out)-40):])
		}
		if want := engine.MaxOutputBytes + len(engine.OutputTruncatedMarker); len(out) != want {
			t.Errorf("expected %s length %d, got %d", name, want, len(out))
		}
	}

	small, err := executor.Execute(ctx, "sh", "echo small")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(small.Stdout, engine.OutputTruncatedMarker) {
		t.Fatalf("unexpected truncation marker in small output %q", small.Stdout)
	}
}

func TestSandboxExecutor_WorkspaceClearedBetweenRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executor := engine.NewSandboxExecutor(t.TempDir())
	if _, err := executor.Execute(ctx, "sh", "echo secret > leftover.txt; mkdir -p sub && touch sub/nested"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := executor.Execute(ctx, "sh", "ls -A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out := strings.TrimSpace(res.Stdout); out != "" {
		t.Fatalf("expected empty workspace on second run, got %q", out)
	}
}

func TestNewSandboxExecutor_WorkspaceCreateError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A regular file in the path makes MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	executor := engine.NewSandboxExecutor(filepath.Join(blocker, "workspace"))
	res, err := executor.Execute(ctx, "sh", "echo hello")
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("expected workspace setup error, got err=%v res=%+v", err, res)
	}
}

func TestSandboxExecutor_SerializesExecutions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	executor := engine.NewSandboxExecutor(t.TempDir())
	// mkdir is atomic: it fails if another run currently holds the lock dir.
	lock := filepath.Join(t.TempDir(), "lock")
	script := fmt.Sprintf("mkdir %[1]s || { echo overlap; exit 1; }; sleep 0.2; rmdir %[1]s", lock)

	const numRuns = 3
	var wg sync.WaitGroup
	errCh := make(chan error, numRuns)
	for i := 0; i < numRuns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := executor.Execute(ctx, "sh", script)
			if err != nil {
				errCh <- err
				return
			}
			if res.ExitCode != 0 {
				errCh <- fmt.Errorf("executions overlapped: stdout=%q stderr=%q", res.Stdout, res.Stderr)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestExecute_ChildSeesOnlyMinimalEnvironment(t *testing.T) {
	t.Setenv("HMAC_SECRET", "must-not-leak")
	t.Setenv("VERTEX_PROJECT", "proj")
	t.Setenv("GEMINI_CREDENTIALS", "leaked-gemini-key")
	t.Setenv("GOOGLE_API_KEY", "leaked-google-key")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := t.TempDir()
	executor := engine.NewSandboxExecutor(ws)

	res, err := executor.Execute(ctx, "sh", "env")
	if err != nil {
		t.Fatal(err)
	}

	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed env line %q", line)
		}
		env[k] = v
	}

	want := map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin",
		"HOME": ws,
		"LANG": "C.UTF-8",
	}
	for k, v := range want {
		if got, ok := env[k]; !ok || got != v {
			t.Errorf("child env %s = %q, want %q", k, got, v)
		}
	}

	if v, ok := env["HMAC_SECRET"]; ok {
		t.Errorf("child env leaked HMAC_SECRET=%q", v)
	}
	if v, ok := env["VERTEX_PROJECT"]; ok {
		t.Errorf("child env leaked VERTEX_PROJECT=%q", v)
	}
	// /bin/sh injects PWD, SHLVL, _ and (on some platforms) OLDPWD itself;
	// they are not part of what the executor controls, so they're ignored.
	shellInjected := map[string]bool{"PWD": true, "SHLVL": true, "_": true, "OLDPWD": true}
	for k, v := range env {
		if _, ok := want[k]; shellInjected[k] || ok {
			continue
		}
		if strings.HasPrefix(k, "GEMINI") || strings.HasPrefix(k, "GOOGLE") {
			t.Errorf("child env leaked %s=%q", k, v)
		}
	}
}

func TestSandboxExecutor_RunsAsNobodyWhenRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executor := engine.NewSandboxExecutor(t.TempDir())
	res, err := executor.Execute(ctx, "sh", "id -u; touch owned && echo wrote")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Fields(res.Stdout)
	if len(lines) != 2 || lines[1] != "wrote" {
		t.Fatalf("expected uid and successful workspace write, got stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if os.Geteuid() != 0 {
		if want := strconv.Itoa(os.Geteuid()); lines[0] != want {
			t.Fatalf("expected child to run as current uid %s, got %s", want, lines[0])
		}
		t.Skip("dropping to uid 65534 requires running the test as root")
	}
	if lines[0] != "65534" {
		t.Fatalf("expected child uid 65534, got %s", lines[0])
	}

	// Python scripts are written by the server and must be readable by the child.
	pyRes, err := executor.Execute(ctx, "python", "import os\nprint(os.getuid())")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(pyRes.Stdout); got != "65534" {
		t.Fatalf("expected python child uid 65534, got %q (stderr: %s)", got, pyRes.Stderr)
	}
}

func TestMakeTraversable_OpensOwnerOnlyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := engine.MakeTraversable(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %o, want 755", got)
	}
}

func TestMakeTraversable_LeavesTraversableDirAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	if err := engine.MakeTraversable(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o711 {
		t.Errorf("mode = %o, want 711 unchanged", got)
	}
}

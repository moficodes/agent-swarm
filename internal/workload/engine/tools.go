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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// waitDelay bounds how long Execute waits for output pipes after the child is killed.
const waitDelay = 500 * time.Millisecond

// sandboxUID and sandboxGID are the ids ("nobody") scripts run as when the server is root.
const (
	sandboxUID = 65534
	sandboxGID = 65534
)

// defaultScriptMemoryMiB caps the address space of each script process.
//
// The actor's 512Mi memory limit applies to its whole gVisor sandbox, so a
// script that goes over it gets the sandbox OOM-killed by the host. Substrate
// does not notice that: the actor stays RUNNING, requests to it hang, and its
// next suspend crashes it. With the cap the script fails on its own with a
// MemoryError (or ENOMEM) and the actor stays healthy. The cap leaves room
// under 512Mi for this server and the sandbox itself.
const defaultScriptMemoryMiB = 320

// scriptMemoryEnv overrides defaultScriptMemoryMiB; 0 turns the cap off.
const scriptMemoryEnv = "SCRIPT_MEMORY_LIMIT_MIB"

// SandboxExecutor manages isolated execution of code scripts inside a workspace directory.
type SandboxExecutor struct {
	WorkspaceDir string
	// MemoryLimitMiB caps the virtual memory (RLIMIT_AS) of every process a
	// script starts; 0 means no cap. The cap is per process, so a script
	// that starts several processes can still use more in total.
	MemoryLimitMiB int

	// mu ensures only one script runs at a time, since runs share the workspace.
	mu sync.Mutex
	// initErr records a workspace setup failure, reported by Execute.
	initErr error
}

// NewSandboxExecutor creates a new SandboxExecutor with the specified workspace directory.
func NewSandboxExecutor(workspaceDir string) *SandboxExecutor {
	if workspaceDir == "" {
		workspaceDir = "/tmp/workspace"
	}
	s := &SandboxExecutor{WorkspaceDir: workspaceDir, MemoryLimitMiB: defaultScriptMemoryMiB}
	if v, ok := os.LookupEnv(scriptMemoryEnv); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.MemoryLimitMiB = n
		} else {
			log.Printf("[executor] ignoring %s=%q, want a whole number of MiB", scriptMemoryEnv, v)
		}
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		s.initErr = fmt.Errorf("failed to create workspace: %w", err)
	} else if os.Geteuid() == 0 {
		if err := os.Chown(workspaceDir, sandboxUID, sandboxGID); err != nil {
			s.initErr = fmt.Errorf("failed to chown workspace: %w", err)
		} else if err := MakeTraversable("/"); err != nil {
			s.initErr = err
		}
	}
	return s
}

// MakeTraversable opens an owner-only dir to everyone for reading and search.
// Substrate composes an actor's rootfs with "/" at 0700, which leaves nobody
// unable to resolve any path, so the executor opens it before running scripts.
func MakeTraversable(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", dir, err)
	}
	if fi.Mode().Perm()&0o001 != 0 {
		return nil
	}
	if err := os.Chmod(dir, fi.Mode().Perm()|0o055); err != nil {
		return fmt.Errorf("failed to open %s to the sandbox user: %w", dir, err)
	}
	return nil
}

// sandboxEnv is the whole environment model-generated code sees, so no
// configuration of the actor process leaks into it.
func sandboxEnv(workspace string) []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + workspace,
		"LANG=C.UTF-8",
	}
}

// memoryCapScript sets the address-space limit ($1, in KiB) and then
// becomes the real command, so the script keeps the PID Execute started.
const memoryCapScript = `ulimit -v "$1" && shift && exec "$@"`

// limitMemory rewrites cmd to run under MemoryLimitMiB. Go cannot set
// rlimits for a child directly, so a shell sets it and execs the command.
func (s *SandboxExecutor) limitMemory(cmd *exec.Cmd) {
	if s.MemoryLimitMiB <= 0 || runtime.GOOS != "linux" {
		return
	}
	kib := strconv.Itoa(s.MemoryLimitMiB * 1024)
	cmd.Args = append([]string{"/bin/sh", "-c", memoryCapScript, "sh", kib, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/bin/sh"
}

// ensureWorkspaceOwner creates dir if needed and hands it to
// sandboxUID:sandboxGID when it is owned by another user. Restoring an actor
// from a template golden snapshot attaches a fresh external volume mounted as
// root:root after NewSandboxExecutor has already run, so Execute checks
// ownership before dropping privileges.
func ensureWorkspaceOwner(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create workspace: %w", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid == sandboxUID && st.Gid == sandboxGID {
		return nil
	}
	if err := os.Chown(dir, sandboxUID, sandboxGID); err != nil {
		return fmt.Errorf("failed to chown workspace: %w", err)
	}
	return nil
}

// ClearWorkspace removes everything inside WorkspaceDir while keeping the
// directory itself intact for mounted volumes.
func (s *SandboxExecutor) ClearWorkspace() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.WorkspaceDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read workspace: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.WorkspaceDir, e.Name())); err != nil {
			return fmt.Errorf("failed to clear workspace: %w", err)
		}
	}
	return nil
}

// Execute runs the provided script in the specified language within the workspace.
func (s *SandboxExecutor) Execute(ctx context.Context, lang string, script string) (*types.ExecutionResult, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	start := time.Now()
	runAsNobody := os.Geteuid() == 0
	if runAsNobody {
		if err := ensureWorkspaceOwner(s.WorkspaceDir); err != nil {
			return nil, err
		}
		if err := MakeTraversable("/"); err != nil {
			return nil, err
		}
	}
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
		if runAsNobody {
			if err := os.Chown(tmpFile.Name(), sandboxUID, sandboxGID); err != nil {
				return nil, fmt.Errorf("failed to chown script file: %w", err)
			}
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
	s.limitMemory(cmd)

	var stdoutBuf, stderrBuf limitWriter
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.Dir = s.WorkspaceDir
	cmd.Env = sandboxEnv(s.WorkspaceDir)
	// Run the child in its own process group so a timeout kills every descendant.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if runAsNobody {
		// Never run model-generated code as root.
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: sandboxUID, Gid: sandboxGID}
	}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Bound how long Run waits on pipes held open by escaped descendants.
	cmd.WaitDelay = waitDelay

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

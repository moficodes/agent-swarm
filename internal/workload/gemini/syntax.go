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
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// SyntaxChecker returns a one-line description of a syntax error in code,
// or "" when the code parses or cannot be checked.
type SyntaxChecker func(ctx context.Context, language, code string) string

// compileScript compiles the script on stdin without running it and prints
// the first syntax error, if any.
const compileScript = `import sys
try:
    compile(sys.stdin.read(), "script.py", "exec")
except SyntaxError as e:
    print(f"{type(e).__name__}: {e.msg} (line {e.lineno})")
`

// PythonSyntaxError checks Python code by compiling it with python3, which
// parses the script but runs none of it. Other languages are not checked.
func PythonSyntaxError(ctx context.Context, language, code string) string {
	switch strings.ToLower(language) {
	case "python", "py", "python3":
	default:
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-I", "-c", compileScript)
	cmd.Stdin = strings.NewReader(code)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

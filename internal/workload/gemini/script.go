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

// ScriptChecker returns a one-line description of the first reason code
// cannot run in the sandbox, or "" when it can or cannot be checked.
type ScriptChecker func(ctx context.Context, language, code string) string

// checkProgram reads a script on stdin and, running none of it, prints the
// first reason it cannot run in the sandbox: a syntax error, a relative
// import, a module outside the standard library, or a network module.
const checkProgram = `import ast, sys
NETWORK = {"socket", "ssl", "http", "urllib", "ftplib", "smtplib", "poplib",
           "imaplib", "telnetlib", "xmlrpc", "socketserver", "asyncio"}
src = sys.stdin.read()
try:
    compile(src, "script.py", "exec")
except SyntaxError as e:
    print(f"{type(e).__name__}: {e.msg} (line {e.lineno})")
    sys.exit()
for node in ast.walk(ast.parse(src)):
    if isinstance(node, ast.Import):
        names = [a.name for a in node.names]
    elif isinstance(node, ast.ImportFrom):
        if node.level:
            print(f"relative import on line {node.lineno}; the script is a single file")
            sys.exit()
        names = [node.module]
    else:
        continue
    for name in names:
        top = name.split(".")[0]
        if top not in sys.stdlib_module_names:
            print(f"module {top} is not installed (line {node.lineno}); use only the standard library")
            sys.exit()
        if top in NETWORK:
            print(f"module {top} needs the network (line {node.lineno}); the sandbox has no network access")
            sys.exit()
`

// ScriptProblem checks that code can run in the sandbox. Only Python can;
// python3 then parses the script, running none of it, for syntax errors and
// imports the sandbox cannot satisfy. Imports built at runtime are not seen.
func ScriptProblem(ctx context.Context, language, code string) string {
	if !isPython(language) {
		return "only Python scripts can run in this sandbox"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-I", "-c", checkProgram)
	cmd.Stdin = strings.NewReader(code)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

func isPython(language string) bool {
	switch strings.ToLower(language) {
	case "python", "py", "python3":
		return true
	}
	return false
}

<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Keeping Generated Scripts From Failing On Stage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Attendees never see a generated script fail because of what the sandbox lacks, and a script that fails anyway after approval comes back as a self-correction instead of a raw stderr dump.

**Architecture:** The actor's Gemini client gets a system instruction, a Python-only tool schema and a stricter pre-approval check (syntax, non-stdlib imports, network modules), and drops a script that fails the check twice. When an approved run fails, the actor's `/api/approve` runs a relayed model turn (`gemini.Client.Fix`) and replies with the failed run marked `Corrected` plus a new approval. The gateway keeps corrected runs off the spotlight and gives the fix its own feed card; the phone, feed and stage render failures through one shared `ActorView.run` mapping.

**Tech Stack:** Go 1.27, `google.golang.org/genai` v1.71, Python 3.12 in the actor image (`python3 -I` for the check), vanilla JS/CSS static pages.

**Spec:** `docs/superpowers/specs/2026-09-24-demo-script-failures-design.md`

## Global Constraints

- Nothing runs before a human approves it. The check only parses the script (`compile` + `ast`); it never executes it.
- Python only: tool `language` is an enum of `python`; any other language is a check failure "only Python scripts can run in this sandbox".
- Network modules: `socket`, `ssl`, `http`, `urllib`, `ftplib`, `smtplib`, `poplib`, `imaplib`, `telnetlib`, `xmlrpc`, `socketserver`, `asyncio`.
- One fix per request. A fix's own failure, a model error during a fix, and mock mode all get the plain reply, never the mock engine.
- Plain reply text, verbatim: `That approach didn't work in this sandbox. Try asking for something simpler.`
- Dropped-script reply text, verbatim: `I couldn't write a script that runs in this sandbox. Try rephrasing the request.`
- `ExecutionResult.Corrected bool` with JSON tag `json:"corrected,omitempty"`.
- The fix turn is bounded by `chatTimeout` (60s) and makes at most 3 model calls (`maxTurns`), within the gateway's 4 per request.
- Stderr is never shown expanded on the phone; the stage and feed show one line of it.
- New and edited Go, JS, CSS, shell and Markdown files keep the Apache license header (`make verify` checks it).

## Review Focus

- A model that ignores the enum and proposes `bash` must be sent back and end up with a Python script, not an unapprovable card. Test: Task 2 `TestGenerate_ShellScriptGoesBackToTheModel`.
- The one-fix budget is per request, not per actor: after a failed fix, the attendee's next request must get its own fix. Test: Task 4 `TestApprove_NewRequestGetsItsOwnFix`.
- A run that floods stderr (up to the executor's cap) must not blow up the fix prompt, and the prompt must keep the end of a traceback, where the exception is. Test: Task 3 `TestFix_KeepsTheTailOfLongErrorOutput`.
- A run stopped by the 5s timeout (exit -1) must tell the model about the time limit, or it will propose the same slow script. Test: Task 3 `TestFix_TimeoutTellsTheModelAboutTheLimit`.
- A fix must not overwrite the approved card in the feed (entries merge by action ID). Test: Task 5 `TestContract_FailedRunIsCorrectedAndTheFixCanBeApproved`.

## Spec deviations (fold into the spec in the task that makes them)

- Stage and feed show the **last** non-empty line of stderr, not the first: a Python traceback's first line is always `Traceback (most recent call last):`. (Task 6)
- The fix prompt keeps the **last** 2 KiB of stderr, for the same reason. (Task 3)

## File map

| File | Change |
|---|---|
| `internal/workload/gemini/syntax.go` → `script.go` | `ScriptChecker`, `ScriptProblem`, Python-only, import checks |
| `internal/workload/gemini/gemini.go` | `WithScriptChecker`, system instruction, `run` loop shared by `Generate` and `Fix`, dropped-script reply |
| `internal/workload/gemini/tools.go` | Python-only `language` enum, sandbox limits in `code` descriptions |
| `internal/workload/gemini/fix.go` (new) | `Fix`, `ErrNoFix`, fix prompt |
| `internal/workload/gemini/gemini_test.go` | check, generate and fix tests |
| `internal/workload/geminitest/geminitest.go` | `ExplainedApprovalResponse` helper |
| `pkg/types/types.go` | `ExecutionResult.Corrected` |
| `internal/workload/server/server.go` | fix turn in `handleApprove`, plain reply, `pendingFix` |
| `internal/workload/server/server_test.go` | existing tests move to Python scripts |
| `internal/workload/server/fix_test.go` (new) | fix-turn tests |
| `cmd/conf-gateway/internal/gateway/actorturn.go` | corrected runs skip the spotlight; fix opens a new action |
| `cmd/conf-gateway/internal/gateway/contract_test.go` | existing tests move to Python; end-to-end fix test |
| `cmd/conf-gateway/static/actorview.js` | `ActorView.run` |
| `cmd/conf-gateway/static/chat.js`, `chat.css` | `renderReply`, collapsed run details |
| `cmd/conf-gateway/static/feed.js`, `feed.css` | failed-run chip in cards |
| `cmd/conf-gateway/static/stage.js`, `stage.css` | failed-run chip in cards, spotlight |
| `cmd/conf-gateway/internal/gateway/static_test.go` | rendering hooks and styles |

---

### Task 1: The pre-approval script check

**Files:**
- Rename: `internal/workload/gemini/syntax.go` → `internal/workload/gemini/script.go`
- Modify: `internal/workload/gemini/gemini.go:80-105` (option, field, default), `:173-183` (check call and message)
- Test: `internal/workload/gemini/gemini_test.go`

**Interfaces:**
- Produces: `type ScriptChecker func(ctx context.Context, language, code string) string`; `func ScriptProblem(ctx context.Context, language, code string) string`; `func WithScriptChecker(sc ScriptChecker) Option`. The `Client` field is `checkScript ScriptChecker`. `SyntaxChecker`, `PythonSyntaxError` and `WithSyntaxChecker` are removed.

- [ ] **Step 1: Write the failing test**

In `internal/workload/gemini/gemini_test.go`, replace `TestPythonSyntaxError` (the last function in the file) with:

```go
// requireStdlibNames skips when python3 is missing or older than 3.10, which
// added sys.stdlib_module_names; the check then reports nothing.
func requireStdlibNames(t *testing.T) {
	t.Helper()
	if err := exec.Command("python3", "-c", "import sys; sys.stdlib_module_names").Run(); err != nil {
		t.Skip("needs python3 3.10 or later")
	}
}

func TestScriptProblem(t *testing.T) {
	requireStdlibNames(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, lang, code, want string
	}{
		{"clean stdlib script", "python", "import statistics, json\nfrom collections import Counter\nprint(statistics.mean([1, 2]))\n", ""},
		{"checked, not run", "python", "raise SystemExit(3)\n", ""},
		{"py alias", "py", "print(1)\n", ""},
		{"syntax error", "python", "x = 1\nprint('a\nb')\n", "line 2"},
		{"compile-only error", "python", "return 5\n", "'return' outside function"},
		{"third-party import", "python", "import pandas as pd\n", "module pandas is not installed"},
		{"third-party from-import", "python", "from numpy import array\n", "module numpy is not installed"},
		{"dotted third-party import", "python", "import matplotlib.pyplot as plt\n", "module matplotlib is not installed"},
		{"import inside a function", "python", "def f():\n    import requests\n", "module requests is not installed"},
		{"relative import", "python", "from . import helpers\n", "relative import"},
		{"network module", "python", "import urllib.request\n", "no network access"},
		{"network from-import", "python", "from socket import socket\n", "no network access"},
		{"shell script", "sh", "echo hi", "only Python scripts can run in this sandbox"},
		{"bash script", "bash", "echo hi", "only Python scripts can run in this sandbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := gemini.ScriptProblem(ctx, tc.lang, tc.code)
			if tc.want == "" && got != "" {
				t.Errorf("ScriptProblem = %q, want none", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("ScriptProblem = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}
```

In the same file, change `fakeChecker`'s return type and the three `WithSyntaxChecker` calls:

```go
// fakeChecker reports a syntax error for the scripts in broken.
func fakeChecker(broken ...string) gemini.ScriptChecker {
```

```go
	c := newClient(fake, gemini.WithScriptChecker(fakeChecker("print('a\nb')")))
```

`TestGenerate_ApprovalCallBecomesPendingApproval` uses the default checker, which now refuses `sh`. Change its fake and assertion to a Python script:

```go
func TestGenerate_ApprovalCallBecomesPendingApproval(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("count files", "import os\nprint(len(os.listdir('.')))", "python"))
	c := newClient(fake)

	msg, err := c.Generate(context.Background(), nil, "how many files?")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil {
		t.Fatal("Approval = nil, want an approval request")
	}
	if msg.Approval.Code != "import os\nprint(len(os.listdir('.')))" || msg.Approval.Language != "python" || msg.Approval.RiskLevel != types.RiskHigh {
		t.Errorf("Approval = %+v", msg.Approval)
	}
	if !strings.HasPrefix(msg.Approval.ApprovalID, "app-") {
		t.Errorf("ApprovalID = %q, want app- prefix", msg.Approval.ApprovalID)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/workload/gemini/`
Expected: build failure, `undefined: gemini.ScriptProblem` / `gemini.ScriptChecker` / `gemini.WithScriptChecker`.

- [ ] **Step 3: Write the implementation**

```bash
git mv internal/workload/gemini/syntax.go internal/workload/gemini/script.go
```

Replace everything below the license header in `internal/workload/gemini/script.go` with:

```go
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
```

In `internal/workload/gemini/gemini.go`, replace the option and the client struct:

```go
// WithScriptChecker replaces the check a proposed script gets before it
// goes to the attendee for approval.
func WithScriptChecker(sc ScriptChecker) Option {
	return func(c *Client) { c.checkScript = sc }
}

// Client runs one Gemini turn, including tool calls.
type Client struct {
	cfg         Config
	transport   http.RoundTripper
	research    ResearchDataHandler
	checkScript ScriptChecker
}
```

In `New`, change `checkSyntax: PythonSyntaxError,` to `checkScript: ScriptProblem,`. In `Generate`, change the check and its message:

```go
		if approval != nil && !retried && turn < maxTurns-1 {
			if msg := c.checkScript(ctx, approval.Language, approval.Code); msg != "" {
				retried = true
				contents = append(contents, cand, &genai.Content{Role: "user", Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{Name: approvalCall.Name, Response: map[string]any{
						"error": "The script cannot run in the sandbox: " + msg + ". Fix it and call " + approvalCall.Name + " again.",
					}},
				}}})
				continue
			}
		}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/workload/gemini/ -v -run 'TestScriptProblem|TestGenerate'`
Expected: PASS (every `TestScriptProblem` subtest runs unless python3 < 3.10).

- [ ] **Step 5: Commit**

```bash
git add internal/workload/gemini
git commit -m "Check proposed scripts for imports and network use, not just syntax"
```

---

### Task 2: What the model is told, and never showing a script that fails the check

**Files:**
- Modify: `internal/workload/gemini/gemini.go` (constants, `Generate` → `Generate` + `run`)
- Modify: `internal/workload/gemini/tools.go:36-68` (`code` and `language` parameters)
- Test: `internal/workload/gemini/gemini_test.go`

**Interfaces:**
- Consumes: `c.checkScript` (Task 1).
- Produces: unexported `func (c *Client) run(ctx context.Context, contents []*genai.Content) (*types.ChatMessage, error)` — plays the tool loop; an approval message's `Content` is the model's text and may be empty. Task 3's `Fix` calls it. `Generate`'s signature is unchanged.

- [ ] **Step 1: Write the failing tests**

Add to `internal/workload/gemini/gemini_test.go`:

```go
func TestGenerate_SendsTheSandboxLimitsAsSystemInstruction(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("hello"))
	if _, err := newClient(fake).Generate(context.Background(), nil, "hi"); err != nil {
		t.Fatal(err)
	}
	body := string(fake.Requests()[0].Body)
	for _, want := range []string{`"systemInstruction"`, "only the standard library", "no network access", "5 seconds"} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %q: %s", want, body)
		}
	}
}

func TestToolDeclarations_LanguageIsPythonOnly(t *testing.T) {
	for _, tool := range gemini.ToolDeclarations() {
		for _, fn := range tool.FunctionDeclarations {
			lang, ok := fn.Parameters.Properties["language"]
			if !ok {
				continue
			}
			if len(lang.Enum) != 1 || lang.Enum[0] != "python" || lang.Format != "enum" {
				t.Errorf("%s language = %+v, want an enum of python only", fn.Name, lang)
			}
		}
	}
}

// The model may ignore the enum; the check sends the script back.
func TestGenerate_ShellScriptGoesBackToTheModel(t *testing.T) {
	fake := geminitest.New(t,
		geminitest.ApprovalResponse("greet", "echo hi", "bash"),
		geminitest.ApprovalResponse("greet", "print('hi')", "python"),
	)
	msg, err := newClient(fake).Generate(context.Background(), nil, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil || msg.Approval.Language != "python" {
		t.Fatalf("Approval = %+v, want the Python script", msg.Approval)
	}
	if body := string(fake.Requests()[1].Body); !strings.Contains(body, "only Python scripts can run in this sandbox") {
		t.Errorf("retry request should say only Python runs; body = %s", body)
	}
}
```

Replace `TestGenerate_BrokenScriptIsRetriedOnlyOnce` and `TestGenerate_NoRetryOnTheLastTurn` with:

```go
func TestGenerate_ScriptThatFailsTheCheckTwiceIsNotShown(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("sum", "print('a\nb')", "python"))
	c := newClient(fake, gemini.WithScriptChecker(fakeChecker("print('a\nb')")))

	msg, err := c.Generate(context.Background(), nil, "sum it")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval != nil {
		t.Fatalf("Approval = %+v, want none for a script that cannot run", msg.Approval)
	}
	if !strings.Contains(msg.Content, "couldn't write a script that runs in this sandbox") {
		t.Errorf("Content = %q", msg.Content)
	}
	if n := len(fake.Requests()); n != 2 {
		t.Errorf("got %d requests, want 2", n)
	}
}

func TestGenerate_BrokenScriptOnTheLastTurnIsNotShown(t *testing.T) {
	research := geminitest.FunctionCallResponse("fetch_research_data", map[string]any{"topic": "cpu"})
	fake := geminitest.New(t, research, research, geminitest.ApprovalResponse("sum", "print('a\nb')", "python"))
	c := newClient(fake, gemini.WithScriptChecker(fakeChecker("print('a\nb')")))

	msg, err := c.Generate(context.Background(), nil, "sum it")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval != nil {
		t.Fatalf("Approval = %+v, want none: there is no turn left to fix it", msg.Approval)
	}
	if n := len(fake.Requests()); n != 3 {
		t.Errorf("got %d requests, want 3", n)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/workload/gemini/ -run 'TestGenerate|TestToolDeclarations'`
Expected: FAIL — no `systemInstruction` in the request, `language` has no enum, the twice-broken script is still returned as an approval.

- [ ] **Step 3: Write the implementation**

In `internal/workload/gemini/tools.go`, replace the `code` and `language` properties of both function declarations. `request_human_approval`:

```go
							"code": {
								Type:        genai.TypeString,
								Description: "The exact Python script to execute. " + sandboxLimits,
							},
							"language": {
								Type:        genai.TypeString,
								Format:      "enum",
								Enum:        []string{"python"},
								Description: "Always python: only Python scripts can run in the sandbox.",
							},
```

`execute_sandbox_code`:

```go
							"language": {
								Type:        genai.TypeString,
								Format:      "enum",
								Enum:        []string{"python"},
								Description: "Always python: only Python scripts can run in the sandbox.",
							},
							"code": {
								Type:        genai.TypeString,
								Description: "Python source code to run inside the sandbox. " + sandboxLimits,
							},
```

Above `ToolDeclarations`, add:

```go
// sandboxLimits is what a script may rely on in the sandbox.
const sandboxLimits = "It may use only the standard library (numpy, pandas and other third-party packages are not installed), has no network access, and is stopped after 5 seconds."
```

In `internal/workload/gemini/gemini.go`, next to `const maxTurns = 3`, add:

```go
// systemInstruction tells the model what the sandbox its scripts run in can
// do, so that it proposes scripts that run there.
const systemInstruction = `You help conference attendees analyze data by writing short Python scripts that run in a sandbox once the attendee approves them.

The sandbox runs Python 3.12 with only the standard library. numpy, pandas, matplotlib, requests and every other third-party package are not installed, and nothing can be installed. The sandbox has no network access. A script is stopped after 5 seconds.

Write scripts that print their results. Use statistics, csv, json, collections, math and itertools for analysis, and draw charts as ASCII text.`

// noScriptReply answers a request whose scripts failed the check twice, so
// the attendee is never asked to approve code that cannot run.
const noScriptReply = "I couldn't write a script that runs in this sandbox. Try rephrasing the request."
```

Replace `Generate` (its doc comment through its closing brace) with `Generate` and `run`:

```go
// Generate runs up to three model turns and returns the assistant message.
// It returns an error rather than falling back to a mock reply.
func (c *Client) Generate(ctx context.Context, history []types.ChatMessage, prompt string) (*types.ChatMessage, error) {
	msg, err := c.run(ctx, BuildContents(history, prompt))
	if err != nil {
		return nil, err
	}
	if msg.Approval != nil && msg.Content == "" {
		msg.Content = fmt.Sprintf("I analyzed your request. To proceed, I need your approval to execute this %s script in the sandbox:", msg.Approval.Language)
	}
	return msg, nil
}

// run plays up to maxTurns model turns over contents. A proposed script that
// fails the check goes back to the model once, so the attendee is not asked
// to approve code that cannot run; one that fails again, or on the last
// turn, is dropped for noScriptReply. An approval message's Content is the
// model's text, which may be empty.
func (c *Client) run(ctx context.Context, contents []*genai.Content) (*types.ChatMessage, error) {
	if !c.cfg.Enabled() {
		return nil, ErrDisabled
	}
	sdk, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     c.cfg.Project,
		Location:    c.cfg.Location,
		HTTPClient:  &http.Client{Transport: c.transport},
		HTTPOptions: genai.HTTPOptions{BaseURL: relay.BaseURL},
	})
	if err != nil {
		return nil, fmt.Errorf("gemini: init client: %w", err)
	}

	genCfg := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: systemInstruction}}},
		Tools:             ToolDeclarations(),
	}
	retried := false
	for turn := range maxTurns {
		resp, err := sdk.Models.GenerateContent(ctx, c.cfg.Model, contents, genCfg)
		if err != nil {
			return nil, fmt.Errorf("gemini: generateContent: %w", err)
		}
		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return nil, errors.New("gemini: empty candidates")
		}
		cand := resp.Candidates[0].Content

		var text strings.Builder
		var approval *types.ApprovalRequest
		var approvalCall *genai.FunctionCall
		var research []*genai.FunctionCall
		for _, part := range cand.Parts {
			text.WriteString(part.Text)
			if part.FunctionCall == nil {
				continue
			}
			switch part.FunctionCall.Name {
			case "request_human_approval", "execute_sandbox_code":
				approval = approvalFromCall(part.FunctionCall)
				approvalCall = part.FunctionCall
			case "fetch_research_data":
				research = append(research, part.FunctionCall)
			}
		}

		if approval != nil {
			if msg := c.checkScript(ctx, approval.Language, approval.Code); msg != "" {
				if retried || turn == maxTurns-1 {
					return assistantMessage(noScriptReply, nil), nil
				}
				retried = true
				contents = append(contents, cand, &genai.Content{Role: "user", Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{Name: approvalCall.Name, Response: map[string]any{
						"error": "The script cannot run in the sandbox: " + msg + ". Fix it and call " + approvalCall.Name + " again.",
					}},
				}}})
				continue
			}
			return assistantMessage(text.String(), approval), nil
		}
		if len(research) > 0 {
			contents = append(contents, cand)
			var parts []*genai.Part
			for _, call := range research {
				topic, _ := call.Args["topic"].(string)
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
					Name: call.Name, Response: c.research(topic),
				}})
			}
			contents = append(contents, &genai.Content{Role: "user", Parts: parts})
			continue
		}
		if text.Len() > 0 {
			return assistantMessage(text.String(), nil), nil
		}
		return nil, errors.New("gemini: reply had neither text nor a tool call")
	}
	return nil, errors.New("gemini: no answer within the turn limit")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/workload/gemini/ -v`
Expected: PASS, including the unchanged `TestGenerate_BrokenScriptGoesBackToTheModelForAFix` and `TestToolDeclarations_CodeIsLimitedToTheStandardLibrary`.

- [ ] **Step 5: Commit**

```bash
git add internal/workload/gemini
git commit -m "Tell the model the sandbox's limits and drop scripts that cannot run"
```

---

### Task 3: `Corrected` and the model's fix

**Files:**
- Modify: `pkg/types/types.go:192-197`
- Create: `internal/workload/gemini/fix.go`
- Modify: `internal/workload/geminitest/geminitest.go` (new helper at the end)
- Modify: `docs/superpowers/specs/2026-09-24-demo-script-failures-design.md` (stderr tail)
- Test: `internal/workload/gemini/gemini_test.go`

**Interfaces:**
- Consumes: `c.run` (Task 2).
- Produces: `types.ExecutionResult.Corrected bool` (`json:"corrected,omitempty"`); `var gemini.ErrNoFix error`; `func (c *Client) Fix(ctx context.Context, history []types.ChatMessage, failed *types.ApprovalRequest, run *types.ExecutionResult) (*types.ChatMessage, error)` — returns a message with a non-nil `Approval` and non-empty `Content`, or an error; it does not set `Execution` or `Source`. `func geminitest.ExplainedApprovalResponse(text, summary, code, language string) string`.

- [ ] **Step 1: Write the failing tests**

Add to the end of `internal/workload/geminitest/geminitest.go` (the tests need it to compile, so it goes in with them):

```go
// ExplainedApprovalResponse is a reply in which the model explains itself in
// text and asks to run code through execute_sandbox_code.
func ExplainedApprovalResponse(text, summary, code, language string) string {
	return response(
		map[string]any{"text": text},
		map[string]any{"functionCall": map[string]any{"name": "execute_sandbox_code", "args": map[string]any{
			"summary": summary, "code": code, "language": language,
		}}},
	)
}
```

Add to `internal/workload/gemini/gemini_test.go`:

```go
var pandasFailure = &types.ExecutionResult{
	ExitCode: 1,
	Stderr:   "Traceback (most recent call last):\n  File \"script.py\", line 1, in <module>\nModuleNotFoundError: No module named 'pandas'\n",
}

var pandasScript = &types.ApprovalRequest{ApprovalID: "app-1", Code: "import pandas as pd\nprint(pd.Series([1, 2]).mean())", Language: "python"}

func TestFix_SendsTheFailedScriptAndReturnsTheCorrection(t *testing.T) {
	fake := geminitest.New(t, geminitest.ExplainedApprovalResponse(
		"pandas isn't installed here, so this version uses statistics.",
		"mean", "import statistics\nprint(statistics.mean([1, 2]))", "python"))
	c := newClient(fake, gemini.WithScriptChecker(fakeChecker()))

	msg, err := c.Fix(context.Background(), nil, pandasScript, pandasFailure)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "pandas isn't installed here, so this version uses statistics." {
		t.Errorf("Content = %q, want the model's explanation", msg.Content)
	}
	if msg.Approval == nil || msg.Approval.Code != "import statistics\nprint(statistics.mean([1, 2]))" {
		t.Fatalf("Approval = %+v, want the corrected script", msg.Approval)
	}
	body := string(fake.Requests()[0].Body)
	for _, want := range []string{"import pandas as pd", "No module named 'pandas'", "exit code 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("fix request lacks %q: %s", want, body)
		}
	}
}

func TestFix_DefaultsTheExplanation(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("mean", "print(1.5)", "python"))
	msg, err := newClient(fake, gemini.WithScriptChecker(fakeChecker())).Fix(context.Background(), nil, pandasScript, pandasFailure)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Content, "corrected") {
		t.Errorf("Content = %q, want a default explanation", msg.Content)
	}
}

func TestFix_KeepsTheTailOfLongErrorOutput(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("mean", "print(1.5)", "python"))
	run := &types.ExecutionResult{ExitCode: 1, Stderr: strings.Repeat("x", 5000) + "\nValueError: the real error"}
	if _, err := newClient(fake, gemini.WithScriptChecker(fakeChecker())).Fix(context.Background(), nil, pandasScript, run); err != nil {
		t.Fatal(err)
	}
	body := string(fake.Requests()[0].Body)
	if !strings.Contains(body, "ValueError: the real error") {
		t.Error("fix request lost the end of the error output")
	}
	if strings.Contains(body, strings.Repeat("x", 2100)) {
		t.Error("fix request carries more than 2 KiB of error output")
	}
}

func TestFix_TimeoutTellsTheModelAboutTheLimit(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("mean", "print(1.5)", "python"))
	run := &types.ExecutionResult{ExitCode: -1, Stderr: "execution context error: context deadline exceeded"}
	if _, err := newClient(fake, gemini.WithScriptChecker(fakeChecker())).Fix(context.Background(), nil, pandasScript, run); err != nil {
		t.Fatal(err)
	}
	if body := string(fake.Requests()[0].Body); !strings.Contains(body, "5 second limit") {
		t.Errorf("fix request for a timeout should mention the limit: %s", body)
	}
}

func TestFix_ReplyWithoutAScriptIsAnError(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("Sorry, I can't."))
	_, err := newClient(fake).Fix(context.Background(), nil, pandasScript, pandasFailure)
	if !errors.Is(err, gemini.ErrNoFix) {
		t.Fatalf("err = %v, want ErrNoFix", err)
	}
}

func TestFix_CorrectionThatFailsTheCheckTwiceIsAnError(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("mean", "print('a\nb')", "python"))
	c := newClient(fake, gemini.WithScriptChecker(fakeChecker("print('a\nb')")))
	if _, err := c.Fix(context.Background(), nil, pandasScript, pandasFailure); !errors.Is(err, gemini.ErrNoFix) {
		t.Fatalf("err = %v, want ErrNoFix", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/workload/gemini/ -run TestFix`
Expected: build failure, `c.Fix undefined` / `undefined: gemini.ErrNoFix`.

- [ ] **Step 3: Write the implementation**

In `pkg/types/types.go`, add the field to `ExecutionResult`:

```go
type ExecutionResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	// Corrected marks a failed run the agent has already proposed a fix for.
	Corrected bool `json:"corrected,omitempty"`
}
```

Create `internal/workload/gemini/fix.go` (license header copied from `script.go`, then):

```go
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
```

In `docs/superpowers/specs/2026-09-24-demo-script-failures-design.md`, in "The fix turn", change "carries the failed code and its stderr, truncated to 2 KiB," to "carries the failed code and the last 2 KiB of its stderr (a traceback ends with the exception),".

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/workload/gemini/ ./pkg/types/ -v -run 'TestFix|TestGenerate|Test'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/types/types.go internal/workload/gemini internal/workload/geminitest docs/superpowers/specs/2026-09-24-demo-script-failures-design.md
git commit -m "Ask the model to fix a script that failed after approval"
```

---

### Task 4: The fix turn in the actor's approve handler

**Files:**
- Modify: `internal/workload/server/server.go` (struct, `handleClearHistory`, `handleChat` turn, `handleApprove`, `describeRun`; new `startFix`, `fix`, `runMessage`)
- Modify: `internal/workload/server/server_test.go` (move to Python scripts; drop `TestApprove_FailedRunReportsStructuredError`)
- Create: `internal/workload/server/fix_test.go`

**Interfaces:**
- Consumes: `(*gemini.Client).Fix`, `types.ExecutionResult.Corrected`, `geminitest.ExplainedApprovalResponse` (Task 3).
- Produces: `/api/approve` behavior the gateway relies on in Task 5: on a failed run in Gemini mode it answers with model-call steps and then one `ChatMessage` whose `Execution.Corrected` is true and whose `Approval` is now the actor's pending approval. Otherwise it answers with one `ChatMessage` whose `Content` is the plain reply (failure) or `describeRun` (success) and `Execution` is the run.

- [ ] **Step 1: Move existing tests to Python scripts**

The actor now refuses non-Python proposals, so these tests would never get an approval. In `internal/workload/server/server_test.go`:

`TestFineGrainedMutexLocking` (line ~580):

```go
		fake := geminitest.New(t, geminitest.ApprovalResponse("Slow background job", "import time\ntime.sleep(0.4)", "python"))
```

`TestApprove_RunsStoredCodeAndIgnoresRequestCode`:

```go
func TestApprove_RunsStoredCodeAndIgnoresRequestCode(t *testing.T) {
	s, id := approvalServer(t, "print('stored')", "python")

	rec := postApprove(t, s.Handler(), types.ApprovalResponse{
		ApprovalID: id, Decision: types.DecisionApprove, Code: "print('injected')", Language: "python",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var msg types.ChatMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &msg)
	if msg.Execution == nil || msg.Execution.Stdout != "stored\n" || msg.Execution.ExitCode != 0 {
		t.Errorf("Execution = %+v, want stdout from the stored code", msg.Execution)
	}
}
```

`TestApprove_WrongIDIsConflictAndKeepsPending`: `approvalServer(t, "print('stored')", "python")`.
`TestApprove_ReplayIsConflict`: `approvalServer(t, "print('once')", "python")`.

Delete `TestApprove_FailedRunReportsStructuredError`: a failed run in Gemini mode now starts a fix turn, which `fix_test.go` covers.

- [ ] **Step 2: Write the failing tests**

Create `internal/workload/server/fix_test.go` (license header from `server_test.go`, then):

```go
package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-substrate/agent-swarm/internal/workload/geminitest"
	"github.com/agent-substrate/agent-swarm/internal/workload/server"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// failingScript passes the check and exits 1 with "boom" on stderr.
const (
	failingScript = "import sys\nsys.exit('boom')"
	fixedScript   = "print('fixed')"
)

// fixServer returns an actor in Gemini mode whose model answers with bodies
// in order, the first of which a chat turn has already left pending.
func fixServer(t *testing.T, bodies ...string) (*server.WorkloadServer, *geminitest.Server, string) {
	t.Helper()
	fake := geminitest.New(t, bodies...)
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(geminiConfig))
	msg := postChat(t, s.Handler(), fake.URL, "k.mac", "do the thing")
	if msg.Approval == nil {
		t.Fatalf("chat reply carried no approval: %+v", msg)
	}
	return s, fake, msg.Approval.ApprovalID
}

// approveDriven approves id, relaying the actor's model calls to upstream
// the way the gateway would.
func approveDriven(t *testing.T, h http.Handler, upstream, id string) types.ChatMessage {
	t.Helper()
	body, _ := json.Marshal(types.ApprovalResponse{ApprovalID: id, Decision: types.DecisionApprove})
	rec := geminitest.Drive(t, h, upstream, httptest.NewRequest(http.MethodPost, "/api/approve", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body %s", rec.Code, rec.Body)
	}
	var msg types.ChatMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestApprove_FailedRunGetsAFixToApprove(t *testing.T) {
	s, fake, id := fixServer(t,
		geminitest.ApprovalResponse("run it", failingScript, "python"),
		geminitest.ExplainedApprovalResponse("It exited on purpose; this version prints instead.", "run it", fixedScript, "python"),
	)

	msg := approveDriven(t, s.Handler(), fake.URL, id)

	if msg.Execution == nil || !msg.Execution.Corrected || msg.Execution.ExitCode != 1 || !strings.Contains(msg.Execution.Stderr, "boom") {
		t.Errorf("Execution = %+v, want the failed run marked corrected", msg.Execution)
	}
	if msg.Approval == nil || msg.Approval.Code != fixedScript {
		t.Fatalf("Approval = %+v, want the fix", msg.Approval)
	}
	if msg.Content != "It exited on purpose; this version prints instead." {
		t.Errorf("Content = %q, want the model's explanation", msg.Content)
	}
	if msg.Source != types.SourceGemini {
		t.Errorf("Source = %q, want gemini", msg.Source)
	}
	if p := s.PendingApproval(); p == nil || p.ApprovalID != msg.Approval.ApprovalID {
		t.Errorf("PendingApproval = %+v, want the fix", p)
	}
	if body := string(fake.Requests()[1].Body); !strings.Contains(body, "sys.exit") || !strings.Contains(body, "boom") {
		t.Errorf("fix request lacks the failed script or its stderr: %s", body)
	}
	// History gains the fix reply, not a separate failed-run message.
	var assistant []types.ChatMessage
	for _, m := range s.History() {
		if m.Sender == types.SenderAssistant {
			assistant = append(assistant, m)
		}
	}
	if len(assistant) != 2 || assistant[1].ID != msg.ID {
		t.Errorf("assistant history = %+v, want the proposal and the fix", assistant)
	}
}

func TestApprove_FixThatFailsGetsThePlainReplyWithoutAModelCall(t *testing.T) {
	s, fake, id := fixServer(t,
		geminitest.ApprovalResponse("run it", failingScript, "python"),
		geminitest.ApprovalResponse("run it", failingScript, "python"),
	)
	fix := approveDriven(t, s.Handler(), fake.URL, id)
	if fix.Approval == nil {
		t.Fatalf("first failure got no fix: %+v", fix)
	}
	calls := len(fake.Requests())

	msg := approveDriven(t, s.Handler(), fake.URL, fix.Approval.ApprovalID)

	if n := len(fake.Requests()) - calls; n != 0 {
		t.Errorf("made %d model calls for a failed fix, want none", n)
	}
	if msg.Approval != nil || msg.Execution == nil || msg.Execution.Corrected || msg.Execution.ExitCode != 1 {
		t.Errorf("reply = %+v, want the failed run with no fix", msg)
	}
	if msg.Content != "That approach didn't work in this sandbox. Try asking for something simpler." {
		t.Errorf("Content = %q, want the plain reply", msg.Content)
	}
	if s.PendingApproval() != nil {
		t.Error("a failed fix left an approval pending")
	}
}

func TestApprove_FixModelErrorGetsThePlainReply(t *testing.T) {
	s, _, id := fixServer(t, geminitest.ApprovalResponse("run it", failingScript, "python"))

	// An empty upstream answers every model call with 502.
	msg := approveDriven(t, s.Handler(), "", id)

	if msg.Approval != nil || msg.Execution == nil || msg.Execution.Corrected {
		t.Errorf("reply = %+v, want the failed run with no fix", msg)
	}
	if !strings.Contains(msg.Content, "didn't work in this sandbox") || msg.Source == types.SourceMockFallback {
		t.Errorf("reply = %+v, want the plain reply, not the mock engine", msg)
	}
}

func TestApprove_NewRequestGetsItsOwnFix(t *testing.T) {
	s, fake, id := fixServer(t,
		geminitest.ApprovalResponse("run it", failingScript, "python"), // first request
		geminitest.ApprovalResponse("run it", failingScript, "python"), // its fix, which fails
		geminitest.ApprovalResponse("run it", failingScript, "python"), // second request
		geminitest.ExplainedApprovalResponse("Fixed.", "run it", fixedScript, "python"),
	)
	fix := approveDriven(t, s.Handler(), fake.URL, id)
	approveDriven(t, s.Handler(), fake.URL, fix.Approval.ApprovalID)

	next := postChat(t, s.Handler(), fake.URL, "k.mac", "try again")
	if next.Approval == nil {
		t.Fatalf("second request got no approval: %+v", next)
	}
	msg := approveDriven(t, s.Handler(), fake.URL, next.Approval.ApprovalID)

	if msg.Execution == nil || !msg.Execution.Corrected || msg.Approval == nil || msg.Approval.Code != fixedScript {
		t.Errorf("reply = %+v, want a fix for the new request", msg)
	}
}

func TestApprove_SuccessfulRunIsUnchanged(t *testing.T) {
	s, fake, id := fixServer(t, geminitest.ApprovalResponse("run it", fixedScript, "python"))
	calls := len(fake.Requests())

	msg := approveDriven(t, s.Handler(), fake.URL, id)

	if len(fake.Requests()) != calls {
		t.Error("a successful run made a model call")
	}
	if msg.Execution == nil || msg.Execution.Corrected || msg.Execution.Stdout != "fixed\n" || !strings.HasPrefix(msg.Content, "Execution completed in ") {
		t.Errorf("reply = %+v, want the run's output", msg)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/workload/server/ -run TestApprove -v`
Expected: FAIL — `TestApprove_FailedRunGetsAFixToApprove` gets a message with no approval and `Content` starting "Execution completed", `TestApprove_FixThatFailsGetsThePlainReplyWithoutAModelCall` fails at "first failure got no fix". `TestApprove_SuccessfulRunIsUnchanged` passes.

- [ ] **Step 4: Write the implementation**

In `internal/workload/server/server.go`, add a field to `WorkloadServer` after `pendingApproval`:

```go
	pendingApproval *types.ApprovalRequest
	pendingFix      bool // pendingApproval fixes a failed run, so it gets no fix of its own
```

In `handleClearHistory`, after `s.pendingApproval = nil`, add `s.pendingFix = false`.

In `handleChat`'s turn function, where the reply's approval becomes pending:

```go
		if assistantMsg.Approval != nil {
			s.pendingApproval = assistantMsg.Approval
			s.pendingFix = false
		}
```

In `handleApprove`, record and clear the flag with the pending approval:

```go
	s.pendingApproval = nil
	wasFix := s.pendingFix
	s.pendingFix = false
```

and replace everything after the reject branch's closing brace (from `s.mu.Unlock()` through the end of `describeRun`) with:

```go
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	exec := runResult(s.executor.Execute(ctx, pending.Language, pending.Code))
	cancel()

	if exec.ExitCode != 0 && !wasFix && !s.forceMock && s.gemini != nil {
		s.startFix(w, r, pending, exec)
		return
	}

	resultMsg := runMessage(exec)
	s.mu.Lock()
	s.history = append(s.history, resultMsg)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resultMsg)
}

// startFix runs a relayed turn in which the model explains a failed run and
// proposes a corrected script, and answers with the turn's first step.
func (s *WorkloadServer) startFix(w http.ResponseWriter, r *http.Request, failed *types.ApprovalRequest, run *types.ExecutionResult) {
	select {
	case s.turnSlot <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	t := relay.NewTurn()
	s.mu.Lock()
	historyCopy := append([]types.ChatMessage(nil), s.history...)
	s.turn = t
	s.mu.Unlock()

	// Like a chat turn, the fix outlives this request.
	t.Run(context.Background(), func(ctx context.Context) *types.ChatMessage {
		msg := s.fix(ctx, historyCopy, failed, run)
		s.mu.Lock()
		if msg.Approval != nil {
			s.pendingApproval = msg.Approval
			s.pendingFix = true
		}
		s.history = append(s.history, *msg)
		s.turn = nil
		s.mu.Unlock()
		<-s.turnSlot
		return msg
	})
	writeStep(w, r, t)
}

// fix asks the model to correct a failed run. Its reply carries the run,
// marked corrected, and the fix to approve. When the model cannot fix it,
// the reply is the plain failed-run message; the mock engine's script would
// have nothing to do with the request.
func (s *WorkloadServer) fix(ctx context.Context, history []types.ChatMessage, failed *types.ApprovalRequest, run *types.ExecutionResult) *types.ChatMessage {
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()
	msg, err := s.gemini.Fix(ctx, history, failed, run)
	if err != nil {
		log.Printf("gemini fix failed, replying with the failed run: %v", err)
		plain := runMessage(run)
		return &plain
	}
	corrected := *run
	corrected.Corrected = true
	msg.Execution = &corrected
	msg.Source = types.SourceGemini
	return msg
}

// runResult folds an executor error into the result. ExitCode -1 means the
// code did not run to completion.
func runResult(res *types.ExecutionResult, err error) *types.ExecutionResult {
	out := types.ExecutionResult{ExitCode: -1}
	if res != nil {
		out = *res
	}
	if err != nil {
		out.ExitCode = -1
		out.Stderr = strings.TrimSpace(out.Stderr + "\n" + err.Error())
	}
	return &out
}

// failedRunReply answers a failed run the agent will not fix.
const failedRunReply = "That approach didn't work in this sandbox. Try asking for something simpler."

// runMessage reports a run: its output when it succeeded, otherwise the
// plain reply, with the run attached for pages to show as much as they want.
func runMessage(exec *types.ExecutionResult) types.ChatMessage {
	content := failedRunReply
	if exec.ExitCode == 0 {
		content = describeRun(exec)
	}
	return types.ChatMessage{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   content,
		Timestamp: time.Now().Unix(),
		Execution: exec,
	}
}

func describeRun(e *types.ExecutionResult) string {
	s := fmt.Sprintf("Execution completed in %dms (exit code %d):\n\n%s", e.DurationMs, e.ExitCode, e.Stdout)
	if e.Stderr != "" {
		s += "\nStderr:\n" + e.Stderr
	}
	return s
}
```

(`runResult` is unchanged; it only moves below `fix`. The old `defer cancel()` becomes an explicit `cancel()` so the 5s run deadline does not also bound the fix turn.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/workload/... -race`
Expected: PASS, including `TestApprove_IncludeStderr` (success with stderr still lists `Stderr:`) and `TestApprove_Execute` (mock mode).

- [ ] **Step 6: Commit**

```bash
git add internal/workload/server
git commit -m "Have the actor propose a fix when an approved script fails"
```

---

### Task 5: The gateway keeps corrected runs off the spotlight and gives each fix its own card

**Files:**
- Modify: `cmd/conf-gateway/internal/gateway/actorturn.go:142-182` (in `onActorReply`)
- Modify: `cmd/conf-gateway/internal/gateway/contract_test.go` (move to Python; new test)

**Interfaces:**
- Consumes: the actor's `/api/approve` reply (Task 4); `types.ExecutionResult.Corrected`.
- Produces: for a reply with a `Corrected` execution and an approval: `GetLastResult` unchanged; the current action's feed entry gains the corrected `Execution` and activity idle; a new action (`act-fix-<attendee>-<nanos>`) becomes `currentActionID` and carries the fix's code, approval ID and explanation, so the next approval's execution merges into it.

- [ ] **Step 1: Move the contract tests to Python scripts**

In `cmd/conf-gateway/internal/gateway/contract_test.go`:

`TestContract_ApprovalRunsActorsOwnCode`:

```go
	env := newContractEnv(t, geminitest.New(t, geminitest.ApprovalResponse("say it", "print('from-model')", "python")))
```

and its approve body `Code: "print('injected')",`. The assertion on `"from-model\n"` stays.

`TestContract_GatewayRejectsUnknownApprovalBeforeActor`: `geminitest.ApprovalResponse("x", "print('x')", "python")`.
`TestContract_ExecutionIsRecordedFromStructuredField`: `geminitest.ApprovalResponse("x", "print('structured')", "python")` (assertion on `"structured\n"` stays).
`TestContract_ResetClearsActorHistory`: `geminitest.ApprovalResponse("run it", "print('old')", "python")`.

- [ ] **Step 2: Write the failing test**

Add to `cmd/conf-gateway/internal/gateway/contract_test.go`:

```go
func TestContract_FailedRunIsCorrectedAndTheFixCanBeApproved(t *testing.T) {
	const failing, fixed = "import sys\nsys.exit('boom')", "print('fixed')"
	env := newContractEnv(t, geminitest.New(t,
		geminitest.ApprovalResponse("run it", failing, "python"),
		geminitest.ExplainedApprovalResponse("It exited on purpose; this one prints.", "run it", fixed, "python"),
	))
	chat := decodeMsg(t, env.post(t, "attendee-10", "/api/chat", types.ChatRequest{Message: "run"}))

	fix := decodeMsg(t, env.post(t, "attendee-10", "/api/approve", types.ApprovalResponse{
		ApprovalID: chat.Approval.ApprovalID, Decision: types.DecisionApprove,
	}))

	if fix.Execution == nil || !fix.Execution.Corrected || fix.Approval == nil || fix.Approval.Code != fixed {
		t.Fatalf("approve reply = %+v, want the corrected run and the fix", fix)
	}
	if res, ok := env.gw.GetLastResult("attendee-10"); ok {
		t.Errorf("GetLastResult = %+v, want the corrected run kept off the spotlight", res)
	}
	var approved, proposed *types.ActionLogEntry
	feed := env.gw.GetFeedHistory()
	for i := range feed {
		switch {
		case feed[i].AttendeeID != "attendee-10":
		case feed[i].ApprovalID == chat.Approval.ApprovalID:
			approved = &feed[i]
		case feed[i].ApprovalID == fix.Approval.ApprovalID:
			proposed = &feed[i]
		}
	}
	if approved == nil || approved.Code != failing || approved.Execution == nil || !approved.Execution.Corrected {
		t.Errorf("approved entry = %+v, want the failing code with its corrected run", approved)
	}
	if proposed == nil || proposed.Code != fixed || proposed.Execution != nil || proposed.Activity != types.ActivityAwaitingApproval {
		t.Errorf("fix entry = %+v, want its own card awaiting approval", proposed)
	}

	res := decodeMsg(t, env.post(t, "attendee-10", "/api/approve", types.ApprovalResponse{
		ApprovalID: fix.Approval.ApprovalID, Decision: types.DecisionApprove,
	}))

	if res.Execution == nil || res.Execution.Stdout != "fixed\n" {
		t.Errorf("fix run = %+v, want its output", res.Execution)
	}
	if lr, ok := env.gw.GetLastResult("attendee-10"); !ok || lr.Stdout != "fixed\n" {
		t.Errorf("GetLastResult = %+v, %v, want the fix's run", lr, ok)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./cmd/conf-gateway/internal/gateway/ -run TestContract -v`
Expected: `TestContract_FailedRunIsCorrectedAndTheFixCanBeApproved` FAILS on `GetLastResult` (the corrected run reached the spotlight) and on the feed entries (the fix merged into the approved card, so there is no separate `proposed` entry and the approved entry's code is the fix). The other contract tests pass.

- [ ] **Step 4: Write the implementation**

In `cmd/conf-gateway/internal/gateway/actorturn.go`, `onActorReply`, replace the `newExec` block:

```go
	var newExec types.ExecutionResult
	hasNewExec := hasChatMsg && chatMsg.Execution != nil
	if hasNewExec {
		newExec = *chatMsg.Execution
		// The agent already proposed a fix for a corrected run, so the
		// spotlight keeps what it showed.
		if !newExec.Corrected {
			g.mu.Lock()
			g.lastResults[attendeeID] = &newExec
			g.mu.Unlock()
		}
	}
```

and at the top of the `if isWaitingForApproval {` branch, before `g.mu.Lock()`, add:

```go
		if hasNewExec && newExec.Corrected {
			// The failed run closes the approved action; the fix opens its own,
			// so it does not overwrite the approved card.
			g.RecordActionEntry(types.ActionLogEntry{
				ID:         actionID,
				AttendeeID: attendeeID,
				Timestamp:  time.Now().Unix(),
				Execution:  &newExec,
				WorkerPod:  workerPod,
				Activity:   types.ActivityIdle,
			})
			actionID = fmt.Sprintf("act-fix-%s-%d", attendeeID, time.Now().UnixNano())
			g.mu.Lock()
			g.currentActionID[attendeeID] = actionID
			g.mu.Unlock()
		}
```

The existing approval entry below then records the fix under the new `actionID`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/conf-gateway/... -race`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/conf-gateway/internal/gateway
git commit -m "Keep corrected runs off the spotlight and give each fix its own card"
```

---

### Task 6: What the phone, feed and stage show for a failed run

**Files:**
- Modify: `cmd/conf-gateway/static/actorview.js` (new `run`, export)
- Modify: `cmd/conf-gateway/static/chat.js` (`renderReply`, `appendRunDetails`; `submitPrompt` and the approve handler use them)
- Modify: `cmd/conf-gateway/static/chat.css`
- Modify: `cmd/conf-gateway/static/feed.js:190-208`, `feed.css`
- Modify: `cmd/conf-gateway/static/stage.js:753-767` (spotlight), `:1002-1020` (cards), `stage.css`
- Modify: `docs/superpowers/specs/2026-09-24-demo-script-failures-design.md` (last line, not first)
- Test: `cmd/conf-gateway/internal/gateway/static_test.go`

**Interfaces:**
- Consumes: `execution.corrected` and `execution.exit_code` in JSON (Tasks 3-5).
- Produces: `window.ActorView.run(exec)` → `null` for a successful run, otherwise `{label, cls, detail}` with `cls` `run-corrected` ("Self-corrected") or `run-failed` ("Didn't run") and `detail` the last non-empty line of stderr.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/conf-gateway/internal/gateway/static_test.go`:

```go
// TestStatic_FailedRunsGoThroughActorView checks that every page presents a
// failed run through the shared ActorView.run mapping, and that the phone
// renders the approve reply like a chat reply, so a fix's approval appears.
func TestStatic_FailedRunsGoThroughActorView(t *testing.T) {
	if js := readStatic(t, "actorview.js"); !strings.Contains(js, "run: run") {
		t.Error("actorview.js does not export run")
	}
	for _, page := range []string{"chat.js", "feed.js", "stage.js"} {
		if js := readStatic(t, page); !strings.Contains(js, "ActorView.run(") {
			t.Errorf("%s does not use ActorView.run", page)
		}
	}
	chat := readStatic(t, "chat.js")
	for _, want := range []string{"renderReply(chatMsg)", "renderReply(resultMsg)", "createElement('details')", "What went wrong"} {
		if !strings.Contains(chat, want) {
			t.Errorf("chat.js lacks %q", want)
		}
	}
}

// TestStatic_FailedRunsAreStyled checks that each page styles the classes
// ActorView.run returns.
func TestStatic_FailedRunsAreStyled(t *testing.T) {
	for _, sheet := range []string{"feed.css", "stage.css"} {
		css := readStatic(t, sheet)
		for _, cls := range []string{".term-exit-status.run-corrected", ".term-exit-status.run-failed", ".term-detail"} {
			if !strings.Contains(css, cls) {
				t.Errorf("%s has no %s rule", sheet, cls)
			}
		}
	}
	if css := readStatic(t, "stage.css"); !strings.Contains(css, ".drawer-terminal-box.run-failed") {
		t.Error("stage.css does not style a failed run in the spotlight")
	}
	css := readStatic(t, "chat.css")
	for _, cls := range []string{".run-note", ".run-details"} {
		if !strings.Contains(css, cls) {
			t.Errorf("chat.css has no %s rule", cls)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/conf-gateway/internal/gateway/ -run 'TestStatic_FailedRuns' -v`
Expected: FAIL on every assertion.

- [ ] **Step 3: Add `ActorView.run`**

In `cmd/conf-gateway/static/actorview.js`, after `fleetIDs`, add:

```js
  // run maps an execution result to how pages present a failed run: a chip
  // {label, cls} and one line of detail, the last line of stderr, which for
  // a Python traceback is the exception. A successful run maps to null.
  function run(exec) {
    if (!exec || (exec.exit_code === 0 && !exec.corrected)) return null;
    const lines = (exec.stderr || '').trim().split('\n');
    const detail = lines[lines.length - 1].trim();
    if (exec.corrected) return { label: 'Self-corrected', cls: 'run-corrected', detail: detail };
    return { label: "Didn't run", cls: 'run-failed', detail: detail };
  }
```

and change the export to:

```js
  window.ActorView = { state: state, activity: activity, source: source, badge: badge, fleetIDs: fleetIDs, run: run };
```

- [ ] **Step 4: Render replies on the phone**

In `cmd/conf-gateway/static/chat.js`, after `appendSystemMessage`, add:

```js
  // renderReply shows an assistant reply: its text, a failed run's stderr
  // collapsed under a note, and any approval it asks for.
  function renderReply(msg) {
    const msgDiv = appendMessage('assistant', msg.content, msg.timestamp, msg.source);
    const failure = window.ActorView.run(msg.execution);
    if (failure) appendRunDetails(msgDiv, failure, msg.execution.stderr);
    if (msg.approval) appendApprovalCard(msg.approval, msgDiv);
    return msgDiv;
  }

  function appendRunDetails(msgDiv, failure, stderr) {
    const bubble = msgDiv.querySelector('.message-bubble');
    const note = document.createElement('div');
    note.className = 'run-note ' + failure.cls;
    note.textContent = failure.label;
    const details = document.createElement('details');
    details.className = 'run-details';
    const summary = document.createElement('summary');
    summary.textContent = 'What went wrong';
    const pre = document.createElement('pre');
    pre.textContent = stderr || '';
    details.appendChild(summary);
    details.appendChild(pre);
    bubble.appendChild(note);
    bubble.appendChild(details);
  }
```

In `submitPrompt`, replace

```js
      const chatMsg = await resp.json();
      const msgDiv = appendMessage('assistant', chatMsg.content, chatMsg.timestamp, chatMsg.source);

      if (chatMsg.approval) {
        appendApprovalCard(chatMsg.approval, msgDiv);
      }
```

with

```js
      const chatMsg = await resp.json();
      renderReply(chatMsg);
```

In `handleApprovalDecision`, replace

```js
      const resultMsg = await resp.json();
      appendMessage('assistant', resultMsg.content, resultMsg.timestamp, resultMsg.source);
```

with

```js
      const resultMsg = await resp.json();
      renderReply(resultMsg);
```

Append to `cmd/conf-gateway/static/chat.css`:

```css
/* A failed run: a note, with its stderr collapsed */
.run-note {
  margin-top: 0.6rem;
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--neon-amber);
}

.run-details {
  margin-top: 0.3rem;
  font-size: 0.75rem;
  color: var(--text-muted);
}

.run-details summary {
  cursor: pointer;
}

.run-details pre {
  margin-top: 0.3rem;
  max-height: 12rem;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono);
}
```

- [ ] **Step 5: Render failed runs in feed and stage cards**

In `cmd/conf-gateway/static/feed.js`, replace the "Execution Terminal Section" block with:

```js
    // Execution Terminal Section. A failed run shows one line, not a dump.
    let terminalHTML = '';
    const failure = window.ActorView.run(entry.execution);
    if (failure) {
      terminalHTML = `
        <div class="card-terminal-section">
          <div class="terminal-header-bar">
            <span class="terminal-title">💻 gVisor Sandbox Output</span>
            <div class="terminal-metrics">
              <span class="term-exit-status ${failure.cls}">${escapeHtml(failure.label)}</span>
            </div>
          </div>
          <div class="terminal-body">
            ${failure.detail ? `<div class="term-detail">${escapeHtml(failure.detail)}</div>` : ''}
          </div>
        </div>
      `;
    } else if (entry.execution) {
      terminalHTML = `
        <div class="card-terminal-section">
          <div class="terminal-header-bar">
            <span class="terminal-title">💻 gVisor Sandbox Output</span>
            <div class="terminal-metrics">
              <span class="term-exit-status exit-0">Exit ${entry.execution.exit_code}</span>
              <span class="term-duration">${entry.execution.duration_ms}ms</span>
            </div>
          </div>
          <div class="terminal-body">
            ${entry.execution.stdout ? `<div class="term-stdout">${escapeHtml(entry.execution.stdout)}</div>` : ''}
            ${entry.execution.stderr ? `<div class="term-stderr">${escapeHtml(entry.execution.stderr)}</div>` : ''}
          </div>
        </div>
      `;
    }
```

In `cmd/conf-gateway/static/stage.js`, replace its "Execution Terminal Section" block with the same code, using `escapeHTML` in place of `escapeHtml` (stage.js's helper name).

After the `.term-exit-status.exit-err` rule in both `feed.css` and `stage.css`, add:

```css
.term-exit-status.run-corrected,
.term-exit-status.run-failed {
  background: rgba(245, 158, 11, 0.2);
  color: var(--neon-amber);
}

.term-detail {
  color: var(--neon-amber);
  white-space: pre-wrap;
  word-break: break-word;
}
```

- [ ] **Step 6: Render a failed run in the spotlight**

In `cmd/conf-gateway/static/stage.js`, replace the "Terminal output console" block with:

```js
    // Terminal output console. A failed run shows one line in amber; a
    // corrected one never reaches last_result.
    if (el.spotlightOutput) {
      const failure = data.last_result ? window.ActorView.run(data.last_result) : null;
      el.spotlightOutput.classList.toggle('run-failed', !!failure);
      if (failure) {
        el.spotlightOutput.textContent = failure.label.toUpperCase() + (failure.detail ? '\n' + failure.detail : '');
      } else if (data.last_result) {
        const res = data.last_result;
        let outputText = '';
        if (res.stdout) outputText += res.stdout;
        if (res.stderr) outputText += (outputText ? '\n[STDERR]\n' : '') + res.stderr;
        if (!outputText) outputText = '(execution returned 0 output)';
        const meta = `\n\n--- Process exited code ${res.exit_code ?? 0} (${res.duration_ms ?? 0} ms) ---`;
        el.spotlightOutput.textContent = outputText + meta;
      } else if (a) {
        el.spotlightOutput.textContent = '[' + a.label.toUpperCase() + '] Waiting on the actor to finish...';
      } else {
        el.spotlightOutput.textContent = 'Awaiting execution output...';
      }
    }
```

After the `.drawer-terminal-box` rule in `stage.css`, add:

```css
.drawer-terminal-box.run-failed {
  color: var(--neon-amber);
}
```

In `docs/superpowers/specs/2026-09-24-demo-script-failures-design.md`, section 3, replace each "the first line of stderr" (three places) with "the last non-empty line of stderr (for a Python traceback, the exception)".

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./cmd/conf-gateway/... -v -run TestStatic`
Expected: PASS.

- [ ] **Step 8: Check `ActorView.run` by hand**

There is no JS test runner in this repo. If `node` is installed, run:

```bash
node -e '
global.window = {};
require("./cmd/conf-gateway/static/actorview.js");
const run = window.ActorView.run;
const tb = "Traceback (most recent call last):\n  File \"script.py\", line 1\nModuleNotFoundError: No module named '"'"'pandas'"'"'\n";
console.log(JSON.stringify([run(null), run({exit_code: 0, stderr: "warn"}), run({exit_code: 1, stderr: tb}), run({exit_code: 1, stderr: tb, corrected: true}), run({exit_code: -1, stderr: ""})]));
'
```

Expected: `[null,null,{"label":"Didn't run","cls":"run-failed","detail":"ModuleNotFoundError: No module named 'pandas'"},{"label":"Self-corrected","cls":"run-corrected","detail":"ModuleNotFoundError: No module named 'pandas'"},{"label":"Didn't run","cls":"run-failed","detail":""}]`. If `node` is missing, say so in the task report.

- [ ] **Step 9: Commit**

```bash
git add cmd/conf-gateway/static cmd/conf-gateway/internal/gateway/static_test.go docs/superpowers/specs/2026-09-24-demo-script-failures-design.md
git commit -m "Show failed runs as a self-correction, not a stderr dump"
```

---

### Task 7: Whole-branch verification

**Files:** none changed unless a check fails.

- [ ] **Step 1: Run the full checks**

```bash
make verify
make test
make verify-local
```

Expected: all three pass. `make verify` covers gofmt, vet, lint, `go mod tidy`, license headers, manifests and shellcheck; `make verify-local` runs the 13 end-to-end steps in mock mode, whose fixed script succeeds, so its approve path is unchanged.

- [ ] **Step 2: Look at the phone by hand**

Run `make run-local`, open `http://127.0.0.1:18090/chat`, send a prompt and approve the mock script. Confirm the reply renders as before (mock mode never fails, so this checks that `renderReply` did not break the success path). The failure rendering is exercised by the contract test and Step 8 of Task 6; seeing it live needs a cluster with Vertex AI.

- [ ] **Step 3: Commit any fixes**

If a check needed a change, commit it with a message naming what it fixed.

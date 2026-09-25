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

package gemini_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/agent-substrate/agent-swarm/internal/workload/gemini"
	"github.com/agent-substrate/agent-swarm/internal/workload/geminitest"
	"github.com/agent-substrate/agent-swarm/internal/workload/relay"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

var testConfig = gemini.Config{Project: "proj", Location: "loc", Model: "gemini-2.5-flash"}

func newClient(fake *geminitest.Server, opts ...gemini.Option) *gemini.Client {
	return gemini.New(testConfig, append([]gemini.Option{gemini.WithTransport(fake.Transport())}, opts...)...)
}

func TestGenerate_SendsVertexPathWithoutCredentials(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("hello"))
	c := newClient(fake)

	msg, err := c.Generate(context.Background(), nil, "hi")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want hello", msg.Content)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	wantPath := "/v1beta1/projects/proj/locations/loc/publishers/google/models/gemini-2.5-flash:generateContent"
	if reqs[0].Method != "POST" || reqs[0].Path != wantPath {
		t.Errorf("request = %s %s, want POST %s", reqs[0].Method, reqs[0].Path, wantPath)
	}
	if reqs[0].Authorization != "" {
		t.Errorf("Authorization = %q, want none: the gateway adds credentials", reqs[0].Authorization)
	}
}

func TestGenerate_DefaultTransportNeedsARelayedTurn(t *testing.T) {
	c := gemini.New(testConfig)
	if _, err := c.Generate(context.Background(), nil, "hi"); !errors.Is(err, relay.ErrNoTurn) {
		t.Fatalf("err = %v, want relay.ErrNoTurn", err)
	}
}

func TestGenerate_DisabledConfig(t *testing.T) {
	c := gemini.New(gemini.Config{})
	if _, err := c.Generate(context.Background(), nil, "hi"); !errors.Is(err, gemini.ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

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

func TestGenerate_ResearchToolLoopsBackToModel(t *testing.T) {
	fake := geminitest.New(t,
		geminitest.FunctionCallResponse("fetch_research_data", map[string]any{"topic": "cpu"}),
		geminitest.TextResponse("analysis done"),
	)
	c := newClient(fake,
		gemini.WithResearchDataHandler(func(topic string) map[string]any {
			return map[string]any{"topic": topic, "values": []int{1, 2}}
		}))

	msg, err := c.Generate(context.Background(), nil, "analyze cpu")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "analysis done" {
		t.Errorf("Content = %q", msg.Content)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 || !strings.Contains(string(reqs[1].Body), `"functionResponse"`) {
		t.Errorf("second request should carry the function response; got %d requests", len(reqs))
	}
}

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

func TestGenerate_UpstreamErrorIsReturned(t *testing.T) {
	fake := geminitest.NewStatus(t, 500)
	c := newClient(fake)
	if _, err := c.Generate(context.Background(), nil, "hi"); err == nil {
		t.Fatal("err = nil, want the upstream failure")
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("VERTEX_PROJECT", "p")
	t.Setenv("VERTEX_LOCATION", "l")
	t.Setenv("GEMINI_MODEL", "")
	cfg := gemini.ConfigFromEnv()
	want := gemini.Config{Project: "p", Location: "l", Model: "gemini-2.5-flash"}
	if cfg != want {
		t.Errorf("ConfigFromEnv() = %+v, want %+v", cfg, want)
	}
	if !cfg.Enabled() {
		t.Error("Enabled() = false, want true")
	}
	t.Setenv("VERTEX_PROJECT", "")
	if gemini.ConfigFromEnv().Enabled() {
		t.Error("Enabled() = true with no project, want false")
	}
}

func TestBuildContents_MergesConsecutiveRolesAndAppendsPrompt(t *testing.T) {
	history := []types.ChatMessage{
		{Sender: types.SenderUser, Content: "a"},
		{Sender: types.SenderSystem, Content: "b"},
		{Sender: types.SenderAssistant, Content: "c"},
	}
	got := gemini.BuildContents(history, "d")
	if len(got) != 3 || got[0].Role != "user" || len(got[0].Parts) != 2 || got[1].Role != "model" || got[2].Parts[0].Text != "d" {
		t.Fatalf("BuildContents = %+v", got)
	}
}

// TestBuildContents_PromptAlreadyLastInHistoryIsNotDuplicated covers the
// server's handleChat path, which appends the user's turn to history before
// calling Generate. BuildContents must recognize the prompt is already the
// last history entry and not append it a second time.
func TestBuildContents_PromptAlreadyLastInHistoryIsNotDuplicated(t *testing.T) {
	history := []types.ChatMessage{
		{Sender: types.SenderUser, Content: "first prompt"},
		{Sender: types.SenderAssistant, Content: "response 1"},
		{Sender: types.SenderUser, Content: "second prompt"},
	}
	got := gemini.BuildContents(history, "second prompt")
	if len(got) != 3 {
		t.Fatalf("BuildContents = %+v, want 3 contents (prompt not duplicated)", got)
	}
	last := got[len(got)-1]
	if last.Role != "user" || len(last.Parts) != 1 || last.Parts[0].Text != "second prompt" {
		t.Fatalf("last content = %+v, want a single-part user content with the prompt once", last)
	}
}

// The sandbox image is python:3.12-slim, so a script that imports numpy or
// another third-party package fails.
func TestToolDeclarations_CodeIsLimitedToTheStandardLibrary(t *testing.T) {
	for _, tool := range gemini.ToolDeclarations() {
		for _, fn := range tool.FunctionDeclarations {
			code, ok := fn.Parameters.Properties["code"]
			if !ok {
				continue
			}
			if !strings.Contains(code.Description, "standard library") {
				t.Errorf("%s code description = %q, want it to limit scripts to the standard library", fn.Name, code.Description)
			}
		}
	}
}

// fakeChecker reports a syntax error for the scripts in broken.
func fakeChecker(broken ...string) gemini.ScriptChecker {
	return func(_ context.Context, _, code string) string {
		for _, b := range broken {
			if code == b {
				return "SyntaxError: unterminated string literal (line 1)"
			}
		}
		return ""
	}
}

func TestGenerate_BrokenScriptGoesBackToTheModelForAFix(t *testing.T) {
	fake := geminitest.New(t,
		geminitest.ApprovalResponse("sum", "print('a\nb')", "python"),
		geminitest.ApprovalResponse("sum", "print('a\\nb')", "python"),
	)
	c := newClient(fake, gemini.WithScriptChecker(fakeChecker("print('a\nb')")))

	msg, err := c.Generate(context.Background(), nil, "sum it")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil || msg.Approval.Code != "print('a\\nb')" {
		t.Fatalf("Approval = %+v, want the fixed script", msg.Approval)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	if body := string(reqs[1].Body); !strings.Contains(body, `"functionResponse"`) || !strings.Contains(body, "unterminated string literal") {
		t.Errorf("retry request should report the syntax error; body = %s", body)
	}
}

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

func TestGenerate_MalformedCallIsAskedAgain(t *testing.T) {
	fake := geminitest.New(t,
		geminitest.MalformedCallResponse(),
		geminitest.ApprovalResponse("pi", "print(3.14)", "python"),
	)
	msg, err := newClient(fake).Generate(context.Background(), nil, "estimate pi")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil || msg.Approval.Code != "print(3.14)" {
		t.Fatalf("Approval = %+v, want the second reply's script", msg.Approval)
	}
	if n := len(fake.Requests()); n != 2 {
		t.Errorf("got %d requests, want 2", n)
	}
}

// The model sometimes calls a code tool it was not offered, such as
// google:python_interpreter; it is told which tool to use instead.
func TestGenerate_UnknownToolIsRedirectedToTheSandbox(t *testing.T) {
	fake := geminitest.New(t,
		geminitest.FunctionCallResponse("google:python_interpreter", map[string]any{"code": "print(3.14)"}),
		geminitest.ApprovalResponse("pi", "print(3.14)", "python"),
	)
	msg, err := newClient(fake).Generate(context.Background(), nil, "estimate pi")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil {
		t.Fatalf("Approval = nil, want the second reply's script")
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	body := string(reqs[1].Body)
	for _, want := range []string{`"functionResponse"`, "google:python_interpreter", "execute_sandbox_code"} {
		if !strings.Contains(body, want) {
			t.Errorf("retry request lacks %q; body = %s", want, body)
		}
	}
}

func TestGenerate_SecondBadReplyGetsThePlainReply(t *testing.T) {
	fake := geminitest.New(t, geminitest.MalformedCallResponse())
	msg, err := newClient(fake).Generate(context.Background(), nil, "estimate pi")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval != nil || !strings.Contains(msg.Content, "couldn't write a script that runs in this sandbox") {
		t.Fatalf("msg = %+v, want the plain reply", msg)
	}
	if n := len(fake.Requests()); n != 2 {
		t.Errorf("got %d requests, want 2", n)
	}
}

// A bad reply and a script that fails the check share the one retry.
func TestGenerate_BadReplyAfterABrokenScriptGetsThePlainReply(t *testing.T) {
	fake := geminitest.New(t,
		geminitest.ApprovalResponse("sum", "print('a\nb')", "python"),
		geminitest.MalformedCallResponse(),
		geminitest.ApprovalResponse("sum", "print('ab')", "python"),
	)
	c := newClient(fake, gemini.WithScriptChecker(fakeChecker("print('a\nb')")))
	msg, err := c.Generate(context.Background(), nil, "sum it")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval != nil {
		t.Fatalf("Approval = %+v, want the plain reply", msg.Approval)
	}
	if n := len(fake.Requests()); n != 2 {
		t.Errorf("got %d requests, want 2", n)
	}
}

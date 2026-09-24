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
	fake := geminitest.New(t, geminitest.ApprovalResponse("count files", "ls | wc -l", "sh"))
	c := newClient(fake)

	msg, err := c.Generate(context.Background(), nil, "how many files?")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil {
		t.Fatal("Approval = nil, want an approval request")
	}
	if msg.Approval.Code != "ls | wc -l" || msg.Approval.Language != "sh" || msg.Approval.RiskLevel != types.RiskHigh {
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
func fakeChecker(broken ...string) gemini.SyntaxChecker {
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
	c := newClient(fake, gemini.WithSyntaxChecker(fakeChecker("print('a\nb')")))

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

func TestGenerate_BrokenScriptIsRetriedOnlyOnce(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("sum", "print('a\nb')", "python"))
	c := newClient(fake, gemini.WithSyntaxChecker(fakeChecker("print('a\nb')")))

	msg, err := c.Generate(context.Background(), nil, "sum it")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil || msg.Approval.Code != "print('a\nb')" {
		t.Fatalf("Approval = %+v, want the still-broken script", msg.Approval)
	}
	if n := len(fake.Requests()); n != 2 {
		t.Errorf("got %d requests, want 2", n)
	}
}

func TestGenerate_NoRetryOnTheLastTurn(t *testing.T) {
	research := geminitest.FunctionCallResponse("fetch_research_data", map[string]any{"topic": "cpu"})
	fake := geminitest.New(t, research, research, geminitest.ApprovalResponse("sum", "print('a\nb')", "python"))
	c := newClient(fake, gemini.WithSyntaxChecker(fakeChecker("print('a\nb')")))

	msg, err := c.Generate(context.Background(), nil, "sum it")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Approval == nil {
		t.Fatal("Approval = nil, want the script from the last turn")
	}
	if n := len(fake.Requests()); n != 3 {
		t.Errorf("got %d requests, want 3", n)
	}
}

func TestPythonSyntaxError(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	ctx := context.Background()
	if got := gemini.PythonSyntaxError(ctx, "python", "print(1)\n"); got != "" {
		t.Errorf("valid script: %q, want none", got)
	}
	// Checking only compiles; running this would exit nonzero.
	if got := gemini.PythonSyntaxError(ctx, "python", "raise SystemExit(3)\n"); got != "" {
		t.Errorf("script was run, not just compiled: %q", got)
	}
	got := gemini.PythonSyntaxError(ctx, "python", "x = 1\nprint('a\nb')\n")
	if !strings.Contains(got, "SyntaxError") || !strings.Contains(got, "line 2") {
		t.Errorf("broken script: %q, want a SyntaxError on line 2", got)
	}
	if got := gemini.PythonSyntaxError(ctx, "sh", "if then fi ((("); got != "" {
		t.Errorf("shell script: %q, want no check", got)
	}
}

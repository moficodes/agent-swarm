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

// Package gemini runs the actor's Gemini tool loop. The gateway makes each
// Vertex AI call for the actor, through the relay.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/agent-substrate/agent-swarm/internal/workload/relay"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const defaultModel = "gemini-2.5-flash"

// ErrDisabled means no Vertex project and location are configured.
var ErrDisabled = errors.New("gemini: no Vertex project and location configured")

// Config names the Vertex model the actor's calls go to.
type Config struct {
	Project  string
	Location string
	Model    string
}

// ConfigFromEnv reads VERTEX_PROJECT, VERTEX_LOCATION and GEMINI_MODEL.
func ConfigFromEnv() Config {
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = defaultModel
	}
	return Config{
		Project:  os.Getenv("VERTEX_PROJECT"),
		Location: os.Getenv("VERTEX_LOCATION"),
		Model:    model,
	}
}

// Enabled reports whether the config names a project and location.
func (c Config) Enabled() bool {
	return c.Project != "" && c.Location != ""
}

// ResearchDataHandler answers the fetch_research_data tool.
type ResearchDataHandler func(topic string) map[string]any

// Option configures a Client.
type Option func(*Client)

// WithTransport replaces the relay transport, for tests.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.transport = rt }
}

// WithResearchDataHandler replaces the canned research data.
func WithResearchDataHandler(h ResearchDataHandler) Option {
	return func(c *Client) { c.research = h }
}

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

// New returns a client whose model calls go through the relay, so Generate
// must run inside a relay.Turn.
func New(cfg Config, opts ...Option) *Client {
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	c := &Client{
		cfg:         cfg,
		transport:   relay.Transport{},
		research:    cannedResearchData,
		checkScript: ScriptProblem,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func cannedResearchData(string) map[string]any {
	return map[string]any{
		"status":      "success",
		"source":      "system_telemetry",
		"data_points": []int{12, 14, 15, 13, 98, 12, 11, 14, 88, 13},
		"message":     "Telemetry data retrieved successfully for analysis.",
	}
}

const maxTurns = 3

// Generate runs up to three model turns and returns the assistant message.
// It returns an error rather than falling back to a mock reply. A proposed
// script that does not parse goes back to the model once for a fix, so the
// attendee is not asked to approve code that cannot run.
func (c *Client) Generate(ctx context.Context, history []types.ChatMessage, prompt string) (*types.ChatMessage, error) {
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

	contents := BuildContents(history, prompt)
	genCfg := &genai.GenerateContentConfig{Tools: ToolDeclarations()}
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
		if approval != nil {
			content := text.String()
			if content == "" {
				content = fmt.Sprintf("I analyzed your request. To proceed, I need your approval to execute this %s script in the sandbox:", approval.Language)
			}
			return assistantMessage(content, approval), nil
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

func approvalFromCall(call *genai.FunctionCall) *types.ApprovalRequest {
	summary, _ := call.Args["summary"].(string)
	code, _ := call.Args["code"].(string)
	lang, _ := call.Args["language"].(string)
	riskStr, _ := call.Args["risk_level"].(string)
	if lang == "" {
		lang = "python"
	}
	if summary == "" {
		summary = "Execute sandbox code in gVisor container"
	}
	risk := types.RiskHigh
	if call.Name == "request_human_approval" {
		switch strings.ToUpper(riskStr) {
		case "LOW":
			risk = types.RiskLow
		case "MEDIUM":
			risk = types.RiskMedium
		}
	}
	return &types.ApprovalRequest{
		ApprovalID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
		Summary:    summary,
		Code:       code,
		Language:   lang,
		RiskLevel:  risk,
	}
}

func assistantMessage(content string, approval *types.ApprovalRequest) *types.ChatMessage {
	return &types.ChatMessage{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   content,
		Timestamp: time.Now().Unix(),
		Approval:  approval,
	}
}

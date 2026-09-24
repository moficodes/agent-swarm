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

// Package gemini runs the actor's Gemini tool loop against the gateway's
// Vertex AI model proxy.
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

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const defaultModel = "gemini-2.5-flash"

var (
	// ErrDisabled means no model proxy is configured.
	ErrDisabled = errors.New("gemini: model proxy not configured")
	// ErrNoModelKey means the gateway has not sent this actor a model key yet.
	ErrNoModelKey = errors.New("gemini: no model key received from the gateway")
)

// Config locates the model proxy and the Vertex model behind it.
type Config struct {
	ProxyURL string
	Project  string
	Location string
	Model    string
}

// ConfigFromEnv reads MODEL_PROXY_URL, VERTEX_PROJECT, VERTEX_LOCATION and GEMINI_MODEL.
func ConfigFromEnv() Config {
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = defaultModel
	}
	return Config{
		ProxyURL: os.Getenv("MODEL_PROXY_URL"),
		Project:  os.Getenv("VERTEX_PROJECT"),
		Location: os.Getenv("VERTEX_LOCATION"),
		Model:    model,
	}
}

// Enabled reports whether the config names a proxy, project and location.
func (c Config) Enabled() bool {
	return c.ProxyURL != "" && c.Project != "" && c.Location != ""
}

// KeyFunc returns the actor's current model proxy key.
type KeyFunc func() string

// ResearchDataHandler answers the fetch_research_data tool.
type ResearchDataHandler func(topic string) map[string]any

// Option configures a Client.
type Option func(*Client)

// WithTransport sets the transport under the bearer-token layer.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.transport = rt }
}

// WithResearchDataHandler replaces the canned research data.
func WithResearchDataHandler(h ResearchDataHandler) Option {
	return func(c *Client) { c.research = h }
}

// Client runs one Gemini turn, including tool calls, through the model proxy.
type Client struct {
	cfg       Config
	key       KeyFunc
	transport http.RoundTripper
	research  ResearchDataHandler
}

// New returns a client that authenticates every request with key().
func New(cfg Config, key KeyFunc, opts ...Option) *Client {
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	c := &Client{
		cfg:       cfg,
		key:       key,
		transport: http.DefaultTransport,
		research:  cannedResearchData,
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
// It returns an error rather than falling back to a mock reply.
func (c *Client) Generate(ctx context.Context, history []types.ChatMessage, prompt string) (*types.ChatMessage, error) {
	if !c.cfg.Enabled() {
		return nil, ErrDisabled
	}
	if c.key == nil || c.key() == "" {
		return nil, ErrNoModelKey
	}
	sdk, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:    genai.BackendVertexAI,
		Project:    c.cfg.Project,
		Location:   c.cfg.Location,
		HTTPClient: &http.Client{Transport: &bearerTransport{key: c.key, base: c.transport}},
		HTTPOptions: genai.HTTPOptions{
			BaseURL: strings.TrimRight(c.cfg.ProxyURL, "/") + "/model/",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gemini: init client: %w", err)
	}

	contents := BuildContents(history, prompt)
	genCfg := &genai.GenerateContentConfig{Tools: ToolDeclarations()}
	for range maxTurns {
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
		var research []*genai.FunctionCall
		for _, part := range cand.Parts {
			text.WriteString(part.Text)
			if part.FunctionCall == nil {
				continue
			}
			switch part.FunctionCall.Name {
			case "request_human_approval", "execute_sandbox_code":
				approval = approvalFromCall(part.FunctionCall)
			case "fetch_research_data":
				research = append(research, part.FunctionCall)
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

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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-workload/internal/engine"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const (
	defaultGeminiModel   = "gemini-2.5-flash"
	defaultGeminiBaseURL = "https://generativelanguage.googleapis.com"
)

// FunctionDeclaration defines a tool/function declared to the Gemini model.
type FunctionDeclaration struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Parameters  *Schema `json:"parameters,omitempty"`
}

// Schema defines JSON Schema for tool parameters.
type Schema struct {
	Type        string             `json:"type"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Description string             `json:"description,omitempty"`
}

// Tool defines tools exposed to the Gemini model.
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
}

// GenerateContentRequest represents the payload dispatched to generateContent.
type GenerateContentRequest struct {
	Contents []Content `json:"contents"`
	Tools    []Tool    `json:"tools,omitempty"`
}

// Content represents a multi-turn conversation turn for Gemini.
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part represents a component of a message turn.
type Part struct {
	Text         string        `json:"text,omitempty"`
	FunctionCall *FunctionCall `json:"functionCall,omitempty"`
}

// FunctionCall describes an invocation proposed by the model.
type FunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// GenerateContentResponse represents the REST API response from Gemini.
type GenerateContentResponse struct {
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Candidate represents a candidate output from the model.
type Candidate struct {
	Content      Content `json:"content"`
	FinishReason string  `json:"finishReason,omitempty"`
}

// GeminiClientOption configures GeminiClient.
type GeminiClientOption func(*GeminiClient)

// WithModel sets the Gemini model name.
func WithModel(model string) GeminiClientOption {
	return func(c *GeminiClient) {
		if model != "" {
			c.model = model
		}
	}
}

// WithBaseURL sets the base URL for Gemini REST requests.
func WithBaseURL(baseURL string) GeminiClientOption {
	return func(c *GeminiClient) {
		if baseURL != "" {
			c.baseURL = strings.TrimRight(baseURL, "/")
		}
	}
}

// WithHTTPClient sets the HTTP client used for REST requests.
func WithHTTPClient(client *http.Client) GeminiClientOption {
	return func(c *GeminiClient) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithGeminiMockEngine sets the fallback mock engine for GeminiClient.
func WithGeminiMockEngine(m *engine.MockEngine) GeminiClientOption {
	return func(c *GeminiClient) {
		if m != nil {
			c.mockEngine = m
		}
	}
}

// GeminiClient communicates with the Gemini REST API with automated fallback to MockEngine.
type GeminiClient struct {
	model      string
	baseURL    string
	httpClient *http.Client
	mockEngine *engine.MockEngine
}

// NewGeminiClient creates a configured GeminiClient.
func NewGeminiClient(opts ...GeminiClientOption) *GeminiClient {
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = defaultGeminiModel
	}

	c := &GeminiClient{
		model:      model,
		baseURL:    defaultGeminiBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		mockEngine: engine.NewMockEngine(),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Model returns the configured model identifier.
func (c *GeminiClient) Model() string {
	return c.model
}

// ToolDeclarations returns the standard tool definitions for conference agents.
func (c *GeminiClient) ToolDeclarations() []FunctionDeclaration {
	return []FunctionDeclaration{
		{
			Name:        "request_human_approval",
			Description: "Request human operator authorization before executing code or high-risk actions.",
			Parameters: &Schema{
				Type: "OBJECT",
				Properties: map[string]*Schema{
					"summary": {
						Type:        "STRING",
						Description: "Concise summary of the proposed code execution or task.",
					},
					"code": {
						Type:        "STRING",
						Description: "The exact script or code snippet to execute.",
					},
					"language": {
						Type:        "STRING",
						Description: "Programming language (e.g. python, bash, sh).",
					},
					"risk_level": {
						Type:        "STRING",
						Description: "Risk assessment level (LOW, MEDIUM, HIGH).",
					},
				},
				Required: []string{"summary", "code"},
			},
		},
		{
			Name:        "execute_sandbox_code",
			Description: "Execute a script inside the isolated gVisor sandbox container workspace.",
			Parameters: &Schema{
				Type: "OBJECT",
				Properties: map[string]*Schema{
					"language": {
						Type:        "STRING",
						Description: "Target programming language (python, bash, sh).",
					},
					"code": {
						Type:        "STRING",
						Description: "Source code to run inside sandbox.",
					},
				},
				Required: []string{"language", "code"},
			},
		},
		{
			Name:        "fetch_research_data",
			Description: "Fetch telemetry metrics or research samples for analysis.",
			Parameters: &Schema{
				Type: "OBJECT",
				Properties: map[string]*Schema{
					"topic": {
						Type:        "STRING",
						Description: "Topic or system metric source to fetch.",
					},
				},
				Required: []string{"topic"},
			},
		},
	}
}

// GenerateContent issues a prompt to the Gemini API or seamlessly delegates to MockEngine on error or missing key.
func (c *GeminiClient) GenerateContent(ctx context.Context, apiKey string, history []types.ChatMessage, prompt string) (*types.ChatMessage, error) {
	if apiKey == "" {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	contents := make([]Content, 0, len(history)+1)
	for _, item := range history {
		role := "user"
		if item.Sender == types.SenderAssistant {
			role = "model"
		}
		if item.Content != "" {
			contents = append(contents, Content{
				Role:  role,
				Parts: []Part{{Text: item.Content}},
			})
		}
	}

	if prompt != "" {
		contents = append(contents, Content{
			Role:  "user",
			Parts: []Part{{Text: prompt}},
		})
	}

	reqBody := GenerateContentRequest{
		Contents: contents,
		Tools: []Tool{
			{
				FunctionDeclarations: c.ToolDeclarations(),
			},
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, c.model, url.QueryEscape(apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBytes))
	if err != nil {
		return c.mockEngine.GenerateTurn(prompt), nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.mockEngine.GenerateTurn(prompt), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	var genResp GenerateContentResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	if len(genResp.Candidates) == 0 {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	cand := genResp.Candidates[0]
	var textBuilder strings.Builder
	var approvalReq *types.ApprovalRequest

	for _, part := range cand.Content.Parts {
		if part.Text != "" {
			textBuilder.WriteString(part.Text)
		}
		if part.FunctionCall != nil && part.FunctionCall.Name == "request_human_approval" {
			summary, _ := part.FunctionCall.Args["summary"].(string)
			code, _ := part.FunctionCall.Args["code"].(string)
			lang, _ := part.FunctionCall.Args["language"].(string)
			if lang == "" {
				lang = "python"
			}
			risk, _ := part.FunctionCall.Args["risk_level"].(string)
			riskLevel := types.RiskHigh
			if risk != "" {
				riskLevel = types.RiskLevel(risk)
			}

			approvalReq = &types.ApprovalRequest{
				ApprovalID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
				Summary:    summary,
				Code:       code,
				Language:   lang,
				RiskLevel:  riskLevel,
			}
		}
	}

	textContent := textBuilder.String()
	if textContent == "" && approvalReq != nil {
		textContent = "I analyzed your request and prepared the following code execution. Please review and authorize:"
	}
	if textContent == "" && approvalReq == nil {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	return &types.ChatMessage{
		ID:        fmt.Sprintf("gemini-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   textContent,
		Timestamp: time.Now().Unix(),
		Approval:  approvalReq,
	}, nil
}

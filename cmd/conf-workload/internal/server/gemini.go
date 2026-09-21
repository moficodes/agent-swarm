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
	Text             string            `json:"text,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// FunctionCall describes an invocation proposed by the model.
type FunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// FunctionResponse describes the output of a tool invocation.
type FunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
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

// ResearchDataHandler defines a handler returning telemetry/research metrics for fetch_research_data.
type ResearchDataHandler func(topic string) map[string]any

// WithResearchDataHandler sets a custom handler for fetch_research_data.
func WithResearchDataHandler(handler ResearchDataHandler) GeminiClientOption {
	return func(c *GeminiClient) {
		if handler != nil {
			c.researchDataHandler = handler
		}
	}
}

// GeminiClient communicates with the Gemini REST API with automated fallback to MockEngine.
type GeminiClient struct {
	model               string
	baseURL             string
	httpClient          *http.Client
	mockEngine          *engine.MockEngine
	researchDataHandler ResearchDataHandler
}

// NewGeminiClient creates a configured GeminiClient.
func NewGeminiClient(opts ...GeminiClientOption) *GeminiClient {
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = defaultGeminiModel
	}

	c := &GeminiClient{
		model:               model,
		baseURL:             defaultGeminiBaseURL,
		httpClient:          &http.Client{Timeout: 30 * time.Second},
		mockEngine:          engine.NewMockEngine(),
		researchDataHandler: executeFetchResearchData,
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
					"query": {
						Type:        "STRING",
						Description: "Alternative query string for telemetry search.",
					},
				},
				Required: []string{"topic"},
			},
		},
	}
}

// executeFetchResearchData returns realistic telemetry metrics and anomaly samples.
func executeFetchResearchData(topic string) map[string]any {
	if topic == "" {
		topic = "cluster_telemetry"
	}
	return map[string]any{
		"topic":  topic,
		"status": "success",
		"telemetry": map[string]any{
			"cpu_usage_percent": 87.4,
			"memory_usage_mb":   1450,
			"active_actors":     12,
			"parked_requests":   3,
			"worker_count":      4,
			"latency_p99_ms":    42.8,
		},
		"anomaly_samples": []map[string]any{
			{"metric": "cpu_spike", "timestamp": "2026-09-20T10:00:00Z", "value": 98.2, "expected": 45.0, "severity": "HIGH"},
			{"metric": "memory_leak", "timestamp": "2026-09-20T10:01:00Z", "value": 1420.0, "expected": 512.0, "severity": "HIGH"},
			{"metric": "network_egress", "timestamp": "2026-09-20T10:02:00Z", "value": 850.0, "expected": 120.0, "severity": "MEDIUM"},
		},
		"raw_samples":  []float64{12.4, 14.1, 15.0, 98.2, 12.1, 88.5, 13.0},
		"sample_count": 7,
	}
}

// ExecuteFetchResearchData calls the configured research data handler for the given topic.
func (c *GeminiClient) ExecuteFetchResearchData(topic string) map[string]any {
	if c.researchDataHandler != nil {
		return c.researchDataHandler(topic)
	}
	return executeFetchResearchData(topic)
}

// GenerateContent issues a prompt to the Gemini API or seamlessly delegates to MockEngine on error or missing key.
func (c *GeminiClient) GenerateContent(ctx context.Context, apiKey string, history []types.ChatMessage, prompt string) (*types.ChatMessage, error) {
	if apiKey == "" {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	contents := make([]Content, 0, len(history)+2)
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

	promptAlreadyAtEnd := len(history) > 0 && history[len(history)-1].Sender == types.SenderUser && history[len(history)-1].Content == prompt
	if prompt != "" && !promptAlreadyAtEnd {
		contents = append(contents, Content{
			Role:  "user",
			Parts: []Part{{Text: prompt}},
		})
	}

	if len(contents) == 0 {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, c.model, url.QueryEscape(apiKey))

	const maxTurns = 3
	for turn := 0; turn < maxTurns; turn++ {
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

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBytes))
		if err != nil {
			return c.mockEngine.GenerateTurn(prompt), nil
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return c.mockEngine.GenerateTurn(prompt), nil
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return c.mockEngine.GenerateTurn(prompt), nil
		}

		var genResp GenerateContentResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&genResp)
		_ = resp.Body.Close()
		if decodeErr != nil || len(genResp.Candidates) == 0 {
			return c.mockEngine.GenerateTurn(prompt), nil
		}

		cand := genResp.Candidates[0]
		var textBuilder strings.Builder
		var approvalReq *types.ApprovalRequest
		var fetchCall *FunctionCall

		for _, part := range cand.Content.Parts {
			if part.Text != "" {
				textBuilder.WriteString(part.Text)
			}
			if part.FunctionCall != nil {
				switch part.FunctionCall.Name {
				case "request_human_approval":
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
				case "fetch_research_data":
					fetchCall = part.FunctionCall
				}
			}
		}

		textContent := textBuilder.String()

		// If human approval was requested, return immediately.
		if approvalReq != nil {
			if textContent == "" {
				textContent = "I analyzed your request and prepared the following code execution. Please review and authorize:"
			}
			return &types.ChatMessage{
				ID:        fmt.Sprintf("gemini-%d", time.Now().UnixNano()),
				Sender:    types.SenderAssistant,
				Content:   textContent,
				Timestamp: time.Now().Unix(),
				Approval:  approvalReq,
			}, nil
		}

		// If fetch_research_data was requested, execute tool and continue multi-turn loop.
		if fetchCall != nil {
			topic := ""
			if t, ok := fetchCall.Args["topic"].(string); ok && t != "" {
				topic = t
			} else if q, ok := fetchCall.Args["query"].(string); ok {
				topic = q
			}

			toolResult := c.ExecuteFetchResearchData(topic)

			modelTurn := cand.Content
			if modelTurn.Role == "" {
				modelTurn.Role = "model"
			}
			contents = append(contents, modelTurn)

			contents = append(contents, Content{
				Role: "function",
				Parts: []Part{
					{
						FunctionResponse: &FunctionResponse{
							Name:     fetchCall.Name,
							Response: toolResult,
						},
					},
				},
			})
			continue
		}

		// If Gemini yielded text without tool calls, return it.
		if textContent != "" {
			return &types.ChatMessage{
				ID:        fmt.Sprintf("gemini-%d", time.Now().UnixNano()),
				Sender:    types.SenderAssistant,
				Content:   textContent,
				Timestamp: time.Now().Unix(),
			}, nil
		}

		return c.mockEngine.GenerateTurn(prompt), nil
	}

	return c.mockEngine.GenerateTurn(prompt), nil
}

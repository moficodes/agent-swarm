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
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/internal/workload/engine"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
	"google.golang.org/genai"
)

const (
	defaultGeminiModel = "gemini-2.5-flash"
)

// GenerateContentRequest represents the payload dispatched to generateContent.
type GenerateContentRequest struct {
	Contents []*genai.Content `json:"contents"`
	Tools    []*genai.Tool    `json:"tools,omitempty"`
}

// Content is an alias for genai.Content.
type Content = genai.Content

// Part is an alias for genai.Part.
type Part = genai.Part

// Model returns the configured Gemini model name.
func (c *GeminiClient) Model() string {
	return c.model
}

// Project returns the configured GCP project ID for Vertex AI.
func (c *GeminiClient) Project() string {
	return c.project
}

// Location returns the configured GCP location for Vertex AI.
func (c *GeminiClient) Location() string {
	return c.location
}

// ResearchDataHandler defines a handler returning telemetry/research metrics for fetch_research_data.
type ResearchDataHandler func(topic string) map[string]any

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

// WithBaseURL sets the base URL for Gemini requests.
func WithBaseURL(baseURL string) GeminiClientOption {
	return func(c *GeminiClient) {
		if baseURL != "" {
			c.baseURL = strings.TrimRight(baseURL, "/")
		}
	}
}

// WithHTTPClient sets the HTTP client used for requests.
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

// WithResearchDataHandler sets a custom handler for fetch_research_data.
func WithResearchDataHandler(handler ResearchDataHandler) GeminiClientOption {
	return func(c *GeminiClient) {
		if handler != nil {
			c.researchDataHandler = handler
		}
	}
}

// WithProject sets the GCP Project ID for Vertex AI.
func WithProject(project string) GeminiClientOption {
	return func(c *GeminiClient) {
		c.project = project
	}
}

// WithLocation sets the GCP location/region for Vertex AI.
func WithLocation(location string) GeminiClientOption {
	return func(c *GeminiClient) {
		c.location = location
	}
}

// GeminiClient communicates with Google GenAI / Vertex AI using official google.golang.org/genai SDK,
// with automated GKE Workload Identity authentication and fallback to MockEngine.
type GeminiClient struct {
	model               string
	project             string
	location            string
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

	project := os.Getenv("PROJECT_ID")
	if project == "" {
		project = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if project == "" {
		project = "mofilabs"
	}

	location := os.Getenv("GCE_REGION")
	if location == "" {
		location = os.Getenv("GOOGLE_CLOUD_LOCATION")
	}
	if location == "" {
		location = "us-west1"
	}

	c := &GeminiClient{
		model:      model,
		project:    project,
		location:   location,
		mockEngine: engine.NewMockEngine(),
		researchDataHandler: func(topic string) map[string]any {
			return map[string]any{
				"status":      "success",
				"source":      "system_telemetry",
				"telemetry":   []int{12, 14, 15, 13, 98, 12, 11, 14, 88, 13},
				"data_points": []int{12, 14, 15, 13, 98, 12, 11, 14, 88, 13},
				"message":     "Telemetry data retrieved successfully for analysis.",
			}
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// ToolDeclarations returns the declared tools for the Gemini model.
func (c *GeminiClient) ToolDeclarations() []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: []*genai.FunctionDeclaration{
				{
					Name:        "request_human_approval",
					Description: "Request human approval before executing any code, script, or state-changing action.",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"summary": {
								Type:        genai.TypeString,
								Description: "A clear, concise summary of the proposed action.",
							},
							"code": {
								Type:        genai.TypeString,
								Description: "The exact script or code snippet to execute.",
							},
							"language": {
								Type:        genai.TypeString,
								Description: "Programming language (e.g. python, bash, sh).",
							},
							"risk_level": {
								Type:        genai.TypeString,
								Description: "Risk assessment level (LOW, MEDIUM, HIGH).",
							},
						},
						Required: []string{"summary", "code"},
					},
				},
				{
					Name:        "execute_sandbox_code",
					Description: "Execute a script inside the isolated gVisor sandbox container workspace. Requires human operator approval before execution.",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"language": {
								Type:        genai.TypeString,
								Description: "Target programming language (python, bash, sh).",
							},
							"code": {
								Type:        genai.TypeString,
								Description: "Source code to run inside sandbox.",
							},
							"summary": {
								Type:        genai.TypeString,
								Description: "Optional concise summary of the proposed code execution.",
							},
						},
						Required: []string{"language", "code"},
					},
				},
				{
					Name:        "fetch_research_data",
					Description: "Fetch telemetry metrics or research samples for analysis.",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"topic": {
								Type:        genai.TypeString,
								Description: "Topic or system metric source to fetch.",
							},
						},
						Required: []string{"topic"},
					},
				},
			},
		},
	}
}

// ExecuteFetchResearchData calls the registered research data handler.
func (c *GeminiClient) ExecuteFetchResearchData(topic string) map[string]any {
	if c.researchDataHandler != nil {
		return c.researchDataHandler(topic)
	}
	return map[string]any{"status": "ok", "topic": topic}
}

// BuildContents prepares alternating user and model contents for the GenAI SDK.
func BuildContents(history []types.ChatMessage, prompt string) []*genai.Content {
	var contents []*genai.Content

	for _, item := range history {
		if item.Content == "" {
			continue
		}
		role := "user"
		if item.Sender == types.SenderAssistant {
			role = "model"
		}

		if len(contents) > 0 && contents[len(contents)-1].Role == role {
			contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, &genai.Part{Text: item.Content})
		} else {
			contents = append(contents, &genai.Content{
				Role: role,
				Parts: []*genai.Part{
					{Text: item.Content},
				},
			})
		}
	}

	promptAlreadyAtEnd := false
	if len(history) > 0 {
		lastMsg := history[len(history)-1]
		if lastMsg.Sender == types.SenderUser && lastMsg.Content == prompt {
			promptAlreadyAtEnd = true
		}
	}

	if prompt != "" && !promptAlreadyAtEnd {
		if len(contents) > 0 && contents[len(contents)-1].Role == "user" {
			contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, &genai.Part{Text: prompt})
		} else {
			contents = append(contents, &genai.Content{
				Role: "user",
				Parts: []*genai.Part{
					{Text: prompt},
				},
			})
		}
	}

	return contents
}

// GenerateContent calls Vertex AI using Workload Identity, or Gemini Developer API if an API key is provided,
// falling back to MockEngine if unavailable or on error.
func (c *GeminiClient) GenerateContent(ctx context.Context, apiKey string, history []types.ChatMessage, prompt string) (*types.ChatMessage, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Prepare SDK client config
	cfg := &genai.ClientConfig{}
	if c.baseURL != "" {
		cfg.HTTPOptions.BaseURL = c.baseURL
	}
	if apiKey != "" {
		cfg.Backend = genai.BackendGeminiAPI
		cfg.APIKey = apiKey
	} else if c.project != "" {
		// Use Vertex AI with GKE Workload Identity / ADC
		cfg.Backend = genai.BackendVertexAI
		cfg.Project = c.project
		cfg.Location = c.location
	} else {
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	if c.httpClient != nil {
		cfg.HTTPClient = c.httpClient
	}

	sdkClient, err := genai.NewClient(ctx, cfg)
	if err != nil {
		log.Printf("[Gemini] Fallback to MockEngine: failed to initialize SDK client: %v", err)
		return c.mockEngine.GenerateTurn(prompt), nil
	}

	contents := BuildContents(history, prompt)
	genConfig := &genai.GenerateContentConfig{
		Tools: c.ToolDeclarations(),
	}

	// Multi-turn tool execution loop (max 3 turns)
	maxTurns := 3
	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		genResp, err := sdkClient.Models.GenerateContent(ctx, c.model, contents, genConfig)
		if err != nil {
			log.Printf("[Gemini] Fallback to MockEngine: GenerateContent error: %v", err)
			return c.mockEngine.GenerateTurn(prompt), nil
		}

		if len(genResp.Candidates) == 0 || genResp.Candidates[0].Content == nil {
			log.Printf("[Gemini] Fallback to MockEngine: empty candidates in response")
			return c.mockEngine.GenerateTurn(prompt), nil
		}

		cand := genResp.Candidates[0]
		var textBuilder strings.Builder
		var approvalReq *types.ApprovalRequest
		var toolCallsToExecute []*genai.FunctionCall

		for _, part := range cand.Content.Parts {
			if part.Text != "" {
				textBuilder.WriteString(part.Text)
			}
			if part.FunctionCall != nil {
				switch part.FunctionCall.Name {
				case "request_human_approval":
					args := part.FunctionCall.Args
					summary, _ := args["summary"].(string)
					code, _ := args["code"].(string)
					lang, _ := args["language"].(string)
					riskStr, _ := args["risk_level"].(string)

					if lang == "" {
						lang = "python"
					}
					risk := types.RiskHigh
					if strings.EqualFold(riskStr, "LOW") {
						risk = types.RiskLow
					} else if strings.EqualFold(riskStr, "MEDIUM") {
						risk = types.RiskMedium
					}

					approvalReq = &types.ApprovalRequest{
						ApprovalID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
						Summary:    summary,
						Code:       code,
						Language:   lang,
						RiskLevel:  risk,
					}
				case "execute_sandbox_code":
					args := part.FunctionCall.Args
					code, _ := args["code"].(string)
					lang, _ := args["language"].(string)
					summary, _ := args["summary"].(string)
					if summary == "" {
						summary = "Execute sandbox code in gVisor container"
					}
					if lang == "" {
						lang = "python"
					}
					approvalReq = &types.ApprovalRequest{
						ApprovalID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
						Summary:    summary,
						Code:       code,
						Language:   lang,
						RiskLevel:  types.RiskHigh,
					}
				case "fetch_research_data":
					toolCallsToExecute = append(toolCallsToExecute, part.FunctionCall)
				}
			}
		}

		if approvalReq != nil {
			textContent := textBuilder.String()
			if textContent == "" {
				textContent = fmt.Sprintf("I analyzed your request. To proceed, I need your approval to execute this %s script in the sandbox:", approvalReq.Language)
			}
			return &types.ChatMessage{
				ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
				Sender:    types.SenderAssistant,
				Content:   textContent,
				Timestamp: time.Now().Unix(),
				Approval:  approvalReq,
			}, nil
		}

		if len(toolCallsToExecute) > 0 {
			// Append model turn to conversation contents
			contents = append(contents, cand.Content)

			// Execute tools and append function responses
			var funcResponseParts []*genai.Part
			for _, call := range toolCallsToExecute {
				topic, _ := call.Args["topic"].(string)
				toolResult := c.ExecuteFetchResearchData(topic)
				funcResponseParts = append(funcResponseParts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						Name:     call.Name,
						Response: toolResult,
					},
				})
			}

			contents = append(contents, &genai.Content{
				Role:  "user",
				Parts: funcResponseParts,
			})
			continue
		}

		textContent := textBuilder.String()
		if textContent != "" {
			return &types.ChatMessage{
				ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
				Sender:    types.SenderAssistant,
				Content:   textContent,
				Timestamp: time.Now().Unix(),
			}, nil
		}
	}

	log.Printf("[Gemini] Fallback to MockEngine: reached max turns without completion")
	return c.mockEngine.GenerateTurn(prompt), nil
}

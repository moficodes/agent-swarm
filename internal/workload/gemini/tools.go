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
	"google.golang.org/genai"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// sandboxLimits is what a script may rely on in the sandbox.
const sandboxLimits = "It may use only the standard library (numpy, pandas and other third-party packages are not installed), has no network access, and is stopped after 5 seconds."

// ToolDeclarations returns the tools offered to the model.
func ToolDeclarations() []*genai.Tool {
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
								Description: "The exact Python script to execute. " + sandboxLimits,
							},
							"language": {
								Type:        genai.TypeString,
								Format:      "enum",
								Enum:        []string{"python"},
								Description: "Always python: only Python scripts can run in the sandbox.",
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
								Format:      "enum",
								Enum:        []string{"python"},
								Description: "Always python: only Python scripts can run in the sandbox.",
							},
							"code": {
								Type:        genai.TypeString,
								Description: "Python source code to run inside the sandbox. " + sandboxLimits,
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

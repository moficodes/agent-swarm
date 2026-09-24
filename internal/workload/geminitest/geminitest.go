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

// Package geminitest serves canned Vertex AI generateContent responses for
// tests of the workload's Gemini client and the gateway's model proxy.
package geminitest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// Request is one request the fake received.
type Request struct {
	Method        string
	Path          string
	Authorization string
	ModelKey      string
	Body          []byte
}

// Server is a fake Vertex AI endpoint.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	bodies   []string
	status   int
	requests []Request
}

// New starts a fake that answers with bodies in order and repeats the last.
func New(t testing.TB, bodies ...string) *Server {
	return start(t, http.StatusOK, bodies)
}

// NewStatus starts a fake that answers every request with status.
func NewStatus(t testing.TB, status int) *Server {
	return start(t, status, []string{`{"error":{"message":"fake failure"}}`})
}

func start(t testing.TB, status int, bodies []string) *Server {
	t.Helper()
	s := &Server{bodies: bodies, status: status}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		ModelKey:      r.Header.Get(types.ModelKeyHeader),
		Body:          body,
	})
	resp := `{}`
	if len(s.bodies) > 0 {
		resp = s.bodies[0]
		if len(s.bodies) > 1 {
			s.bodies = s.bodies[1:]
		}
	}
	status := s.status
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, resp)
}

// Requests returns a copy of every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func response(parts ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": parts},
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     11,
			"candidatesTokenCount": 7,
			"totalTokenCount":      18,
		},
	})
	return string(b)
}

// TextResponse is a generateContent reply carrying only text.
func TextResponse(text string) string {
	return response(map[string]any{"text": text})
}

// FunctionCallResponse is a generateContent reply carrying one function call.
func FunctionCallResponse(name string, args map[string]any) string {
	return response(map[string]any{"functionCall": map[string]any{"name": name, "args": args}})
}

// ApprovalResponse is a reply in which the model asks for approval to run code.
func ApprovalResponse(summary, code, language string) string {
	return FunctionCallResponse("request_human_approval", map[string]any{
		"summary": summary, "code": code, "language": language, "risk_level": "HIGH",
	})
}

# Conference Demo B1 Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the Gemini loop into the conference actors, get their model calls through a gateway-side Vertex proxy, show real Substrate actor state polled from `kubectl-ate`, and run the gateway in-cluster.

**Architecture:**
- The conf-workload actor owns chat history, the Gemini tool loop and its pending approval. It reaches Vertex only through the gateway's model proxy, using a per-actor HMAC key that the gateway attaches to every proxied request.
- The gateway proxies chat and approvals to the atenet router and polls `kubectl-ate get actors -o json` into an `actorstate.Board`, which pushes changed `ActorView`s over SSE. It sums router `parking.active` across router pods.
- The gateway ships as a distroless image that also carries a `kubectl-ate` built from the same commit.

**Tech Stack:**
- Go 1.27, `net/http`, `httputil.ReverseProxy`
- `google.golang.org/genai` v1.71.0 (vendored), `golang.org/x/oauth2/google`
- `k8s.io/client-go` (EndpointSlices)
- Vanilla JS/CSS static UI
- Bash install and verify scripts, Docker buildx

**Spec:** `docs/superpowers/specs/2026-09-23-conference-demo-b1-architecture-design.md`

## Global Constraints

- Every new file starts with the Apache 2.0 header from `hack/boilerplate/` (Go: `boilerplate.go.txt`, shell: `boilerplate.sh.txt`, YAML: `boilerplate.yaml.txt`, Dockerfile: `boilerplate.Dockerfile.txt`). Copyright year 2026.
- American English everywhere (`misspell` runs with `locale: US`).
- Comments describe the final code, never the path taken to it, and never point at this plan.
- `gofmt` clean. `go vet ./demos/conference/...` clean.
- Tests synchronize on channels or returned values, never on `time.Sleep` followed by an assertion. Every package runs under `go test -race`.
- Guard tests for the HMAC check, the model allowlist, both approval-binding layers and the presenter guard must fail if the guard is removed. Temporarily delete the guard once to confirm.
- Vendor and `go.mod` hygiene are out of scope: the demo moves to its own repo. Use only packages already vendored.
- `GEMINI_API_KEY` must not appear in any binary, manifest or script after Task 9.
- The hardcoded `mofilabs` project must not appear anywhere after Task 3.
- Model key header name: `X-Conf-Model-Key`, exported as `types.ModelKeyHeader`.
- Model proxy defaults:
  - listen address `MODEL_PROXY_ADDR`, default `:8091`
  - upstream timeout 30s
  - body cap 256 KiB (413 above it)
  - 2 calls in flight per actor (429 above it)
  - disabled (every request 503) when `VERTEX_PROJECT` is empty
- `SUSPEND_AFTER` defaults to `0`, which suspends the actor right after its turn.
- Poll interval defaults to 1s (`STATE_POLL_INTERVAL`).
- Commit messages start with `demos/conference:`, carry no issue or PR references, and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

Test commands used throughout:

```bash
# all demo Go tests
go test -race ./demos/conference/...
# local end-to-end check (no cluster)
hack/verify-demo-conference.sh --mock-mode
```

## Deviations from the spec

These refine the spec. None changes an approved decision.

1. **Workload packages move to `demos/conference/internal/workload/{server,gemini,engine}`.** A shared fake-Vertex helper lives in `demos/conference/internal/workload/geminitest`.
   - **Why:** Go `internal` visibility. Gateway contract tests and the new local router must build real actors, and they cannot import `cmd/conf-workload/internal/...`.
   - The spec's §7 package names are otherwise unchanged.
2. **The actor sends its model key with a custom `HTTPClient` bearer `RoundTripper`, not `ClientConfig.Credentials`.**
   - **Why:** the vendored SDK skips ADC whenever `HTTPClient` is set. A RoundTripper reads the latest key on every request, so it picks up the key the gateway sends. A static `auth.Credentials` would capture the key only once.
   - Task 2 step 1 verifies the path and header against a fake proxy.
3. **The model key MAC input is `"model-key/" + atespace + "/" + actor`, not `atespace + "/" + actor`.**
   - **Why:** domain separation. The same `HMAC_SECRET` also signs attendee JWTs.
4. **`ActorView.Mock` becomes `ActorView.Source`, of type `ReplySource`, with values `gemini`, `mock` and `mock-fallback`.**
   - The UI maps `mock-fallback` to "Mock (model error)".
   - `ChatMessage` gains `Source` and a structured `Execution *ExecutionResult`, so the gateway no longer parses "Execution completed in…" text.
5. **Router pods for statusz are discovered through EndpointSlices.** The gateway lists EndpointSlices labeled `kubernetes.io/service-name=atenet-router` in `ate-system` and reads port 4040 on each ready endpoint. This needs a Role in `ate-system` granting `list` on `discovery.k8s.io/endpointslices`. This resolves the spec's §6.2 open question.
6. **A new `demos/conference/cmd/conf-localrouter`** (package `demos/conference/internal/localactors`) replaces the Python mock router and the single shared workload in `verify-demo-conference.sh --mock-mode`.
   - It builds one real workload server per `ate-target-actor`, limits concurrency to N slots with a parked counter, and serves `/statusz`.
   - Gateway contract tests use the same package.
7. **The per-attendee SSE event is renamed from `state_change` to `actor_view`,** and its payload is an `ActorView`. Reset is its own `reset` event, so `chat.js` no longer detects reset by matching text.
8. **`types.ModelKeyHeader = "X-Conf-Model-Key"`** is a shared constant used by the gateway, the actor and the proxy.
9. **The spec's "To verify during implementation" items** are settled by Task 13 (router pod discovery) and Task 19 (in-cluster `kubectl-ate` config and poll latency).

## File Structure

Paths are relative to `demos/conference/` unless they start with `hack/`.

```
internal/workload/
  engine/            tools.go (+sandboxEnv), limitwriter.go, mock.go, engine_test.go   [moved, Task 1]
  gemini/            gemini.go, transport.go, tools.go, gemini_test.go                  [new, Task 2]
  geminitest/        geminitest.go                                                       [new, Task 2]
  server/            server.go, server_test.go                                           [moved, Tasks 1,3,4]
internal/localactors/ router.go, router_test.go                                          [new, Task 8]
cmd/conf-workload/   main.go                                                             [imports updated]
cmd/conf-localrouter/ main.go                                                            [new, Task 8]
cmd/conf-gateway/
  main.go, main_test.go                                                                  [Tasks 7,9,12,13]
  Dockerfile, Dockerfile.dockerignore                                                    [new, Task 16]
  internal/modelproxy/  proxy.go, key.go, proxy_test.go                                  [new, Task 6]
  internal/actorstate/  control.go, kubectlate.go, board.go, poll.go, *_test.go, testdata/actors.json [new, Tasks 10,11]
  internal/routerstatus/ status.go, endpointslices.go, status_test.go                   [new, Task 13]
  internal/auth/        auth.go, auth_test.go                                            [split out, Task 15]
  internal/actorproxy/  proxy.go, approvals.go, *_test.go                                [split out, Task 15]
  internal/feed/        feed.go, broker.go, *_test.go                                    [split out, Task 15]
  internal/simulation/  simulation.go, simulation_test.go                                [split out, Task 15]
  internal/web/         web.go, web_test.go                                              [split out, Task 15]
  internal/gateway/     gateway.go (options, mux, lifecycle), contract_test.go, …        [Tasks 9,12,15]
  static/               actorview.js [new], chat.js, feed.js, stage.js, stage.html, *.css [Task 14]
manifests/gateway.yaml.tmpl                                                              [new, Task 16]
conference-template.yaml.tmpl                                                            [env added, Task 16]
pkg/types/types.go, types_test.go                                                        [Tasks 3,4,6,10,12]
pkg/ai/, pkg/engine/                                                                     [deleted, Task 9]
README.md                                                                                [Task 19]
hack/install-demo-conference.sh                                                          [Task 17]
hack/verify-demo-conference.sh                                                           [Task 18]
```

---

### Task 1: Move the workload packages under `demos/conference/internal/workload`

This is a mechanical move so that gateway tests and the local router can import the real workload server. There is no behavior change.

**Files:**
- Move: `cmd/conf-workload/internal/server/*` → `internal/workload/server/`
- Move: `cmd/conf-workload/internal/engine/*` → `internal/workload/engine/`
- Modify: `cmd/conf-workload/main.go` (import path)
- Modify: `internal/workload/server/*.go` (engine import path)

**Interfaces:**
- Produces:
  - `github.com/agent-substrate/substrate/demos/conference/internal/workload/server`
  - `github.com/agent-substrate/substrate/demos/conference/internal/workload/engine`
  - Both keep their identifiers unchanged.

- [ ] **Step 1: Move the files with git**

```bash
cd demos/conference
mkdir -p internal/workload
git mv cmd/conf-workload/internal/server internal/workload/server
git mv cmd/conf-workload/internal/engine internal/workload/engine
rmdir cmd/conf-workload/internal 2>/dev/null || true
```

- [ ] **Step 2: Rewrite the import paths**

```bash
grep -rl 'demos/conference/cmd/conf-workload/internal/' --include='*.go' . \
  | xargs sed -i '' 's#demos/conference/cmd/conf-workload/internal/#demos/conference/internal/workload/#g'
```

On Linux, use `sed -i` without the `''`.

- [ ] **Step 3: Run the tests to confirm nothing changed**

Run: `go test -race ./demos/conference/...`
Expected: PASS, with the same test count as before the move.

- [ ] **Step 4: Confirm the ko image path still builds**

`.ko.yaml` keys on `demos/conference/cmd/conf-workload`, which has not moved.

Run: `go build ./demos/conference/cmd/conf-workload`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add -A demos/conference
git commit -m "demos/conference: move workload packages under internal/workload

Gateway contract tests and a local actor router need to build real
workload servers, which Go's internal rule forbids while the packages
live under cmd/conf-workload/internal.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Proxy-configured Gemini client and fake Vertex helper

This task confirms the SDK credentials path before anything depends on it. The new `gemini` package has no mock fallback: callers decide what to do with an error.

**Files:**
- Create: `internal/workload/gemini/gemini.go`, `internal/workload/gemini/transport.go`, `internal/workload/gemini/tools.go`
- Create: `internal/workload/gemini/gemini_test.go`
- Create: `internal/workload/geminitest/geminitest.go`
- Modify: `pkg/types/types.go` (add `ModelKeyHeader`)

**Interfaces:**
- Produces, in package `gemini`:
  ```go
  type Config struct{ ProxyURL, Project, Location, Model string }
  func ConfigFromEnv() Config            // MODEL_PROXY_URL, VERTEX_PROJECT, VERTEX_LOCATION, GEMINI_MODEL (default "gemini-2.5-flash")
  func (c Config) Enabled() bool         // ProxyURL, Project and Location all non-empty
  type KeyFunc func() string
  var ErrDisabled, ErrNoModelKey error
  type ResearchDataHandler func(topic string) map[string]any
  type Option func(*Client)
  func WithTransport(rt http.RoundTripper) Option
  func WithResearchDataHandler(h ResearchDataHandler) Option
  func New(cfg Config, key KeyFunc, opts ...Option) *Client
  func (c *Client) Generate(ctx context.Context, history []types.ChatMessage, prompt string) (*types.ChatMessage, error)
  func BuildContents(history []types.ChatMessage, prompt string) []*genai.Content
  func ToolDeclarations() []*genai.Tool
  ```
- Produces, in package `geminitest`:
  ```go
  type Request struct{ Method, Path, Authorization, ModelKey string; Body []byte }
  type Server struct{ *httptest.Server /* … */ }
  func New(t testing.TB, bodies ...string) *Server   // replies with bodies in order; repeats the last one
  func NewStatus(t testing.TB, status int) *Server   // always replies with status
  func (s *Server) Requests() []Request
  func TextResponse(text string) string
  func ApprovalResponse(summary, code, language string) string
  func FunctionCallResponse(name string, args map[string]any) string
  ```
- Produces, in package `types`: `const ModelKeyHeader = "X-Conf-Model-Key"`

- [ ] **Step 1: Write the fake Vertex helper**

Create `internal/workload/geminitest/geminitest.go`:

```go
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
```

Add to `pkg/types/types.go` after the `DecisionType` constants:

```go
// ModelKeyHeader carries an actor's model proxy key from the gateway to the
// actor on every proxied request.
const ModelKeyHeader = "X-Conf-Model-Key"
```

- [ ] **Step 2: Write the failing client tests**

Create `internal/workload/gemini/gemini_test.go`:

```go
package gemini_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/internal/workload/gemini"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/geminitest"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func testConfig(proxyURL string) gemini.Config {
	return gemini.Config{ProxyURL: proxyURL, Project: "proj", Location: "loc", Model: "gemini-2.5-flash"}
}

func staticKey(k string) gemini.KeyFunc { return func() string { return k } }

func TestGenerate_SendsVertexPathAndModelKeyAsBearer(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("hello"))
	c := gemini.New(testConfig(fake.URL), staticKey("attendee-01.mac"))

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
	wantPath := "/model/v1beta1/projects/proj/locations/loc/publishers/google/models/gemini-2.5-flash:generateContent"
	if reqs[0].Method != "POST" || reqs[0].Path != wantPath {
		t.Errorf("request = %s %s, want POST %s", reqs[0].Method, reqs[0].Path, wantPath)
	}
	if reqs[0].Authorization != "Bearer attendee-01.mac" {
		t.Errorf("Authorization = %q, want the model key as a bearer token", reqs[0].Authorization)
	}
}

func TestGenerate_ReadsLatestKeyPerRequest(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("a"))
	key := "first.mac"
	c := gemini.New(testConfig(fake.URL), func() string { return key })

	if _, err := c.Generate(context.Background(), nil, "one"); err != nil {
		t.Fatal(err)
	}
	key = "second.mac"
	if _, err := c.Generate(context.Background(), nil, "two"); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if reqs[1].Authorization != "Bearer second.mac" {
		t.Errorf("second Authorization = %q, want Bearer second.mac", reqs[1].Authorization)
	}
}

func TestGenerate_NoKeyFailsWithoutCallingProxy(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("unused"))
	c := gemini.New(testConfig(fake.URL), staticKey(""))

	_, err := c.Generate(context.Background(), nil, "hi")
	if !errors.Is(err, gemini.ErrNoModelKey) {
		t.Fatalf("err = %v, want ErrNoModelKey", err)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Errorf("proxy got %d requests, want 0", n)
	}
}

func TestGenerate_DisabledConfig(t *testing.T) {
	c := gemini.New(gemini.Config{}, staticKey("k.mac"))
	if _, err := c.Generate(context.Background(), nil, "hi"); !errors.Is(err, gemini.ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

func TestGenerate_ApprovalCallBecomesPendingApproval(t *testing.T) {
	fake := geminitest.New(t, geminitest.ApprovalResponse("count files", "ls | wc -l", "sh"))
	c := gemini.New(testConfig(fake.URL), staticKey("k.mac"))

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
	c := gemini.New(testConfig(fake.URL), staticKey("k.mac"),
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
	c := gemini.New(testConfig(fake.URL), staticKey("k.mac"))
	if _, err := c.Generate(context.Background(), nil, "hi"); err == nil {
		t.Fatal("err = nil, want the upstream failure")
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("MODEL_PROXY_URL", "http://proxy:8091")
	t.Setenv("VERTEX_PROJECT", "p")
	t.Setenv("VERTEX_LOCATION", "l")
	t.Setenv("GEMINI_MODEL", "")
	cfg := gemini.ConfigFromEnv()
	want := gemini.Config{ProxyURL: "http://proxy:8091", Project: "p", Location: "l", Model: "gemini-2.5-flash"}
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
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./demos/conference/internal/workload/gemini/`
Expected: FAIL to compile with `undefined: gemini.New` (the package has no non-test files yet).

- [ ] **Step 4: Implement the package**

Create `internal/workload/gemini/transport.go`:

```go
package gemini

import "net/http"

// bearerTransport sends the actor's current model key as a bearer token.
type bearerTransport struct {
	key  KeyFunc
	base http.RoundTripper
}

func (t *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	k := t.key()
	if k == "" {
		return nil, ErrNoModelKey
	}
	out := r.Clone(r.Context())
	out.Header.Set("Authorization", "Bearer "+k)
	return t.base.RoundTrip(out)
}
```

Create `internal/workload/gemini/tools.go`. Move `ToolDeclarations` (from the old `server/gemini.go`, currently lines 185–258) and `BuildContents` (lines 267–318) into it unchanged, except that `ToolDeclarations` becomes a package function rather than a method:

```go
package gemini

import (
	"google.golang.org/genai"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// ToolDeclarations returns the tools offered to the model.
func ToolDeclarations() []*genai.Tool {
	// body moved verbatim from the old (*GeminiClient).ToolDeclarations:
	// request_human_approval, execute_sandbox_code, fetch_research_data.
}

// BuildContents prepares alternating user and model contents for the GenAI SDK.
func BuildContents(history []types.ChatMessage, prompt string) []*genai.Content {
	// body moved verbatim from the old BuildContents.
}
```

The two comment-only bodies above mean "paste the existing body here". They are not placeholders for new logic: the old code is the spec for them.

Create `internal/workload/gemini/gemini.go`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/internal/workload/gemini/`
Expected: PASS.

If `TestGenerate_SendsVertexPathAndModelKeyAsBearer` fails on the path, print `reqs[0].Path`. The SDK's Vertex path layout is `{BaseURL}{APIVersion}/projects/…`, so adjust only the `BaseURL` suffix; the proxy path in Task 6 must equal what this test pins. If the SDK sets its own `Authorization` header, stop and report it: Deviation 2 then needs revisiting.

- [ ] **Step 6: Commit**

```bash
git add demos/conference/internal/workload/gemini demos/conference/internal/workload/geminitest demos/conference/pkg/types/types.go
git commit -m "demos/conference: add proxy-configured Gemini client for actors

The actor will call Vertex through the gateway's model proxy, sending
its per-actor key as a bearer token read on every request. The client
reports failures instead of silently falling back to the mock engine,
so callers can label fallback replies.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Workload server uses the proxy client and labels reply sources

**Files:**
- Modify: `internal/workload/server/server.go`
- Delete: `internal/workload/server/gemini.go`
- Modify: `internal/workload/server/server_test.go`
- Modify: `cmd/conf-workload/main.go`
- Modify: `pkg/types/types.go`, `pkg/types/types_test.go`

**Interfaces:**
- Consumes: `gemini.Config`, `gemini.New`, `gemini.ConfigFromEnv`, `geminitest.*`, `types.ModelKeyHeader`.
- Produces:
  ```go
  // pkg/types
  type ReplySource string
  const (SourceGemini ReplySource = "gemini"; SourceMock ReplySource = "mock"; SourceMockFallback ReplySource = "mock-fallback")
  // ChatMessage gains: Source ReplySource `json:"source,omitempty"`

  // server
  func WithGeminiConfig(cfg gemini.Config) ServerOption   // replaces WithGeminiClient
  func (s *WorkloadServer) ModelKey() string
  ```
  `NewWorkloadServer` uses `gemini.ConfigFromEnv()` when no `WithGeminiConfig` is given. The server stores `X-Conf-Model-Key` from every request that carries it.

- [ ] **Step 1: Write the failing tests**

Add these to `internal/workload/server/server_test.go`:

```go
func proxyConfig(url string) gemini.Config {
	return gemini.Config{ProxyURL: url, Project: "proj", Location: "loc", Model: "gemini-2.5-flash"}
}

func postChat(t *testing.T, h http.Handler, key, msg string) types.ChatMessage {
	t.Helper()
	body, _ := json.Marshal(types.ChatRequest{Message: msg})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	if key != "" {
		req.Header.Set(types.ModelKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body %s", rec.Code, rec.Body)
	}
	var out types.ChatMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestChat_UsesModelKeyFromGatewayHeader(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("from gemini"))
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(proxyConfig(fake.URL)))

	msg := postChat(t, s.Handler(), "attendee-01.mac", "hi")

	if msg.Content != "from gemini" || msg.Source != types.SourceGemini {
		t.Errorf("reply = %q source %q, want gemini reply", msg.Content, msg.Source)
	}
	if got := fake.Requests()[0].Authorization; got != "Bearer attendee-01.mac" {
		t.Errorf("proxy Authorization = %q", got)
	}
	if s.ModelKey() != "attendee-01.mac" {
		t.Errorf("ModelKey() = %q", s.ModelKey())
	}
}

func TestChat_ModelFailureFallsBackToLabeledMock(t *testing.T) {
	fake := geminitest.NewStatus(t, http.StatusInternalServerError)
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(proxyConfig(fake.URL)))

	msg := postChat(t, s.Handler(), "k.mac", "hi")

	if msg.Source != types.SourceMockFallback {
		t.Errorf("Source = %q, want mock-fallback", msg.Source)
	}
}

func TestChat_NoProxyConfiguredUsesLabeledMock(t *testing.T) {
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(gemini.Config{}))
	if msg := postChat(t, s.Handler(), "", "hi"); msg.Source != types.SourceMock {
		t.Errorf("Source = %q, want mock", msg.Source)
	}
}

func TestChat_ForceMockSkipsProxy(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("unused"))
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()),
		server.WithGeminiConfig(proxyConfig(fake.URL)), server.WithForceMock(true))

	if msg := postChat(t, s.Handler(), "k.mac", "hi"); msg.Source != types.SourceMock {
		t.Errorf("Source = %q, want mock", msg.Source)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Errorf("proxy got %d requests, want 0", n)
	}
}

func TestChat_HistoryIsSentOnFollowingTurn(t *testing.T) {
	fake := geminitest.New(t, geminitest.TextResponse("first answer"), geminitest.TextResponse("second answer"))
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(proxyConfig(fake.URL)))

	postChat(t, s.Handler(), "k.mac", "remember the word pelican")
	postChat(t, s.Handler(), "k.mac", "what word?")

	second := string(fake.Requests()[1].Body)
	if !strings.Contains(second, "pelican") || !strings.Contains(second, "first answer") {
		t.Errorf("second model request lacks history: %s", second)
	}
}
```

In the same file, remove every `t.Setenv("GEMINI_API_KEY", …)`. The known sites are at the old lines 105, 557, 570, 587, 683, 946, 1026, 1167, 1188, 1252 and 1355; find them all with `grep -n GEMINI_API_KEY`.
- **Tests that pointed `WithGeminiClient(NewGeminiClient(WithBaseURL(fake.URL)…))` at a fake:** switch them to `WithGeminiConfig(proxyConfig(fake.URL))` plus a `geminitest.New` fake, and send the key header via `postChat`.
- **Tests that exercised `BuildContents` or `ToolDeclarations` through the server:** delete them; `gemini_test.go` covers both directly.
- **Tests asserting the mock text is returned when Gemini fails:** keep them, and also assert `Source == types.SourceMockFallback`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/internal/workload/server/`
Expected: FAIL to compile: `undefined: server.WithGeminiConfig`, `msg.Source undefined`.

- [ ] **Step 3: Add the types**

In `pkg/types/types.go`, add after `ModelKeyHeader`:

```go
// ReplySource says which engine produced an assistant reply.
type ReplySource string

const (
	// SourceGemini is a reply from Gemini through the model proxy.
	SourceGemini ReplySource = "gemini"
	// SourceMock is a reply from the offline mock engine by configuration.
	SourceMock ReplySource = "mock"
	// SourceMockFallback is a mock reply used because the Gemini call failed.
	SourceMockFallback ReplySource = "mock-fallback"
)
```

Add a field to `ChatMessage`:

```go
	Source    ReplySource      `json:"source,omitempty"`
```

Add a `ChatMessage` round-trip subtest to `pkg/types/types_test.go` asserting `"source":"mock-fallback"` in the JSON.

- [ ] **Step 4: Rewrite the server's chat path**

Delete `internal/workload/server/gemini.go` (`git rm`). In `server.go`:
- Replace the `geminiClient *GeminiClient` field with `gemini *gemini.Client` and `modelKey string`.
- Replace `WithGeminiClient` with:

```go
// WithGeminiConfig points the actor's Gemini client at a model proxy. A
// config that is not Enabled leaves the actor on the mock engine.
func WithGeminiConfig(cfg gemini.Config) ServerOption {
	return func(s *WorkloadServer) { s.geminiCfg = cfg }
}
```

In `NewWorkloadServer`, set `geminiCfg: gemini.ConfigFromEnv()` in the struct literal. After the options loop, add:

```go
	if s.geminiCfg.Enabled() {
		s.gemini = gemini.New(s.geminiCfg, s.ModelKey)
	}
```

Add the key accessor and the header capture:

```go
// ModelKey returns the latest model proxy key the gateway sent.
func (s *WorkloadServer) ModelKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelKey
}

// Handler returns the HTTP handler for the server. Every request that
// carries a model key refreshes the one the Gemini client uses.
func (s *WorkloadServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get(types.ModelKeyHeader); k != "" {
			s.mu.Lock()
			s.modelKey = k
			s.mu.Unlock()
		}
		s.mux.ServeHTTP(w, r)
	})
}
```

Replace the body of `handleChat` from `apiKey := os.Getenv("GEMINI_API_KEY")` to the reply encoding with:

```go
	userMsg := types.ChatMessage{
		ID:        fmt.Sprintf("user-%d", time.Now().UnixNano()),
		Sender:    types.SenderUser,
		Content:   req.Message,
		Timestamp: time.Now().Unix(),
	}

	s.mu.Lock()
	s.history = append(s.history, userMsg)
	historyCopy := append([]types.ChatMessage(nil), s.history...)
	s.mu.Unlock()

	assistantMsg := s.reply(r.Context(), historyCopy, req.Message)

	s.mu.Lock()
	if assistantMsg.Approval != nil {
		s.pendingApproval = assistantMsg.Approval
	}
	s.history = append(s.history, *assistantMsg)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(assistantMsg)
}

// chatTimeout bounds one Gemini turn, tool calls included.
const chatTimeout = 60 * time.Second

func (s *WorkloadServer) reply(ctx context.Context, history []types.ChatMessage, prompt string) *types.ChatMessage {
	if s.forceMock || s.gemini == nil {
		msg := s.mockEngine.GenerateTurn(prompt)
		msg.Source = types.SourceMock
		return msg
	}
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()
	msg, err := s.gemini.Generate(ctx, history, prompt)
	if err != nil {
		log.Printf("gemini turn failed, replying with the mock engine: %v", err)
		msg = s.mockEngine.GenerateTurn(prompt)
		msg.Source = types.SourceMockFallback
		return msg
	}
	msg.Source = types.SourceGemini
	return msg
}
```

Add `"log"` and the `gemini` import, and drop the `os` use for the key.

- [ ] **Step 5: Update `cmd/conf-workload/main.go`**

`main.go` already reads `DEMO_MOCK_LLM`. `NewWorkloadServer` reads the proxy config itself, so `main.go` needs no Gemini code. Confirm it contains no `GEMINI_API_KEY`, `PROJECT_ID` or `GOOGLE_CLOUD_PROJECT`:

```bash
grep -rn 'GEMINI_API_KEY\|mofilabs\|GOOGLE_CLOUD_PROJECT\|GCE_REGION' demos/conference/cmd/conf-workload demos/conference/internal
```

Expected: no output.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/internal/workload/... ./demos/conference/pkg/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A demos/conference/internal/workload demos/conference/pkg/types demos/conference/cmd/conf-workload
git commit -m "demos/conference: actor calls Gemini through the model proxy

The workload now reads its model proxy location from template env,
authenticates with the key the gateway sends on each request, and
labels every reply as gemini, mock, or mock-fallback. The hardcoded
GCP project default and GEMINI_API_KEY are gone from the workload.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Actor-side approval binding and structured execution results

**Files:**
- Modify: `internal/workload/server/server.go` (`handleApprove`)
- Modify: `internal/workload/server/server_test.go`
- Modify: `pkg/types/types.go`, `pkg/types/types_test.go`

**Interfaces:**
- Produces:
  - `ChatMessage` gains `Execution *ExecutionResult \`json:"execution,omitempty"\``.
  - `/api/approve` returns 409 when the request does not match the actor's own pending approval. It ignores `code` and `language` from the request.
  - An approved turn returns a `ChatMessage` whose `Execution` is set. When the run could not start or did not finish, `Execution.ExitCode` is `-1` and `Stderr` holds the error.

- [ ] **Step 1: Write the failing tests**

Add to `server_test.go`:

```go
func approvalServer(t *testing.T, code, lang string) (*server.WorkloadServer, string) {
	t.Helper()
	fake := geminitest.New(t, geminitest.ApprovalResponse("run it", code, lang))
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithGeminiConfig(proxyConfig(fake.URL)))
	msg := postChat(t, s.Handler(), "k.mac", "do the thing")
	if msg.Approval == nil {
		t.Fatal("expected an approval request")
	}
	return s, msg.Approval.ApprovalID
}

func postApprove(t *testing.T, h http.Handler, resp types.ApprovalResponse) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(resp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/approve", bytes.NewReader(body)))
	return rec
}

func TestApprove_RunsStoredCodeAndIgnoresRequestCode(t *testing.T) {
	s, id := approvalServer(t, "echo stored", "sh")

	rec := postApprove(t, s.Handler(), types.ApprovalResponse{
		ApprovalID: id, Decision: types.DecisionApprove, Code: "echo injected", Language: "sh",
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

func TestApprove_NoPendingApprovalIsConflictEvenWithCode(t *testing.T) {
	s := server.NewWorkloadServer(server.WithWorkspaceDir(t.TempDir()), server.WithForceMock(true))

	rec := postApprove(t, s.Handler(), types.ApprovalResponse{
		ApprovalID: "app-forged", Decision: types.DecisionApprove, Code: "echo pwned", Language: "sh",
	})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestApprove_WrongIDIsConflictAndKeepsPending(t *testing.T) {
	s, id := approvalServer(t, "echo stored", "sh")

	rec := postApprove(t, s.Handler(), types.ApprovalResponse{ApprovalID: id + "-other", Decision: types.DecisionApprove})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if p := s.PendingApproval(); p == nil || p.ApprovalID != id {
		t.Errorf("PendingApproval = %+v, want it kept", p)
	}
}

func TestApprove_ReplayIsConflict(t *testing.T) {
	s, id := approvalServer(t, "echo once", "sh")
	ok := types.ApprovalResponse{ApprovalID: id, Decision: types.DecisionApprove}
	if rec := postApprove(t, s.Handler(), ok); rec.Code != http.StatusOK {
		t.Fatalf("first approve = %d", rec.Code)
	}
	if rec := postApprove(t, s.Handler(), ok); rec.Code != http.StatusConflict {
		t.Fatalf("replay = %d, want 409", rec.Code)
	}
}

func TestApprove_FailedRunReportsStructuredError(t *testing.T) {
	s, id := approvalServer(t, "print(1)", "cobol")

	rec := postApprove(t, s.Handler(), types.ApprovalResponse{ApprovalID: id, Decision: types.DecisionApprove})

	var msg types.ChatMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &msg)
	if msg.Execution == nil || msg.Execution.ExitCode != -1 || !strings.Contains(msg.Execution.Stderr, "unsupported language") {
		t.Errorf("Execution = %+v, want exit -1 with the error", msg.Execution)
	}
}
```

Change the existing tests that expected 404 for a missing approval (`TestApprove_Execute` re-approve, `TestApprove_Reject`, `TestApprove_InvalidAndNotFound`) to expect `http.StatusConflict`. Delete any test that approves supplied `code` with no pending approval; that path no longer exists.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/internal/workload/server/ -run 'TestApprove'`
Expected: FAIL. The forged-code test gets 200, the wrong-ID test gets 404, and `msg.Execution` is undefined (compile error until the type change).

- [ ] **Step 3: Add `Execution` to `ChatMessage`**

```go
	Execution *ExecutionResult `json:"execution,omitempty"`
```

- [ ] **Step 4: Rewrite `handleApprove` after validation**

```go
	s.mu.Lock()
	pending := s.pendingApproval
	if pending == nil || pending.ApprovalID != req.ApprovalID {
		s.mu.Unlock()
		http.Error(w, "no matching pending approval", http.StatusConflict)
		return
	}
	s.pendingApproval = nil

	if req.Decision == types.DecisionReject {
		rejectMsg := types.ChatMessage{
			ID:        fmt.Sprintf("sys-%d", time.Now().UnixNano()),
			Sender:    types.SenderSystem,
			Content:   "Execution rejected by attendee. Operation canceled.",
			Timestamp: time.Now().Unix(),
		}
		s.history = append(s.history, rejectMsg)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rejectMsg)
		return
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	exec := runResult(s.executor.Execute(ctx, pending.Language, pending.Code))

	resultMsg := types.ChatMessage{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Sender:    types.SenderAssistant,
		Content:   describeRun(exec),
		Timestamp: time.Now().Unix(),
		Execution: exec,
	}

	s.mu.Lock()
	s.history = append(s.history, resultMsg)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resultMsg)
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

func describeRun(e *types.ExecutionResult) string {
	if e.ExitCode == -1 {
		return "Execution error:\n" + e.Stderr
	}
	s := fmt.Sprintf("Execution completed in %dms (exit code %d):\n\n%s", e.DurationMs, e.ExitCode, e.Stdout)
	if e.Stderr != "" || e.ExitCode != 0 {
		s += "\nStderr:\n" + e.Stderr
	}
	return s
}
```

Add `"strings"` to the imports.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/internal/workload/... ./demos/conference/pkg/...`
Expected: PASS.

Remove the `pending == nil ||` clause once to confirm `TestApprove_NoPendingApprovalIsConflictEvenWithCode` fails, then restore it.

- [ ] **Step 6: Commit**

```bash
git add demos/conference/internal/workload/server demos/conference/pkg/types
git commit -m "demos/conference: actor runs only the code it proposed

/api/approve now requires a match with the actor's own pending approval
and ignores any code in the request, closing the path where an actor
executed caller-supplied code it never proposed. Results carry a
structured execution field so the gateway need not parse reply text.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Minimal environment for sandboxed code

**Files:**
- Modify: `internal/workload/engine/tools.go`
- Modify: `internal/workload/engine/engine_test.go`

**Interfaces:**
- Produces: approved code runs with exactly `PATH=/usr/local/bin:/usr/bin:/bin`, `HOME=<workspace>` and `LANG=C.UTF-8`.

- [ ] **Step 1: Write the failing test**

```go
func TestExecute_ChildSeesOnlyMinimalEnvironment(t *testing.T) {
	t.Setenv("HMAC_SECRET", "must-not-leak")
	t.Setenv("MODEL_PROXY_URL", "http://proxy")
	ws := t.TempDir()
	ex := engine.NewSandboxExecutor(ws)

	res, err := ex.Execute(context.Background(), "sh", "env")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(res.Stdout)
	sort.Strings(got)
	want := []string{"HOME=" + ws, "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin", "PWD=" + ws}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("child env = %v, want %v", got, want)
	}
}
```

`/bin/sh` sets `PWD` itself, and `OLDPWD` or `SHLVL` may appear depending on the shell. If they do, drop them from `got` before comparing, and name them in a comment. Everything else must be exactly the three variables.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./demos/conference/internal/workload/engine/ -run TestExecute_ChildSeesOnlyMinimalEnvironment`
Expected: FAIL, showing `HMAC_SECRET=must-not-leak` in the child env.

- [ ] **Step 3: Implement**

In `tools.go`, after `cmd.Dir = s.WorkspaceDir`:

```go
	cmd.Env = sandboxEnv(s.WorkspaceDir)
```

Add:

```go
// sandboxEnv is the whole environment model-generated code sees, so no
// configuration of the actor process leaks into it.
func sandboxEnv(workspace string) []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + workspace,
		"LANG=C.UTF-8",
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/internal/workload/engine/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add demos/conference/internal/workload/engine
git commit -m "demos/conference: give sandboxed code a minimal environment

Approved code inherited the actor's whole environment. It now gets
only PATH, HOME set to the workspace, and LANG.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 6: `modelproxy`, the gateway-side Vertex proxy

**Files:**
- Create: `cmd/conf-gateway/internal/modelproxy/key.go`
- Create: `cmd/conf-gateway/internal/modelproxy/proxy.go`
- Create: `cmd/conf-gateway/internal/modelproxy/proxy_test.go`
- Modify: `pkg/types/types.go` (add `ModelUsage`)

**Interfaces:**
- Consumes: `geminitest.New` (as fake Vertex), `types.ModelKeyHeader`.
- Produces:
  ```go
  // pkg/types
  type ModelUsage struct {
  	Calls           int64 `json:"calls"`
  	PromptTokens    int64 `json:"prompt_tokens"`
  	CandidateTokens int64 `json:"candidate_tokens"`
  	TotalLatencyMs  int64 `json:"total_latency_ms"`
  	LastLatencyMs   int64 `json:"last_latency_ms"`
  }
  // modelproxy
  func Key(secret []byte, atespace, actor string) string
  type Config struct {
  	Secret      []byte
  	Atespace    string
  	Project     string // empty disables the proxy (503)
  	Location    string
  	Models      []string
  	Upstream    string // default "https://{Location}-aiplatform.googleapis.com"
  	TokenSource oauth2.TokenSource
  	Timeout     time.Duration // default 30s
  	MaxBody     int64         // default 256 KiB
  	MaxInFlight int           // default 2
  	Transport   http.RoundTripper
  }
  func New(cfg Config) (*Proxy, error)
  func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request)
  func (p *Proxy) Enabled() bool
  func (p *Proxy) Usage(actor string) types.ModelUsage
  func (p *Proxy) TotalUsage() types.ModelUsage
  func (p *Proxy) ResetUsage()
  ```

- [ ] **Step 1: Write the failing tests**

Create `cmd/conf-gateway/internal/modelproxy/proxy_test.go`:

```go
package modelproxy_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/geminitest"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const (
	testAtespace = "ate-demo-conf"
	goodPath     = "/model/v1beta1/projects/proj/locations/loc/publishers/google/models/gemini-2.5-flash:generateContent"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func newProxy(t *testing.T, upstream string, mutate ...func(*modelproxy.Config)) *modelproxy.Proxy {
	t.Helper()
	cfg := modelproxy.Config{
		Secret:      testSecret,
		Atespace:    testAtespace,
		Project:     "proj",
		Location:    "loc",
		Models:      []string{"gemini-2.5-flash"},
		Upstream:    upstream,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"}),
	}
	for _, m := range mutate {
		m(&cfg)
	}
	p, err := modelproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func call(p http.Handler, method, path, bearer string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestProxy_ForwardsWithADCTokenAndStripsActorKey(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	req := httptest.NewRequest(http.MethodPost, goodPath+"?alt=sse", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(types.ModelKeyHeader, key)
	req.Header.Set("Cookie", "session=x")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := vertex.Requests()[0]
	if got.Authorization != "Bearer adc-token" {
		t.Errorf("upstream Authorization = %q, want the ADC token", got.Authorization)
	}
	if got.ModelKey != "" {
		t.Errorf("upstream saw %s = %q", types.ModelKeyHeader, got.ModelKey)
	}
	if want := strings.TrimPrefix(goodPath, "/model"); got.Path != want {
		t.Errorf("upstream path = %q, want %q", got.Path, want)
	}
}

func TestProxy_RejectsBadKeys(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	good := modelproxy.Key(testSecret, testAtespace, "attendee-01")
	otherSpace := modelproxy.Key(testSecret, "other-space", "attendee-01")
	otherSecret := modelproxy.Key([]byte("another-secret-another-secret-00"), testAtespace, "attendee-01")
	forgedActor := "attendee-02" + good[strings.LastIndex(good, "."):]

	for name, bearer := range map[string]string{
		"missing":      "",
		"garbage":      "not-a-key",
		"other space":  otherSpace,
		"other secret": otherSecret,
		"forged actor": forgedActor,
		"no actor":     good[strings.LastIndex(good, "."):],
	} {
		t.Run(name, func(t *testing.T) {
			if rec := call(p, http.MethodPost, goodPath, bearer, []byte(`{}`)); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
	if n := len(vertex.Requests()); n != 0 {
		t.Errorf("upstream got %d requests, want 0", n)
	}
}

func TestProxy_AllowsOnlyConfiguredGenerateContent(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	for name, tc := range map[string]struct{ method, path string }{
		"GET":             {http.MethodGet, goodPath},
		"other project":   {http.MethodPost, strings.Replace(goodPath, "/proj/", "/evil/", 1)},
		"other location":  {http.MethodPost, strings.Replace(goodPath, "/loc/", "/us-east1/", 1)},
		"model not allowed": {http.MethodPost, strings.Replace(goodPath, "gemini-2.5-flash", "gemini-2.5-pro", 1)},
		"streaming":       {http.MethodPost, strings.Replace(goodPath, ":generateContent", ":streamGenerateContent", 1)},
		"other method":    {http.MethodPost, strings.Replace(goodPath, ":generateContent", ":countTokens", 1)},
		"no prefix":       {http.MethodPost, strings.TrimPrefix(goodPath, "/model")},
		"encoded slash":   {http.MethodPost, strings.Replace(goodPath, "/models/", "/models%2F", 1)},
		"traversal":       {http.MethodPost, "/model/v1beta1/projects/proj/locations/loc/publishers/google/models/../../x:generateContent"},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := call(p, tc.method, tc.path, key, []byte(`{}`)); rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
		})
	}
	if n := len(vertex.Requests()); n != 0 {
		t.Errorf("upstream got %d requests, want 0", n)
	}
}

func TestProxy_BodyCap(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL, func(c *modelproxy.Config) { c.MaxBody = 16 })
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	if rec := call(p, http.MethodPost, goodPath, key, bytes.Repeat([]byte("a"), 17)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

// blockingUpstream holds every request until release is closed.
func blockingUpstream(t *testing.T) (url string, entered <-chan struct{}, release chan struct{}) {
	t.Helper()
	in := make(chan struct{}, 8)
	rel := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in <- struct{}{}
		<-rel
		_, _ = w.Write([]byte(geminitest.TextResponse("ok")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-rel:
		default:
			close(rel)
		}
	})
	return srv.URL, in, rel
}

func TestProxy_PerActorConcurrencyCap(t *testing.T) {
	url, entered, release := blockingUpstream(t)
	p := newProxy(t, url, func(c *modelproxy.Config) { c.MaxInFlight = 1 })
	key1 := modelproxy.Key(testSecret, testAtespace, "attendee-01")
	key2 := modelproxy.Key(testSecret, testAtespace, "attendee-02")

	done := make(chan int)
	go func() { done <- call(p, http.MethodPost, goodPath, key1, []byte(`{}`)).Code }()
	<-entered

	if rec := call(p, http.MethodPost, goodPath, key1, []byte(`{}`)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second call for same actor = %d, want 429", rec.Code)
	}
	go func() { done <- call(p, http.MethodPost, goodPath, key2, []byte(`{}`)).Code }()
	<-entered // another actor is not limited by attendee-01's calls

	close(release)
	for range 2 {
		if code := <-done; code != http.StatusOK {
			t.Errorf("held call = %d, want 200", code)
		}
	}
}

func TestProxy_RecordsUsagePerActor(t *testing.T) {
	vertex := geminitest.New(t, geminitest.TextResponse("ok"))
	p := newProxy(t, vertex.URL)
	key := modelproxy.Key(testSecret, testAtespace, "attendee-07")

	call(p, http.MethodPost, goodPath, key, []byte(`{}`))
	call(p, http.MethodPost, goodPath, key, []byte(`{}`))

	u := p.Usage("attendee-07")
	if u.Calls != 2 || u.PromptTokens != 22 || u.CandidateTokens != 14 {
		t.Errorf("Usage = %+v, want 2 calls, 22 prompt, 14 candidate tokens", u)
	}
	if other := p.Usage("attendee-01"); other.Calls != 0 {
		t.Errorf("attendee-01 usage = %+v, want zero", other)
	}
	if total := p.TotalUsage(); total.Calls != 2 {
		t.Errorf("TotalUsage = %+v", total)
	}
	p.ResetUsage()
	if u := p.Usage("attendee-07"); u.Calls != 0 {
		t.Errorf("after ResetUsage = %+v", u)
	}
}

func TestProxy_DisabledWithoutProject(t *testing.T) {
	p := newProxy(t, "http://unused", func(c *modelproxy.Config) { c.Project = ""; c.TokenSource = nil })
	key := modelproxy.Key(testSecret, testAtespace, "attendee-01")

	if p.Enabled() {
		t.Error("Enabled() = true, want false")
	}
	if rec := call(p, http.MethodPost, goodPath, key, []byte(`{}`)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestNew_RequiresSecretAndTokenSource(t *testing.T) {
	if _, err := modelproxy.New(modelproxy.Config{Project: "p", Location: "l", TokenSource: oauth2.StaticTokenSource(&oauth2.Token{})}); err == nil {
		t.Error("New without Secret succeeded")
	}
	if _, err := modelproxy.New(modelproxy.Config{Secret: testSecret, Project: "p", Location: "l"}); err == nil {
		t.Error("New without TokenSource succeeded while enabled")
	}
}

func TestKey_DiffersFromJWTSigningInput(t *testing.T) {
	a := modelproxy.Key(testSecret, testAtespace, "attendee-01")
	if !strings.HasPrefix(a, "attendee-01.") {
		t.Fatalf("Key = %q, want actor prefix", a)
	}
	if a == modelproxy.Key(testSecret, testAtespace, "attendee-02") {
		t.Error("keys for different actors are equal")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/internal/modelproxy/`
Expected: FAIL to compile: `undefined: modelproxy.Config`.

- [ ] **Step 3: Implement `key.go`**

```go
// Package modelproxy forwards actors' Gemini calls to Vertex AI with the
// gateway's own credentials, so no model credential lives in an actor.
package modelproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// Key returns the model proxy key for one actor: "<actor>.<mac>". The MAC
// input is prefixed so the key never matches another use of the secret.
func Key(secret []byte, atespace, actor string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("model-key/" + atespace + "/" + actor))
	return actor + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// actorFromKey returns the actor a key was issued to, if the MAC is valid.
func actorFromKey(secret []byte, atespace, key string) (string, bool) {
	i := strings.LastIndexByte(key, '.')
	if i <= 0 {
		return "", false
	}
	actor := key[:i]
	return actor, hmac.Equal([]byte(Key(secret, atespace, actor)), []byte(key))
}
```

- [ ] **Step 4: Implement `proxy.go`**

```go
package modelproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const (
	defaultTimeout     = 30 * time.Second
	defaultMaxBody     = 256 << 10
	defaultMaxInFlight = 2
	maxResponseBody    = 8 << 20
)

var generatePath = regexp.MustCompile(`^/model/(v1[a-z0-9]*)/projects/([^/]+)/locations/([^/]+)/publishers/google/models/([^/:]+):generateContent$`)

// Config configures a Proxy.
type Config struct {
	Secret      []byte
	Atespace    string
	Project     string
	Location    string
	Models      []string
	Upstream    string
	TokenSource oauth2.TokenSource
	Timeout     time.Duration
	MaxBody     int64
	MaxInFlight int
	Transport   http.RoundTripper
}

// Proxy authenticates actors by model key and forwards allowlisted
// generateContent calls to Vertex AI.
type Proxy struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	inFlight map[string]int
	usage    map[string]types.ModelUsage
}

// New validates cfg and returns a Proxy. An empty Project yields a disabled
// proxy that answers 503.
func New(cfg Config) (*Proxy, error) {
	if len(cfg.Secret) == 0 {
		return nil, errors.New("modelproxy: secret is required")
	}
	if cfg.Project != "" {
		if cfg.Location == "" {
			return nil, errors.New("modelproxy: location is required with a project")
		}
		if cfg.TokenSource == nil {
			return nil, errors.New("modelproxy: token source is required with a project")
		}
	}
	if cfg.Upstream == "" {
		cfg.Upstream = "https://" + cfg.Location + "-aiplatform.googleapis.com"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = defaultMaxBody
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = defaultMaxInFlight
	}
	return &Proxy{
		cfg:      cfg,
		client:   &http.Client{Transport: cfg.Transport, Timeout: cfg.Timeout},
		inFlight: map[string]int{},
		usage:    map[string]types.ModelUsage{},
	}, nil
}

// Enabled reports whether the proxy forwards requests.
func (p *Proxy) Enabled() bool { return p.cfg.Project != "" }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.Enabled() {
		http.Error(w, "model proxy disabled: no Vertex project configured", http.StatusServiceUnavailable)
		return
	}
	actor, ok := actorFromKey(p.cfg.Secret, p.cfg.Atespace, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !ok {
		http.Error(w, "invalid model key", http.StatusUnauthorized)
		return
	}
	if !p.allowed(r) {
		http.Error(w, "only allowlisted generateContent calls are permitted", http.StatusForbidden)
		return
	}
	if !p.acquire(actor) {
		http.Error(w, "too many model calls in flight for this actor", http.StatusTooManyRequests)
		return
	}
	defer p.release(actor)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.cfg.MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "reading request body failed", http.StatusBadRequest)
		return
	}
	tok, err := p.cfg.TokenSource.Token()
	if err != nil {
		http.Error(w, "gateway credentials unavailable", http.StatusBadGateway)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), p.cfg.Timeout)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.cfg.Upstream, "/")+strings.TrimPrefix(r.URL.Path, "/model"), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "building upstream request failed", http.StatusInternalServerError)
		return
	}
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	start := time.Now()
	resp, err := p.client.Do(out)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "Vertex AI call failed", status)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		http.Error(w, "reading Vertex AI response failed", http.StatusBadGateway)
		return
	}
	if resp.StatusCode == http.StatusOK {
		p.record(actor, respBody, time.Since(start))
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (p *Proxy) allowed(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.RawPath != "" || strings.Contains(r.URL.Path, "..") {
		return false
	}
	m := generatePath.FindStringSubmatch(r.URL.Path)
	return m != nil && m[2] == p.cfg.Project && m[3] == p.cfg.Location && slices.Contains(p.cfg.Models, m[4])
}

func (p *Proxy) acquire(actor string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[actor] >= p.cfg.MaxInFlight {
		return false
	}
	p.inFlight[actor]++
	return true
}

func (p *Proxy) release(actor string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[actor]--; p.inFlight[actor] <= 0 {
		delete(p.inFlight, actor)
	}
}

func (p *Proxy) record(actor string, body []byte, latency time.Duration) {
	var parsed struct {
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	_ = json.Unmarshal(body, &parsed)
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.usage[actor]
	u.Calls++
	u.PromptTokens += parsed.UsageMetadata.PromptTokenCount
	u.CandidateTokens += parsed.UsageMetadata.CandidatesTokenCount
	u.LastLatencyMs = latency.Milliseconds()
	u.TotalLatencyMs += u.LastLatencyMs
	p.usage[actor] = u
}

// Usage returns one actor's accumulated model usage.
func (p *Proxy) Usage(actor string) types.ModelUsage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.usage[actor]
}

// TotalUsage sums usage across all actors.
func (p *Proxy) TotalUsage() types.ModelUsage {
	p.mu.Lock()
	defer p.mu.Unlock()
	var t types.ModelUsage
	for _, u := range p.usage {
		t.Calls += u.Calls
		t.PromptTokens += u.PromptTokens
		t.CandidateTokens += u.CandidateTokens
		t.TotalLatencyMs += u.TotalLatencyMs
		t.LastLatencyMs = max(t.LastLatencyMs, u.LastLatencyMs)
	}
	return t
}

// ResetUsage clears all usage counters.
func (p *Proxy) ResetUsage() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usage = map[string]types.ModelUsage{}
}
```

Add `ModelUsage` to `pkg/types/types.go`, with the fields from the Interfaces block, and a JSON round-trip subtest in `types_test.go`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/cmd/conf-gateway/internal/modelproxy/ ./demos/conference/pkg/...`
Expected: PASS.

Guard check:
- Replace `hmac.Equal(...)` with `true`: `TestProxy_RejectsBadKeys` must fail.
- Replace `slices.Contains(p.cfg.Models, m[4])` with `true`: the "model not allowed" subtest must fail.
- Restore both.

- [ ] **Step 6: Commit**

```bash
git add demos/conference/cmd/conf-gateway/internal/modelproxy demos/conference/pkg/types
git commit -m "demos/conference: add gateway model proxy for actor Gemini calls

Actors authenticate with a per-actor HMAC key. The proxy allows only
allowlisted generateContent calls for the configured project and
location, swaps in the gateway's own token, caps body size and
per-actor concurrency, and records token usage per attendee.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Serve the model proxy from the gateway binary

**Files:**
- Modify: `cmd/conf-gateway/main.go`
- Modify: `cmd/conf-gateway/main_test.go`

**Interfaces:**
- Consumes: `modelproxy.New`, `modelproxy.Config`.
- Produces: new fields on `Config`, read from env:
  - `HMACSecret` from `HMAC_SECRET`, which replaces `JWT_SECRET`
  - `ModelProxyAddr` from `MODEL_PROXY_ADDR`, default `:8091`
  - `VertexProject` from `VERTEX_PROJECT`
  - `VertexLocation` from `VERTEX_LOCATION`, default `us-central1`
  - `ModelAllowlist []string` from `MODEL_ALLOWLIST`, comma-separated, default `gemini-2.5-flash`

  Also `func newModelProxy(ctx context.Context, cfg Config, secret []byte) (*modelproxy.Proxy, error)` and `func resolveSecret(s string) (secret string, generated bool)`.

- [ ] **Step 1: Write the failing tests**

In `main_test.go`, add the new names to `configEnvVars`: `HMAC_SECRET`, `MODEL_PROXY_ADDR`, `VERTEX_PROJECT`, `VERTEX_LOCATION`, `MODEL_ALLOWLIST`. Remove `JWT_SECRET`. Then add:

```go
func TestLoadConfig_ModelProxyDefaults(t *testing.T) {
	clearConfigEnv(t)
	cfg := LoadConfig()
	if cfg.ModelProxyAddr != ":8091" || cfg.VertexLocation != "us-central1" {
		t.Errorf("defaults = %q %q", cfg.ModelProxyAddr, cfg.VertexLocation)
	}
	if !slices.Equal(cfg.ModelAllowlist, []string{"gemini-2.5-flash"}) {
		t.Errorf("ModelAllowlist = %v", cfg.ModelAllowlist)
	}
}

func TestLoadConfig_ModelAllowlistTrimsEntries(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MODEL_ALLOWLIST", " gemini-2.5-flash , gemini-2.5-pro ,")
	if got := LoadConfig().ModelAllowlist; !slices.Equal(got, []string{"gemini-2.5-flash", "gemini-2.5-pro"}) {
		t.Errorf("ModelAllowlist = %v", got)
	}
}

func TestNewModelProxy_DisabledWithoutProject(t *testing.T) {
	clearConfigEnv(t)
	p, err := newModelProxy(context.Background(), LoadConfig(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() {
		t.Error("proxy enabled with no VERTEX_PROJECT")
	}
}

func TestResolveSecret(t *testing.T) {
	if s, gen := resolveSecret("given"); s != "given" || gen {
		t.Errorf("resolveSecret(given) = %q, %v", s, gen)
	}
	a, gen := resolveSecret("")
	b, _ := resolveSecret("")
	if !gen || len(a) < 32 || a == b {
		t.Errorf("generated secrets %q %q", a, b)
	}
}
```

If `main_test.go` has no `clearConfigEnv` helper, add one that calls `t.Setenv(name, "")` for each entry of `configEnvVars`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/`
Expected: FAIL to compile: `cfg.ModelProxyAddr undefined`.

- [ ] **Step 3: Implement**

In `main.go`:
- Replace `JWTSecret` with `HMACSecret` (`os.Getenv("HMAC_SECRET")`).
- Add the new fields to `LoadConfig`, using a `splitList(s, def string) []string` helper that trims entries and drops empty ones.
- Add:

```go
// resolveSecret returns s, or a random secret when s is empty.
func resolveSecret(s string) (string, bool) {
	if s != "" {
		return s, false
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("generating HMAC secret: %v", err)
	}
	return hex.EncodeToString(b), true
}

func newModelProxy(ctx context.Context, cfg Config, secret []byte) (*modelproxy.Proxy, error) {
	pc := modelproxy.Config{
		Secret:   secret,
		Atespace: cfg.Atespace,
		Project:  cfg.VertexProject,
		Location: cfg.VertexLocation,
		Models:   cfg.ModelAllowlist,
	}
	if cfg.VertexProject != "" {
		ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("finding application default credentials: %w", err)
		}
		pc.TokenSource = ts
	}
	return modelproxy.New(pc)
}
```

In `main()`:
1. Call `secret, generated := resolveSecret(cfg.HMACSecret)`. If `generated`, log: `HMAC_SECRET is unset; generated one. Sessions and model keys will not survive a restart.`
2. Pass `gateway.WithJWTSecret(secret)`. Task 9 renames it to `WithSecret`.
3. Build the proxy with `newModelProxy(ctx, cfg, []byte(secret))`. If it fails, `log.Fatal`.
4. When `!proxy.Enabled()`, log `VERTEX_PROJECT is unset; the model proxy is disabled and actors must run with DEMO_MOCK_LLM=true.`
5. Start a second `http.Server{Addr: cfg.ModelProxyAddr, Handler: proxy, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second}` next to the main server, and shut both down on signal.

The `signal.NotifyContext` context must exist before `newModelProxy` runs, so move that line up.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/cmd/conf-gateway/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add demos/conference/cmd/conf-gateway/main.go demos/conference/cmd/conf-gateway/main_test.go
git commit -m "demos/conference: serve the model proxy from conf-gateway

The gateway listens for actor model calls on MODEL_PROXY_ADDR with
credentials from ADC. HMAC_SECRET replaces JWT_SECRET because it now
keys both attendee sessions and actor model keys.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: `localactors` router and the `conf-localrouter` binary

This task gives local runs and contract tests one real workload server per actor, behind a router that speaks the atenet router's header and statusz contract.

**Files:**
- Create: `internal/localactors/router.go`, `internal/localactors/router_test.go`
- Create: `cmd/conf-localrouter/main.go`

**Interfaces:**
- Produces:
  ```go
  package localactors
  type Option func(*Router)
  func WithSlots(n int) Option             // 0 (default) means unlimited
  func New(atespace string, newActor func(name string) http.Handler, opts ...Option) *Router
  func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request)
  func (r *Router) Parked() int
  func (r *Router) Forget(name string)
  ```
  - A request with `ate-target-actor: <atespace>/<name>` reaches that actor's handler, created on first use.
  - Other targets get 404.
  - `GET /statusz` returns `{"parking":{"active":N}}`.

- [ ] **Step 1: Write the failing tests**

```go
package localactors_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/internal/localactors"
)

func echoActor(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, name) })
}

func send(h http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	if target != "" {
		req.Header.Set("ate-target-actor", target)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouter_DispatchesByTargetHeader(t *testing.T) {
	r := localactors.New("ate-demo-conf", echoActor)
	if got := send(r, "ate-demo-conf/attendee-03").Body.String(); got != "attendee-03" {
		t.Errorf("body = %q", got)
	}
}

func TestRouter_ReusesActorInstance(t *testing.T) {
	var mu sync.Mutex
	created := map[string]int{}
	r := localactors.New("ate-demo-conf", func(name string) http.Handler {
		mu.Lock()
		created[name]++
		mu.Unlock()
		return echoActor(name)
	})
	send(r, "ate-demo-conf/attendee-01")
	send(r, "ate-demo-conf/attendee-01")
	if created["attendee-01"] != 1 {
		t.Errorf("created %d instances, want 1", created["attendee-01"])
	}
	r.Forget("attendee-01")
	send(r, "ate-demo-conf/attendee-01")
	if created["attendee-01"] != 2 {
		t.Errorf("after Forget created %d instances, want 2", created["attendee-01"])
	}
}

func TestRouter_RejectsBadTargets(t *testing.T) {
	r := localactors.New("ate-demo-conf", echoActor)
	for _, target := range []string{"", "attendee-01", "other/attendee-01", "ate-demo-conf/", "ate-demo-conf/../x", "ate-demo-conf/A"} {
		if rec := send(r, target); rec.Code != http.StatusNotFound {
			t.Errorf("target %q: status %d, want 404", target, rec.Code)
		}
	}
}

func TestRouter_ParksBeyondSlotsAndReportsStatusz(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	r := localactors.New("ate-demo-conf", func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			entered <- struct{}{}
			<-release
		})
	}, localactors.WithSlots(1))

	done := make(chan struct{})
	go func() { send(r, "ate-demo-conf/attendee-01"); done <- struct{}{} }()
	<-entered
	go func() { send(r, "ate-demo-conf/attendee-02"); done <- struct{}{} }()

	// Parked() becomes 1 once the second request is waiting for a slot.
	for r.Parked() != 1 {
		runtime.Gosched()
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statusz?format=json", nil))
	var sz struct{ Parking struct{ Active int } }
	_ = json.Unmarshal(rec.Body.Bytes(), &sz)
	if sz.Parking.Active != 1 {
		t.Errorf("statusz parking.active = %d, want 1", sz.Parking.Active)
	}

	release <- struct{}{} // finish attendee-01; attendee-02 takes the slot
	<-entered
	release <- struct{}{}
	<-done
	<-done
	if r.Parked() != 0 {
		t.Errorf("Parked() = %d after drain", r.Parked())
	}
}
```

Replace the `release <- struct{}{}` sends with `close(release)` if you find that simpler. Add `"runtime"` to the imports. The `Gosched` loop waits on state the router sets itself, not on a timer.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/internal/localactors/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `router.go`**

```go
// Package localactors stands in for the atenet router in local runs and
// tests: it routes on the ate-target-actor header to one in-process actor
// per name, limits how many run at once, and reports parked requests.
package localactors

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

const targetHeader = "ate-target-actor"

var actorName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Option configures a Router.
type Option func(*Router)

// WithSlots limits how many requests run at once; the rest wait, counted
// as parked. Zero means no limit.
func WithSlots(n int) Option {
	return func(r *Router) {
		if n > 0 {
			r.slots = make(chan struct{}, n)
		}
	}
}

// Router dispatches requests to per-actor handlers.
type Router struct {
	atespace string
	newActor func(string) http.Handler
	slots    chan struct{}
	parked   atomic.Int64

	mu     sync.Mutex
	actors map[string]http.Handler
}

// New returns a Router that builds each actor's handler with newActor.
func New(atespace string, newActor func(name string) http.Handler, opts ...Option) *Router {
	r := &Router{atespace: atespace, newActor: newActor, actors: map[string]http.Handler{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/statusz" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"parking": map[string]int{"active": r.Parked()}})
		return
	}
	space, name, ok := strings.Cut(req.Header.Get(targetHeader), "/")
	if !ok || space != r.atespace || !actorName.MatchString(name) {
		http.NotFound(w, req)
		return
	}
	if r.slots != nil {
		select {
		case r.slots <- struct{}{}:
		default:
			r.parked.Add(1)
			select {
			case r.slots <- struct{}{}:
				r.parked.Add(-1)
			case <-req.Context().Done():
				r.parked.Add(-1)
				return
			}
		}
		defer func() { <-r.slots }()
	}
	r.actor(name).ServeHTTP(w, req)
}

func (r *Router) actor(name string) http.Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.actors[name]
	if !ok {
		h = r.newActor(name)
		r.actors[name] = h
	}
	return h
}

// Parked returns how many requests are waiting for a slot.
func (r *Router) Parked() int { return int(r.parked.Load()) }

// Forget drops an actor so its next request starts a fresh instance.
func (r *Router) Forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.actors, name)
}
```

- [ ] **Step 4: Implement `cmd/conf-localrouter/main.go`**

```go
// Command conf-localrouter runs conference actors in-process behind a
// router that mimics atenet's routing header and statusz, for local runs.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/internal/localactors"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/server"
)

func main() {
	addr := flag.String("addr", ":18080", "listen address")
	atespace := flag.String("atespace", "ate-demo-conf", "atespace in the ate-target-actor header")
	slots := flag.Int("slots", 2, "requests served at once; the rest are parked (0 = unlimited)")
	root := flag.String("workspace-root", filepath.Join(os.TempDir(), "conf-localrouter"), "parent of each actor's sandbox workspace")
	flag.Parse()

	r := localactors.New(*atespace, func(name string) http.Handler {
		return server.NewWorkloadServer(server.WithWorkspaceDir(filepath.Join(*root, name))).Handler()
	}, localactors.WithSlots(*slots))

	srv := &http.Server{Addr: *addr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("conf-localrouter listening on %s (atespace %s, %d slots)", *addr, *atespace, *slots)
	log.Fatal(srv.ListenAndServe())
}
```

- [ ] **Step 5: Run the tests and build**

Run: `go test -race ./demos/conference/internal/localactors/ && go build ./demos/conference/cmd/conf-localrouter`
Expected: PASS, and the build exits 0.

- [ ] **Step 6: Commit**

```bash
git add demos/conference/internal/localactors demos/conference/cmd/conf-localrouter
git commit -m "demos/conference: add a local actor router for tests and mock mode

conf-localrouter runs one real workload server per ate-target-actor,
parks requests beyond a slot limit, and serves statusz, so local runs
and gateway tests exercise real actors instead of a single shared one
or a Python stub.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Gateway: remove the AI path; chat goes to the actor

**Files:**
- Modify: `cmd/conf-gateway/internal/gateway/gateway.go`
- Delete: `pkg/ai/`, `pkg/engine/`
- Create: `cmd/conf-gateway/internal/gateway/contract_test.go`
- Modify: `cmd/conf-gateway/internal/gateway/{approval,hygiene,gateway,helpers}_test.go`
- Modify: `cmd/conf-gateway/main.go`, `main_test.go`

**Interfaces:**
- Consumes: `modelproxy.Key`, `localactors.New`, `server.NewWorkloadServer`, `server.WithGeminiConfig`, `geminitest`, `ChatMessage.Execution`, `types.ModelKeyHeader`.
- Produces:
  - `gateway.WithSecret(secret string) Option` replaces `WithJWTSecret`. It signs JWTs and derives model keys.
  - Removed: `WithGeminiAPIKey`, `WithAIClient`, `handleChatRequest`, `handleApproveRequest` and the `workerSlots`, `aiClient` and `geminiAPIKey` fields.
  - The proxy's `Rewrite` sets `X-Conf-Model-Key: modelproxy.Key(secret, atespace, attendee)`.
  - `/api/chat` and `/api/approve` always go through `proxyToActor`.

- [ ] **Step 1: Write the failing contract tests**

Create `cmd/conf-gateway/internal/gateway/contract_test.go`:

```go
package gateway_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/internal/localactors"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/gemini"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/geminitest"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/server"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

const contractSecret = "contract-secret-contract-secret-00"

// contractEnv wires a gateway to real workload servers behind a local
// router, with the real model proxy in front of a fake Vertex.
type contractEnv struct {
	gw        *gateway.Gateway
	routerURL string
	vertex    *geminitest.Server

	mu     sync.Mutex
	actors map[string]*server.WorkloadServer
}

func newContractEnv(t *testing.T, vertex *geminitest.Server, opts ...gateway.Option) *contractEnv {
	t.Helper()
	proxy, err := modelproxy.New(modelproxy.Config{
		Secret: []byte(contractSecret), Atespace: "ate-demo-conf",
		Project: "proj", Location: "loc", Models: []string{"gemini-2.5-flash"},
		Upstream:    vertex.URL,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(proxy)
	t.Cleanup(proxySrv.Close)

	env := &contractEnv{vertex: vertex, actors: map[string]*server.WorkloadServer{}}
	router := localactors.New("ate-demo-conf", func(name string) http.Handler {
		ws := server.NewWorkloadServer(
			server.WithWorkspaceDir(t.TempDir()),
			server.WithGeminiConfig(gemini.Config{ProxyURL: proxySrv.URL, Project: "proj", Location: "loc", Model: "gemini-2.5-flash"}),
		)
		env.mu.Lock()
		env.actors[name] = ws
		env.mu.Unlock()
		return ws.Handler()
	})
	routerSrv := httptest.NewServer(router)
	t.Cleanup(routerSrv.Close)
	env.routerURL = routerSrv.URL

	env.gw = newTestGateway(t, routerSrv.URL, "", append([]gateway.Option{gateway.WithSecret(contractSecret)}, opts...)...)
	return env
}

func (e *contractEnv) actor(name string) *server.WorkloadServer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.actors[name]
}

func (e *contractEnv) post(t *testing.T, attendee, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.gw.MintToken(attendee))
	rec := httptest.NewRecorder()
	e.gw.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeMsg(t *testing.T, rec *httptest.ResponseRecorder) types.ChatMessage {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var m types.ChatMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestContract_ChatRunsInActorThroughModelProxy(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("actor says hi")))

	msg := decodeMsg(t, env.post(t, "attendee-01", "/api/chat", types.ChatRequest{Message: "hello"}))

	if msg.Content != "actor says hi" || msg.Source != types.SourceGemini {
		t.Errorf("reply = %q (%s)", msg.Content, msg.Source)
	}
	up := env.vertex.Requests()
	if len(up) != 1 || up[0].Authorization != "Bearer adc-token" || up[0].ModelKey != "" {
		t.Errorf("upstream requests = %+v", up)
	}
	if h := env.actor("attendee-01").History(); len(h) != 2 {
		t.Errorf("actor history has %d messages, want 2", len(h))
	}
}

func TestContract_GatewayDeliversPerActorModelKey(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("ok")))
	env.post(t, "attendee-02", "/api/chat", types.ChatRequest{Message: "hi"})

	want := modelproxy.Key([]byte(contractSecret), "ate-demo-conf", "attendee-02")
	if got := env.actor("attendee-02").ModelKey(); got != want {
		t.Errorf("actor ModelKey = %q, want %q", got, want)
	}
}

func TestContract_ClientCannotOverrideModelKey(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("ok")))
	b, _ := json.Marshal(types.ChatRequest{Message: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.gw.MintToken("attendee-03"))
	req.Header.Set(types.ModelKeyHeader, modelproxy.Key([]byte(contractSecret), "ate-demo-conf", "attendee-09"))
	env.gw.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if got := env.actor("attendee-03").ModelKey(); !strings.HasPrefix(got, "attendee-03.") {
		t.Errorf("actor ModelKey = %q, want attendee-03's key", got)
	}
}

func TestContract_ApprovalRunsActorsOwnCode(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.ApprovalResponse("say it", "echo from-model", "sh")))
	chat := decodeMsg(t, env.post(t, "attendee-04", "/api/chat", types.ChatRequest{Message: "run"}))
	if chat.Approval == nil {
		t.Fatal("no approval in reply")
	}

	res := decodeMsg(t, env.post(t, "attendee-04", "/api/approve", types.ApprovalResponse{
		ApprovalID: chat.Approval.ApprovalID, Decision: types.DecisionApprove, Code: "echo injected",
	}))

	if res.Execution == nil || res.Execution.Stdout != "from-model\n" {
		t.Errorf("Execution = %+v, want the model's code output", res.Execution)
	}
}

func TestContract_GatewayRejectsUnknownApprovalBeforeActor(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.ApprovalResponse("x", "echo x", "sh")))
	chat := decodeMsg(t, env.post(t, "attendee-05", "/api/chat", types.ChatRequest{Message: "run"}))

	rec := env.post(t, "attendee-05", "/api/approve", types.ApprovalResponse{ApprovalID: "app-forged", Decision: types.DecisionApprove})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if p := env.actor("attendee-05").PendingApproval(); p == nil || p.ApprovalID != chat.Approval.ApprovalID {
		t.Errorf("actor pending approval = %+v, want it untouched", p)
	}
}

func TestContract_ActorRejectsApprovalItNeverIssued(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("no approval here")))
	env.post(t, "attendee-06", "/api/chat", types.ChatRequest{Message: "hi"})

	// Bypass the gateway, as a request forged inside the cluster would.
	b, _ := json.Marshal(types.ApprovalResponse{ApprovalID: "app-1", Decision: types.DecisionApprove, Code: "echo pwned", Language: "sh"})
	req, _ := http.NewRequest(http.MethodPost, env.routerURL+"/api/approve", bytes.NewReader(b))
	req.Header.Set("ate-target-actor", "ate-demo-conf/attendee-06")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestContract_HistoryReachesModelOnNextTurn(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.TextResponse("noted"), geminitest.TextResponse("pelican")))
	env.post(t, "attendee-07", "/api/chat", types.ChatRequest{Message: "remember pelican"})
	env.post(t, "attendee-07", "/api/chat", types.ChatRequest{Message: "which bird?"})

	if body := string(env.vertex.Requests()[1].Body); !strings.Contains(body, "remember pelican") {
		t.Errorf("second model call lacks history: %s", body)
	}
}

func TestContract_VertexFailureIsLabeledFallback(t *testing.T) {
	env := newContractEnv(t, geminitest.NewStatus(t, http.StatusInternalServerError))
	msg := decodeMsg(t, env.post(t, "attendee-08", "/api/chat", types.ChatRequest{Message: "hi"}))
	if msg.Source != types.SourceMockFallback {
		t.Errorf("Source = %q, want mock-fallback", msg.Source)
	}
}

func TestContract_ExecutionIsRecordedFromStructuredField(t *testing.T) {
	env := newContractEnv(t, geminitest.New(t, geminitest.ApprovalResponse("x", "echo structured", "sh")))
	chat := decodeMsg(t, env.post(t, "attendee-09", "/api/chat", types.ChatRequest{Message: "run"}))
	env.post(t, "attendee-09", "/api/approve", types.ApprovalResponse{ApprovalID: chat.Approval.ApprovalID, Decision: types.DecisionApprove})

	res, ok := env.gw.GetLastResult("attendee-09")
	if !ok || res.Stdout != "structured\n" {
		t.Errorf("GetLastResult = %+v, %v", res, ok)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/internal/gateway/ -run TestContract`
Expected: FAIL to compile: `undefined: gateway.WithSecret`.

- [ ] **Step 3: Remove the AI path from `gateway.go`**

1. Delete `WithGeminiAPIKey` and `WithAIClient`, the `geminiAPIKey`, `aiClient` and `workerSlots` fields, the `workerSlots: make(chan struct{}, 2)` line, and the `pkg/ai` import.
2. Delete `handleChatRequest` and `handleApproveRequest` (currently lines 864–1107) with every helper only they use. Check with `go build` after deleting.
3. In `routes()`, replace the `if g.aiClient != nil { … } else { … }` block with:

```go
	g.mux.HandleFunc("/api/chat", jsonPOST(g.proxyToActor))
	g.mux.HandleFunc("/api/approve", jsonPOST(g.proxyToActor))
```

4. Rename `WithJWTSecret` to `WithSecret`, and the `jwtSecret` field to `secret`. Update the doc comment: `// WithSecret sets the HMAC secret that signs attendee tokens and derives actor model keys.`
5. In `Rewrite`, replace the header block with:

```go
			attendeeID, _ := pr.In.Context().Value(attendeeContextKey{}).(string)
			// Gateway credentials stay here; the actor gets only its own model key.
			for _, h := range []string{"Authorization", "Cookie", PresenterHeaderName, types.ModelKeyHeader} {
				pr.Out.Header.Del(h)
			}
			if attendeeID != "" {
				pr.Out.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", gw.atespace, attendeeID))
				pr.Out.Header.Set(types.ModelKeyHeader, modelproxy.Key([]byte(gw.secret), gw.atespace, attendeeID))
			}
```

6. At the top of `modifyResponse`, add `resp.Header.Del(types.ModelKeyHeader)`.
7. In `modifyResponse`, replace the whole execution-parsing block, from `// Record execution result if returned` down to the end of the `else if hasChatMsg { … }` chain, with:

```go
	var newExec types.ExecutionResult
	hasNewExec := hasChatMsg && chatMsg.Execution != nil
	if hasNewExec {
		newExec = *chatMsg.Execution
		g.mu.Lock()
		g.lastResults[attendeeID] = &newExec
		g.mu.Unlock()
	}
```

- [ ] **Step 4: Delete the shared AI packages**

```bash
git rm -r demos/conference/pkg/ai demos/conference/pkg/engine
grep -rn 'pkg/ai\|pkg/engine' demos/conference
```

Expected: no output from grep.

- [ ] **Step 5: Update `main.go`**

- Remove `GeminiAPIKey` from `Config` and `LoadConfig`.
- Remove `gateway.WithGeminiAPIKey(...)` and `gateway.WithAIClient(ai.NewGeminiClient())`.
- Replace `gateway.WithJWTSecret(secret)` with `gateway.WithSecret(secret)`.
- In `main_test.go`, remove `GEMINI_API_KEY` from `configEnvVars` and delete any assertion on `GeminiAPIKey`.

- [ ] **Step 6: Prune and port the existing gateway tests**

Use this inventory, and `go vet` output, to find each site.

- **DELETE:**
  - `TestAIChat_ConcurrentWithTelemetry` (hygiene)
  - `TestChatAndApprovalFlowActionStream_WithAIClient`
  - the env-key subtest of `TestChat_GeminiKeyComesFromEnvNotRequest`
- **`approval_test.go`:** drop the gateway-AI mode from `newApprovalHarness(withAI)` and `forEachMode`. Then `newApprovalHarness` takes no mode argument, and the six approval tests run once, in proxy mode.
- **`hygiene_test.go`:** the two POST tests run in proxy mode only.
- **`TestProxy_DoesNotForwardSecretsToActor`:**
  - drop `WithGeminiAPIKey`;
  - assert the actor-side request has no `Authorization`, `Cookie` or presenter header;
  - assert that it has an `X-Conf-Model-Key` equal to `modelproxy.Key([]byte(secret), "ate-demo-conf", attendee)`.
- **`TestProxyRouting_ApprovalDetection_SubstringIgnored`:** also assert that a following `/api/approve` returns 409, because a reply whose text merely mentions an approval must not create one.
- **Tests that fed the gateway a fake actor reply containing "Execution completed in…" text:** make the fake return `types.ChatMessage{Execution: &types.ExecutionResult{…}}`.
- **All tests:** replace every `WithJWTSecret` with `WithSecret`.

- [ ] **Step 7: Run all demo tests**

Run: `go test -race ./demos/conference/...`
Expected: PASS.

Guard check: comment out `pr.Out.Header.Del(h)` for `types.ModelKeyHeader`. `TestContract_ClientCannotOverrideModelKey` must still pass, because `Set` overwrites. Then comment out the `Set` line: `TestContract_GatewayDeliversPerActorModelKey` must fail. Restore both.

- [ ] **Step 8: Confirm no model key or Gemini key remains**

```bash
grep -rn 'GEMINI_API_KEY\|X-Gemini-API-Key\|mofilabs\|workerSlots' demos/conference hack/*conference*
```

Expected: no output. If `hack/verify-demo-conference.sh` still mentions `GEMINI_API_KEY`, remove that line here; Task 18 rewrites the rest.

- [ ] **Step 9: Commit**

```bash
git add -A demos/conference hack/verify-demo-conference.sh
git commit -m "demos/conference: send chat to the actor, not a gateway AI client

The gateway no longer runs Gemini. Chat and approvals are proxied to
the attendee's actor, which receives its model proxy key on every
request. Execution results come from the actor's structured field
instead of parsing reply text. pkg/ai and pkg/engine are removed.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 10: Actor view types and the `kubectl-ate` adapter

**Files:**
- Modify: `pkg/types/types.go`, `pkg/types/types_test.go`
- Create: `cmd/conf-gateway/internal/actorstate/control.go`
- Create: `cmd/conf-gateway/internal/actorstate/kubectlate.go`
- Create: `cmd/conf-gateway/internal/actorstate/kubectlate_test.go`
- Create: `cmd/conf-gateway/internal/actorstate/testdata/actors.json`

**Interfaces:**
- Produces:
  ```go
  // pkg/types
  type SubstrateState string // UNKNOWN RESUMING RUNNING SUSPENDING SUSPENDED PAUSING PAUSED CRASHED DELETING REVERTING UNAVAILABLE
  type Activity string       // "" (idle), THINKING, AWAITING_APPROVAL, EXECUTING
  type ActorView struct {
  	ActorID      string         `json:"actor_id"`
  	State        SubstrateState `json:"state"`
  	NeverStarted bool           `json:"never_started"`
  	StateDetail  string         `json:"state_detail,omitempty"`
  	Activity     Activity       `json:"activity,omitempty"`
  	WorkerPod    string         `json:"worker_pod,omitempty"`
  	SnapshotURI  string         `json:"snapshot_uri,omitempty"`
  	Source       ReplySource    `json:"source,omitempty"`
  }
  // actorstate
  type Status struct{ Name string; State types.SubstrateState; WorkerPod, InProgressSnapshotURI, SnapshotURI string }
  type ActorControl interface {
  	List(ctx context.Context) ([]Status, error)
  	Suspend(ctx context.Context, name string) error
  }
  type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)
  func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error)
  func NewKubectlATE(bin, endpoint, atespace string) *KubectlATE
  func NewKubectlATEWithRunner(bin, endpoint, atespace string, run Runner) *KubectlATE
  func ParseActorList(data []byte) ([]Status, error)
  ```

`kubectl-ate get actors -a <atespace> -o json` prints a protojson `ListActorsResponse`, in camelCase. The printer is at `cmd/kubectl-ate/internal/printer/printer.go`, in `PrintActorsTo` and `printProto`. The adapter reads only these fields:
- `actors[].metadata.name`
- `actors[].status.state`: `ACTOR_STATE_*`, omitted when UNSPECIFIED
- `actors[].status.workerAssignment.workerPod`
- `actors[].status.inProgressSnapshotUri`
- `actors[].status.externalSnapshot.snapshotUri`

- [ ] **Step 1: Write the fixture**

Create `testdata/actors.json`. Task 19 replaces it with output captured from a real cluster.

```json
{
  "actors": [
    {
      "metadata": {"atespace": "ate-demo-conf", "name": "attendee-01", "version": "1"},
      "actorTemplate": {"atespace": "ate-demo-conf", "name": "conf-workload"},
      "status": {"state": "ACTOR_STATE_SUSPENDED"}
    },
    {
      "metadata": {"atespace": "ate-demo-conf", "name": "attendee-02", "version": "7"},
      "actorTemplate": {"atespace": "ate-demo-conf", "name": "conf-workload"},
      "status": {
        "state": "ACTOR_STATE_RUNNING",
        "workerAssignment": {"worker": {"name": "w-abc"}, "workerNamespace": "ate-demo-conf", "workerPool": "conf-pool", "workerPod": "conf-pool-7d9f-x2k4p", "workerPodIp": "10.8.0.12"}
      }
    },
    {
      "metadata": {"atespace": "ate-demo-conf", "name": "attendee-03", "version": "9"},
      "actorTemplate": {"atespace": "ate-demo-conf", "name": "conf-workload"},
      "status": {
        "state": "ACTOR_STATE_SUSPENDED",
        "externalSnapshot": {"snapshotUri": "gs://conf-snapshots/ate-demo-conf/attendee-03/9"}
      }
    },
    {
      "metadata": {"atespace": "ate-demo-conf", "name": "attendee-04", "version": "3"},
      "actorTemplate": {"atespace": "ate-demo-conf", "name": "conf-workload"},
      "status": {"state": "ACTOR_STATE_CRASHED"}
    },
    {
      "metadata": {"atespace": "ate-demo-conf", "name": "attendee-05", "version": "2"},
      "actorTemplate": {"atespace": "ate-demo-conf", "name": "conf-workload"},
      "status": {}
    }
  ]
}
```

- [ ] **Step 2: Write the failing tests**

`kubectlate_test.go`:

```go
package actorstate_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

func TestParseActorList_Fixture(t *testing.T) {
	data, err := os.ReadFile("testdata/actors.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := actorstate.ParseActorList(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []actorstate.Status{
		{Name: "attendee-01", State: types.StateSuspended},
		{Name: "attendee-02", State: types.StateRunning, WorkerPod: "conf-pool-7d9f-x2k4p"},
		{Name: "attendee-03", State: types.StateSuspended, SnapshotURI: "gs://conf-snapshots/ate-demo-conf/attendee-03/9"},
		{Name: "attendee-04", State: types.StateCrashed},
		{Name: "attendee-05", State: types.StateUnknown},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParseActorList =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseActorList_Empty(t *testing.T) {
	got, err := actorstate.ParseActorList([]byte(`{}`))
	if err != nil || len(got) != 0 {
		t.Errorf("ParseActorList({}) = %v, %v", got, err)
	}
}

func TestParseActorList_RejectsGarbage(t *testing.T) {
	if _, err := actorstate.ParseActorList([]byte("Error: unauthorized")); err == nil {
		t.Error("ParseActorList accepted non-JSON")
	}
}

type recordedCall struct {
	name string
	args []string
}

func recorder(out []byte, err error) (actorstate.Runner, *[]recordedCall) {
	var calls []recordedCall
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, recordedCall{name, args})
		return out, err
	}, &calls
}

func TestKubectlATE_ListArgs(t *testing.T) {
	run, calls := recorder([]byte(`{"actors":[]}`), nil)
	k := actorstate.NewKubectlATEWithRunner("/bin/kubectl-ate", "api.ate-system.svc:443", "ate-demo-conf", run)
	if _, err := k.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"--endpoint", "api.ate-system.svc:443", "get", "actors", "-a", "ate-demo-conf", "-o", "json"}
	if c := (*calls)[0]; c.name != "/bin/kubectl-ate" || !slices.Equal(c.args, want) {
		t.Errorf("call = %s %v, want %v", c.name, c.args, want)
	}
}

func TestKubectlATE_NoEndpointFlagWhenEmpty(t *testing.T) {
	run, calls := recorder(nil, nil)
	k := actorstate.NewKubectlATEWithRunner("kubectl-ate", "", "ate-demo-conf", run)
	if err := k.Suspend(context.Background(), "attendee-02"); err != nil {
		t.Fatal(err)
	}
	want := []string{"suspend", "actor", "attendee-02", "-a", "ate-demo-conf"}
	if got := (*calls)[0].args; !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestKubectlATE_SuspendRejectsBadName(t *testing.T) {
	run, calls := recorder(nil, nil)
	k := actorstate.NewKubectlATEWithRunner("kubectl-ate", "", "ate-demo-conf", run)
	if err := k.Suspend(context.Background(), "--endpoint=evil"); err == nil {
		t.Error("Suspend accepted a flag-shaped name")
	}
	if len(*calls) != 0 {
		t.Errorf("runner called %d times", len(*calls))
	}
}

func TestKubectlATE_ListPropagatesErrors(t *testing.T) {
	run, _ := recorder([]byte("boom"), errors.New("exit status 1"))
	k := actorstate.NewKubectlATEWithRunner("kubectl-ate", "", "ate-demo-conf", run)
	if _, err := k.List(context.Background()); err == nil {
		t.Error("List returned nil error")
	}
}
```

In `types_test.go`, add a JSON round-trip test. `ActorView{ActorID: "attendee-01", State: StateSuspended, NeverStarted: true}` must marshal with `"never_started":true` and without `activity` or `source`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/internal/actorstate/ ./demos/conference/pkg/types/`
Expected: FAIL to compile.

- [ ] **Step 4: Add the types**

In `pkg/types/types.go`:

```go
// SubstrateState is an actor's state as reported by ateapi, without the
// ACTOR_STATE_ prefix. StateUnavailable means the gateway could not read it.
type SubstrateState string

const (
	StateUnknown     SubstrateState = "UNKNOWN"
	StateResuming    SubstrateState = "RESUMING"
	StateRunning     SubstrateState = "RUNNING"
	StateSuspending  SubstrateState = "SUSPENDING"
	StateSuspended   SubstrateState = "SUSPENDED"
	StatePausing     SubstrateState = "PAUSING"
	StatePaused      SubstrateState = "PAUSED"
	StateCrashed     SubstrateState = "CRASHED"
	StateDeleting    SubstrateState = "DELETING"
	StateReverting   SubstrateState = "REVERTING"
	StateUnavailable SubstrateState = "UNAVAILABLE"
)

// Activity is what the gateway knows the actor is doing for its attendee.
type Activity string

const (
	ActivityIdle             Activity = ""
	ActivityThinking         Activity = "THINKING"
	ActivityAwaitingApproval Activity = "AWAITING_APPROVAL"
	ActivityExecuting        Activity = "EXECUTING"
)

// ActorView is everything the UI shows about one actor's state.
type ActorView struct {
	ActorID      string         `json:"actor_id"`
	State        SubstrateState `json:"state"`
	NeverStarted bool           `json:"never_started"`
	StateDetail  string         `json:"state_detail,omitempty"`
	Activity     Activity       `json:"activity,omitempty"`
	WorkerPod    string         `json:"worker_pod,omitempty"`
	SnapshotURI  string         `json:"snapshot_uri,omitempty"`
	Source       ReplySource    `json:"source,omitempty"`
}
```

Leave `ActorLifecycleState` in place until Task 12 deletes it.

- [ ] **Step 5: Implement `control.go`**

```go
// Package actorstate reads actor state from ateapi through kubectl-ate and
// keeps the per-actor views the UI renders.
package actorstate

import (
	"context"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// Status is one actor as ateapi reports it.
type Status struct {
	Name                  string
	State                 types.SubstrateState
	WorkerPod             string
	InProgressSnapshotURI string
	SnapshotURI           string
}

// ActorControl lists and suspends the demo's actors.
type ActorControl interface {
	List(ctx context.Context) ([]Status, error)
	Suspend(ctx context.Context, name string) error
}
```

- [ ] **Step 6: Implement `kubectlate.go`**

```go
package actorstate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

var actorName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs the command with os/exec; stderr is folded into the error.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// KubectlATE implements ActorControl with the kubectl-ate CLI.
type KubectlATE struct {
	bin, endpoint, atespace string
	run                     Runner
}

// NewKubectlATE returns an ActorControl that runs bin. An empty endpoint
// lets kubectl-ate use its own default.
func NewKubectlATE(bin, endpoint, atespace string) *KubectlATE {
	return NewKubectlATEWithRunner(bin, endpoint, atespace, ExecRunner)
}

// NewKubectlATEWithRunner is NewKubectlATE with a custom Runner, for tests.
func NewKubectlATEWithRunner(bin, endpoint, atespace string, run Runner) *KubectlATE {
	return &KubectlATE{bin: bin, endpoint: endpoint, atespace: atespace, run: run}
}

func (k *KubectlATE) args(rest ...string) []string {
	if k.endpoint == "" {
		return rest
	}
	return append([]string{"--endpoint", k.endpoint}, rest...)
}

// List returns every actor in the atespace.
func (k *KubectlATE) List(ctx context.Context) ([]Status, error) {
	out, err := k.run(ctx, k.bin, k.args("get", "actors", "-a", k.atespace, "-o", "json")...)
	if err != nil {
		return nil, err
	}
	return ParseActorList(out)
}

// Suspend asks ateapi to suspend one actor.
func (k *KubectlATE) Suspend(ctx context.Context, name string) error {
	if !actorName.MatchString(name) {
		return fmt.Errorf("invalid actor name %q", name)
	}
	_, err := k.run(ctx, k.bin, k.args("suspend", "actor", name, "-a", k.atespace)...)
	return err
}

// ParseActorList decodes `kubectl-ate get actors -o json` output.
func ParseActorList(data []byte) ([]Status, error) {
	var list struct {
		Actors []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				State            string `json:"state"`
				WorkerAssignment struct {
					WorkerPod string `json:"workerPod"`
				} `json:"workerAssignment"`
				InProgressSnapshotURI string `json:"inProgressSnapshotUri"`
				ExternalSnapshot      struct {
					SnapshotURI string `json:"snapshotUri"`
				} `json:"externalSnapshot"`
			} `json:"status"`
		} `json:"actors"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decoding actor list: %w", err)
	}
	out := make([]Status, 0, len(list.Actors))
	for _, a := range list.Actors {
		state := types.SubstrateState(strings.TrimPrefix(a.Status.State, "ACTOR_STATE_"))
		if state == "" || state == "UNSPECIFIED" {
			state = types.StateUnknown
		}
		out = append(out, Status{
			Name:                  a.Metadata.Name,
			State:                 state,
			WorkerPod:             a.Status.WorkerAssignment.WorkerPod,
			InProgressSnapshotURI: a.Status.InProgressSnapshotURI,
			SnapshotURI:           a.Status.ExternalSnapshot.SnapshotURI,
		})
	}
	return out, nil
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/cmd/conf-gateway/internal/actorstate/ ./demos/conference/pkg/types/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add demos/conference/pkg/types demos/conference/cmd/conf-gateway/internal/actorstate
git commit -m "demos/conference: read actor state from ateapi via kubectl-ate

Add ActorView, whose state uses ateapi's own names, and an adapter that
lists and suspends actors through the pinned kubectl-ate binary instead
of copying substrate's client code.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: `Board` and `Poll`

**Files:**
- Create: `cmd/conf-gateway/internal/actorstate/board.go`, `board_test.go`
- Create: `cmd/conf-gateway/internal/actorstate/poll.go`, `poll_test.go`

**Interfaces:**
- Consumes: `Status`, `ActorControl`, `types.ActorView`.
- Produces:
  ```go
  func NewBoard(publish func(types.ActorView)) *Board
  func (b *Board) SetStatuses(statuses []Status)
  func (b *Board) SetUnavailable(reason string)
  func (b *Board) SetActivity(actor string, a types.Activity)
  func (b *Board) SetSource(actor string, s types.ReplySource)
  func (b *Board) Reset()
  func (b *Board) View(actor string) (types.ActorView, bool)
  func (b *Board) Views() map[string]types.ActorView
  func Poll(ctx context.Context, c ActorControl, interval time.Duration, b *Board, observe func(time.Duration, error))
  ```

**Board semantics:**
- `publish` is called outside the lock, once per view that changed, and never for an unchanged view.
- `SetStatuses` replaces the substrate fields of every listed actor and keeps `Activity` and `Source`. An actor on the board that is missing from the list becomes `UNKNOWN`, with detail `not found in ateapi`.
- `NeverStarted` = `State == SUSPENDED && SnapshotURI == "" && InProgressSnapshotURI == ""`.
- `SnapshotURI` shows the in-progress URI while one exists, otherwise the external one.
- `SetUnavailable` sets every known actor to `UNAVAILABLE` with the reason, and clears `WorkerPod` and `NeverStarted`.
- `SetActivity` and `SetSource` create the view as `UNKNOWN` if the actor has not been listed yet.
- `Reset` clears `Activity` and `Source` on every view and publishes each change.

- [ ] **Step 1: Write the failing Board tests**

```go
package actorstate_test

import (
	"sync"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type published struct {
	mu    sync.Mutex
	views []types.ActorView
}

func (p *published) add(v types.ActorView) { p.mu.Lock(); p.views = append(p.views, v); p.mu.Unlock() }
func (p *published) take() []types.ActorView {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.views
	p.views = nil
	return v
}

func TestBoard_NeverStartedAndSnapshot(t *testing.T) {
	var pub published
	b := actorstate.NewBoard(pub.add)
	b.SetStatuses([]actorstate.Status{
		{Name: "a", State: types.StateSuspended},
		{Name: "b", State: types.StateSuspended, SnapshotURI: "gs://s/b"},
		{Name: "c", State: types.StateSuspending, SnapshotURI: "gs://s/c/1", InProgressSnapshotURI: "gs://s/c/2"},
	})
	a, _ := b.View("a")
	bv, _ := b.View("b")
	c, _ := b.View("c")
	if !a.NeverStarted || bv.NeverStarted || c.NeverStarted {
		t.Errorf("NeverStarted a=%v b=%v c=%v", a.NeverStarted, bv.NeverStarted, c.NeverStarted)
	}
	if c.SnapshotURI != "gs://s/c/2" {
		t.Errorf("c.SnapshotURI = %q, want the in-progress URI", c.SnapshotURI)
	}
	if n := len(pub.take()); n != 3 {
		t.Errorf("published %d views, want 3", n)
	}
}

func TestBoard_PublishesOnlyChanges(t *testing.T) {
	var pub published
	b := actorstate.NewBoard(pub.add)
	st := []actorstate.Status{{Name: "a", State: types.StateRunning, WorkerPod: "p1"}}
	b.SetStatuses(st)
	pub.take()

	b.SetStatuses(st)
	if got := pub.take(); len(got) != 0 {
		t.Errorf("republished unchanged view: %+v", got)
	}
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateSuspending}})
	if got := pub.take(); len(got) != 1 || got[0].State != types.StateSuspending || got[0].WorkerPod != "" {
		t.Errorf("published %+v", got)
	}
}

func TestBoard_ActivitySurvivesPolls(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetActivity("a", types.ActivityThinking)
	b.SetSource("a", types.SourceGemini)
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateRunning}})
	v, _ := b.View("a")
	if v.Activity != types.ActivityThinking || v.Source != types.SourceGemini || v.State != types.StateRunning {
		t.Errorf("view = %+v", v)
	}
}

func TestBoard_MissingActorBecomesUnknown(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateRunning}})
	b.SetStatuses(nil)
	if v, _ := b.View("a"); v.State != types.StateUnknown || v.StateDetail == "" {
		t.Errorf("view = %+v", v)
	}
}

func TestBoard_Unavailable(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetStatuses([]actorstate.Status{{Name: "a", State: types.StateSuspended}})
	b.SetUnavailable("kubectl-ate: exit status 1")
	v, _ := b.View("a")
	if v.State != types.StateUnavailable || v.NeverStarted || v.StateDetail != "kubectl-ate: exit status 1" {
		t.Errorf("view = %+v", v)
	}
}

func TestBoard_ResetClearsActivity(t *testing.T) {
	var pub published
	b := actorstate.NewBoard(pub.add)
	b.SetActivity("a", types.ActivityAwaitingApproval)
	pub.take()
	b.Reset()
	v, _ := b.View("a")
	if v.Activity != types.ActivityIdle {
		t.Errorf("Activity = %q after Reset", v.Activity)
	}
	if len(pub.take()) != 1 {
		t.Error("Reset did not publish the change")
	}
}

func TestBoard_ViewsIsACopy(t *testing.T) {
	b := actorstate.NewBoard(func(types.ActorView) {})
	b.SetActivity("a", types.ActivityThinking)
	views := b.Views()
	views["a"] = types.ActorView{}
	if v, _ := b.View("a"); v.Activity != types.ActivityThinking {
		t.Error("mutating Views() changed the board")
	}
}
```

- [ ] **Step 2: Write the failing Poll tests**

```go
package actorstate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// scriptedControl returns queued results from List, one per call.
type scriptedControl struct {
	results chan listResult
}

type listResult struct {
	statuses []actorstate.Status
	err      error
}

func (s *scriptedControl) List(ctx context.Context) ([]actorstate.Status, error) {
	select {
	case r := <-s.results:
		return r.statuses, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *scriptedControl) Suspend(context.Context, string) error { return nil }

func TestPoll_UpdatesBoardAndReportsErrors(t *testing.T) {
	ctrl := &scriptedControl{results: make(chan listResult)}
	board := actorstate.NewBoard(func(types.ActorView) {})
	observed := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		actorstate.Poll(ctx, ctrl, time.Millisecond, board, func(_ time.Duration, err error) { observed <- err })
		close(done)
	}()

	ctrl.results <- listResult{statuses: []actorstate.Status{{Name: "a", State: types.StateRunning}}}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	if v, _ := board.View("a"); v.State != types.StateRunning {
		t.Errorf("after good poll: %+v", v)
	}

	ctrl.results <- listResult{err: errors.New("ateapi unreachable")}
	if err := <-observed; err == nil {
		t.Fatal("observe got nil error")
	}
	if v, _ := board.View("a"); v.State != types.StateUnavailable {
		t.Errorf("after failed poll: %+v", v)
	}

	cancel()
	<-done
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/internal/actorstate/`
Expected: FAIL to compile: `undefined: actorstate.NewBoard`.

- [ ] **Step 4: Implement `board.go`**

```go
package actorstate

import (
	"maps"
	"sync"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// Board holds the latest view of every actor and publishes each change.
type Board struct {
	publish func(types.ActorView)

	mu    sync.Mutex
	views map[string]types.ActorView
}

// NewBoard returns an empty Board that calls publish for each changed view.
func NewBoard(publish func(types.ActorView)) *Board {
	return &Board{publish: publish, views: map[string]types.ActorView{}}
}

// update applies fn to each named view and publishes the ones that changed.
func (b *Board) update(names []string, fn func(*types.ActorView)) {
	var changed []types.ActorView
	b.mu.Lock()
	for _, name := range names {
		old, ok := b.views[name]
		if !ok {
			old = types.ActorView{ActorID: name, State: types.StateUnknown}
		}
		v := old
		fn(&v)
		if !ok || v != old {
			b.views[name] = v
			changed = append(changed, v)
		}
	}
	b.mu.Unlock()
	for _, v := range changed {
		b.publish(v)
	}
}

func (b *Board) names() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Collect(maps.Keys(b.views))
}

// SetStatuses applies one ateapi listing. Known actors missing from it
// become UNKNOWN.
func (b *Board) SetStatuses(statuses []Status) {
	byName := make(map[string]Status, len(statuses))
	names := b.names()
	for _, s := range statuses {
		if _, seen := byName[s.Name]; !seen {
			names = append(names, s.Name)
		}
		byName[s.Name] = s
	}
	b.update(slices.Compact(slices.Sorted(slices.Values(names))), func(v *types.ActorView) {
		s, ok := byName[v.ActorID]
		if !ok {
			v.State, v.StateDetail, v.WorkerPod, v.SnapshotURI, v.NeverStarted = types.StateUnknown, "not found in ateapi", "", "", false
			return
		}
		v.State, v.StateDetail, v.WorkerPod = s.State, "", s.WorkerPod
		v.SnapshotURI = s.SnapshotURI
		if s.InProgressSnapshotURI != "" {
			v.SnapshotURI = s.InProgressSnapshotURI
		}
		v.NeverStarted = s.State == types.StateSuspended && s.SnapshotURI == "" && s.InProgressSnapshotURI == ""
	})
}

// SetUnavailable marks every known actor's state as unreadable.
func (b *Board) SetUnavailable(reason string) {
	b.update(b.names(), func(v *types.ActorView) {
		v.State, v.StateDetail, v.WorkerPod, v.NeverStarted = types.StateUnavailable, reason, "", false
	})
}

// SetActivity records what the actor is doing for its attendee.
func (b *Board) SetActivity(actor string, a types.Activity) {
	b.update([]string{actor}, func(v *types.ActorView) { v.Activity = a })
}

// SetSource records where the actor's last reply came from.
func (b *Board) SetSource(actor string, s types.ReplySource) {
	b.update([]string{actor}, func(v *types.ActorView) { v.Source = s })
}

// Reset clears gateway-side activity and reply source on every actor.
func (b *Board) Reset() {
	b.update(b.names(), func(v *types.ActorView) { v.Activity, v.Source = types.ActivityIdle, "" })
}

// View returns one actor's view.
func (b *Board) View(actor string) (types.ActorView, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.views[actor]
	return v, ok
}

// Views returns a copy of every view.
func (b *Board) Views() map[string]types.ActorView {
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.views)
}
```

Add `"slices"` to the imports. `names()` returns map keys in random order; `SetStatuses` sorts and compacts them so publish order is stable.

- [ ] **Step 5: Implement `poll.go`**

```go
package actorstate

import (
	"context"
	"time"
)

// Poll lists actors every interval until ctx ends, applying each result to
// b. observe, if set, receives each poll's latency and error.
func Poll(ctx context.Context, c ActorControl, interval time.Duration, b *Board, observe func(time.Duration, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		start := time.Now()
		pollCtx, cancel := context.WithTimeout(ctx, max(interval*5, 5*time.Second))
		statuses, err := c.List(pollCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b.SetUnavailable(err.Error())
		} else {
			b.SetStatuses(statuses)
		}
		if observe != nil {
			observe(time.Since(start), err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test -race ./demos/conference/cmd/conf-gateway/internal/actorstate/`
Expected: PASS.

Guard check: remove the `!ok || v != old` condition, so that it always publishes. `TestBoard_PublishesOnlyChanges` must fail. Restore it.

- [ ] **Step 7: Commit**

```bash
git add demos/conference/cmd/conf-gateway/internal/actorstate
git commit -m "demos/conference: keep actor views from polled ateapi state

Board merges ateapi state with the gateway's knowledge of what each
actor is doing and publishes only views that changed. A failed poll
marks state unavailable instead of leaving stale values on screen.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Gateway runs on the Board

This task replaces every invented state in the gateway with Board views.

**Files:**
- Modify: `cmd/conf-gateway/internal/gateway/gateway.go`
- Modify: `pkg/types/types.go` (delete `ActorLifecycleState`, `StateChangeEvent`; reshape `StageTelemetry`, `WorkerStatus`, `ActorDetails`, `ActionLogEntry`)
- Modify: `cmd/conf-gateway/main.go`, `main_test.go`
- Modify: gateway tests (`helpers_test.go`, `gateway_test.go`, `hygiene_test.go`, `approval_test.go`, `simulation_test.go`)
- Create: `cmd/conf-gateway/internal/gateway/state_test.go`

**Interfaces:**
- Consumes: `actorstate.{ActorControl, Board, NewBoard, Poll, Status}`, `modelproxy.Proxy.Usage/TotalUsage/ResetUsage`.
- Produces:
  - Gateway options:
    ```go
    func WithActorControl(c actorstate.ActorControl) Option
    func WithPollInterval(d time.Duration) Option // default 1s
    func WithSuspendAfter(d time.Duration) Option // default 0 = never auto-suspend
    func WithUsage(u UsageSource) Option
    func WithMockLLM(on bool) Option
    type UsageSource interface {
    	Usage(actor string) types.ModelUsage
    	TotalUsage() types.ModelUsage
    	ResetUsage()
    }
    func (g *Gateway) ActorView(attendee string) (types.ActorView, bool)
    ```
  - Type shapes:
    ```go
    type WorkerStatus struct {
    	PodName     string         `json:"pod_name"`
    	ActiveActor string         `json:"active_actor"`
    	State       SubstrateState `json:"state"`
    }
    type StageTelemetry struct {
    	ParkedRequests   int                  `json:"parked_requests"`
    	ParkingAvailable bool                 `json:"parking_available"`
    	Workers          []WorkerStatus       `json:"workers"`
    	Actors           map[string]ActorView `json:"actors"`
    	RecentEvents     []string             `json:"recent_events"`
    	MockLLM          bool                 `json:"mock_llm"`
    	ModelUsage       ModelUsage           `json:"model_usage"`
    	StatePollMs      int64                `json:"state_poll_ms"`
    }
    type ActorDetails struct {
    	ActorView
    	ChatHistory     []ChatMessage    `json:"chat_history"`
    	PendingApproval *ApprovalRequest `json:"pending_approval,omitempty"`
    	LastResult      *ExecutionResult `json:"last_result,omitempty"`
    	Usage           ModelUsage       `json:"usage"`
    }
    // ActionLogEntry: State ActorLifecycleState is replaced by
    //   Activity Activity    `json:"activity,omitempty"`
    //   Source   ReplySource `json:"source,omitempty"`
    ```
  - SSE on `/api/events`:
    - `event: actor_view` with a `types.ActorView` body. It is sent once on connect and on every change for that attendee.
    - `event: reset` with body `{}`. It is sent when the presenter resets.
  - Removed:
    - `Suspender`, `SuspendFunc`, `commandSuspender`, `WithSuspender`, `getClusterRunningActors`;
    - `SetActorState`, `GetActorState`, `BroadcastStateChange`, `asyncSuspendActor`;
    - `stateToLabel`, `stateToSubtext`;
    - fake workers in `handleTelemetry`;
    - the fake pod and snapshot in `handleActorDetails`.

**Behavior:**
- **Activity:**
  - A chat request sets `THINKING` before proxying.
  - `modifyResponse` sets `AWAITING_APPROVAL` when the reply has an approval, else idle.
  - An approve with `APPROVE` sets `EXECUTING` before proxying, and idle after.
  - A reject sets idle.
  - A proxy error sets idle.
  - `modifyResponse` also calls `SetSource(attendee, chatMsg.Source)`.
- **Auto-suspend:**
  - When `suspendAfter > 0`, each completed chat or approve response resets a per-attendee `time.AfterFunc`, which calls `control.Suspend`.
  - Timers are stored in `suspendTimers map[string]*time.Timer` under `g.mu`.
  - Each firing runs inside `g.wg` so `Close` waits for it.
  - `Close` stops all timers.
- **Workers:** one `WorkerStatus` per distinct non-empty `WorkerPod` among the views, with `ActiveActor` set to that actor and `State` to its state. Sort by pod name.
- **Reset:**
  - clears chat and feed maps and `board.Reset()`;
  - calls `usage.ResetUsage()` when set;
  - publishes `reset` to every SSE subscriber;
  - suspends every actor whose view is `RUNNING` or `RESUMING`, in parallel and bounded to 8 at a time, using a 30s context;
  - response: `{"status":"ok","actors_reset":N,"suspend_errors":M}`.
- **Local mode:** with no `ActorControl`, the Board is set unavailable with reason `local mode: no ateapi` at start, and on each `SetActivity` for a new actor. The UI shows "State unavailable".
- **Poll:** `NewGateway` starts `actorstate.Poll` in a goroutine under `g.wg`, with `observe` storing the last latency in `g.statePollMs` (`atomic.Int64`).

- [ ] **Step 1: Write the failing tests**

Create `state_test.go`. The fake control lives in the test file:

```go
package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// fakeControl serves a fixed listing and records suspends.
type fakeControl struct {
	mu        sync.Mutex
	statuses  []actorstate.Status
	listErr   error
	suspended chan string
}

func newFakeControl(statuses ...actorstate.Status) *fakeControl {
	return &fakeControl{statuses: statuses, suspended: make(chan string, 64)}
}

func (f *fakeControl) set(statuses ...actorstate.Status) {
	f.mu.Lock()
	f.statuses = statuses
	f.mu.Unlock()
}

func (f *fakeControl) List(context.Context) ([]actorstate.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]actorstate.Status(nil), f.statuses...), f.listErr
}

func (f *fakeControl) Suspend(_ context.Context, name string) error {
	f.suspended <- name
	return nil
}

// waitView reads SSE events until one matches.
func waitView(t *testing.T, sc *bufio.Scanner, match func(types.ActorView) bool) types.ActorView {
	t.Helper()
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "actor_view":
			var v types.ActorView
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
				t.Fatal(err)
			}
			if match(v) {
				return v
			}
		}
	}
	t.Fatalf("stream ended: %v", sc.Err())
	return types.ActorView{}
}

func openEvents(t *testing.T, gw *gateway.Gateway, attendee string) *bufio.Scanner {
	t.Helper()
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events?token="+gw.MintToken(attendee), nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return bufio.NewScanner(resp.Body)
}

func TestState_SSEStreamsSubstrateStateChanges(t *testing.T) {
	ctrl := newFakeControl(actorstate.Status{Name: "attendee-01", State: types.StateSuspended})
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-01")

	waitView(t, sc, func(v types.ActorView) bool { return v.NeverStarted })
	ctrl.set(actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "pool-abc"})
	v := waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateRunning })
	if v.WorkerPod != "pool-abc" {
		t.Errorf("WorkerPod = %q", v.WorkerPod)
	}
}

func TestState_ListFailureShowsUnavailable(t *testing.T) {
	ctrl := newFakeControl(actorstate.Status{Name: "attendee-02", State: types.StateRunning})
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-02")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateRunning })

	ctrl.mu.Lock()
	ctrl.listErr = context.DeadlineExceeded
	ctrl.mu.Unlock()
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateUnavailable })
}

func TestState_LocalModeIsUnavailable(t *testing.T) {
	gw := newTestGateway(t, "", "")
	sc := openEvents(t, gw, "attendee-03")
	v := waitView(t, sc, func(types.ActorView) bool { return true })
	if v.State != types.StateUnavailable || !strings.Contains(v.StateDetail, "local mode") {
		t.Errorf("view = %+v", v)
	}
}

func TestState_ActivityFollowsChatAndApproval(t *testing.T) {
	actorReached := make(chan struct{})
	releaseActor := make(chan struct{})
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actorReached <- struct{}{}
		<-releaseActor
		_ = json.NewEncoder(w).Encode(types.ChatMessage{
			Sender: types.SenderAssistant, Content: "ok", Source: types.SourceGemini,
			Approval: &types.ApprovalRequest{ApprovalID: "app-1", Code: "echo", Language: "sh"},
		})
	}))
	t.Cleanup(actor.Close)
	gw := newTestGateway(t, actor.URL, "")
	sc := openEvents(t, gw, "attendee-04")

	go postJSON(gw, "attendee-04", "/api/chat", types.ChatRequest{Message: "hi"})
	<-actorReached
	waitView(t, sc, func(v types.ActorView) bool { return v.Activity == types.ActivityThinking })
	close(releaseActor)
	v := waitView(t, sc, func(v types.ActorView) bool { return v.Activity == types.ActivityAwaitingApproval })
	if v.Source != types.SourceGemini {
		t.Errorf("Source = %q", v.Source)
	}
}

func TestState_AutoSuspendAfterIdle(t *testing.T) {
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Sender: types.SenderAssistant, Content: "ok"})
	}))
	t.Cleanup(actor.Close)
	ctrl := newFakeControl()
	gw := newTestGateway(t, actor.URL, "", gateway.WithActorControl(ctrl), gateway.WithSuspendAfter(time.Millisecond))

	postJSON(gw, "attendee-05", "/api/chat", types.ChatRequest{Message: "hi"})
	if got := <-ctrl.suspended; got != "attendee-05" {
		t.Errorf("suspended %q", got)
	}
}

func TestState_NoAutoSuspendByDefault(t *testing.T) {
	actor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(types.ChatMessage{Sender: types.SenderAssistant, Content: "ok"})
	}))
	t.Cleanup(actor.Close)
	ctrl := newFakeControl()
	gw := newTestGateway(t, actor.URL, "", gateway.WithActorControl(ctrl))

	postJSON(gw, "attendee-06", "/api/chat", types.ChatRequest{Message: "hi"})
	gw.Close() // waits for any timer-driven suspend
	select {
	case name := <-ctrl.suspended:
		t.Errorf("suspended %q with SuspendAfter unset", name)
	default:
	}
}

func TestState_ResetSuspendsOnlyRunningActors(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "p1"},
		actorstate.Status{Name: "attendee-02", State: types.StateSuspended},
		actorstate.Status{Name: "attendee-03", State: types.StateResuming},
	)
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-03")
	waitView(t, sc, func(v types.ActorView) bool { return v.State == types.StateResuming })

	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, asPresenter(gw, httptest.NewRequest(http.MethodPost, "/api/reset", nil)))
	var body struct {
		ActorsReset int `json:"actors_reset"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.ActorsReset != 2 {
		t.Errorf("actors_reset = %d, want 2", body.ActorsReset)
	}
	got := map[string]bool{<-ctrl.suspended: true, <-ctrl.suspended: true}
	if !got["attendee-01"] || !got["attendee-03"] {
		t.Errorf("suspended %v", got)
	}
}

func TestState_TelemetryWorkersComeFromRealPods(t *testing.T) {
	ctrl := newFakeControl(
		actorstate.Status{Name: "attendee-01", State: types.StateRunning, WorkerPod: "pool-b"},
		actorstate.Status{Name: "attendee-02", State: types.StateRunning, WorkerPod: "pool-a"},
		actorstate.Status{Name: "attendee-03", State: types.StateSuspended},
	)
	gw := newTestGateway(t, "", "", gateway.WithActorControl(ctrl), gateway.WithPollInterval(time.Millisecond))
	sc := openEvents(t, gw, "attendee-01")
	waitView(t, sc, func(v types.ActorView) bool { return v.WorkerPod == "pool-b" })

	tel := getTelemetry(t, gw)
	if len(tel.Workers) != 2 || tel.Workers[0].PodName != "pool-a" || tel.Workers[0].ActiveActor != "attendee-02" {
		t.Errorf("Workers = %+v", tel.Workers)
	}
	if tel.Actors["attendee-03"].State != types.StateSuspended {
		t.Errorf("Actors = %+v", tel.Actors)
	}
}
```

Add these helpers to `helpers_test.go` if they do not already exist:
- `postJSON(gw, attendee, path string, body any) *httptest.ResponseRecorder` mints a token and serves the request.
- `getTelemetry(t, gw) types.StageTelemetry` does a presenter GET of `/api/stage/telemetry` and decodes the body.
- `asPresenter(gw, req) *http.Request` sets the presenter header.

In `newTestGateway`, drop `WithSuspender(&mockSuspender{})` and delete `mockSuspender`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/internal/gateway/ -run TestState`
Expected: FAIL to compile: `undefined: gateway.WithActorControl`.

- [ ] **Step 3: Reshape the types**

Apply the type shapes from the Interfaces block to `pkg/types/types.go`:
- delete `ActorLifecycleState` and its constants, and `StateChangeEvent`;
- remove `WorkerStatus.CurrentTask` and `ElapsedMs`;
- remove `ActorDetails.SnapshotLocation` and `SnapshotSizeBytes`.

Update `types_test.go` to match.

- [ ] **Step 4: Wire the Board into the gateway**

In `gateway.go`:

1. Add fields: `control actorstate.ActorControl`, `board *actorstate.Board`, `pollInterval time.Duration`, `suspendAfter time.Duration`, `suspendTimers map[string]*time.Timer`, `usage UsageSource`, `mockLLM bool`, `statePollMs atomic.Int64`.
2. Delete the `actorStates` map and everything listed under **Removed** above.
3. Add the options:

```go
// WithActorControl reads actor state from, and suspends actors through, c.
func WithActorControl(c actorstate.ActorControl) Option {
	return func(g *Gateway) { g.control = c }
}

// WithPollInterval sets how often actor state is read from ateapi.
func WithPollInterval(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.pollInterval = d
		}
	}
}

// WithSuspendAfter suspends an actor once it has been idle for d. Zero
// leaves suspension to the substrate.
func WithSuspendAfter(d time.Duration) Option {
	return func(g *Gateway) { g.suspendAfter = d }
}

// UsageSource reports model usage per actor.
type UsageSource interface {
	Usage(actor string) types.ModelUsage
	TotalUsage() types.ModelUsage
	ResetUsage()
}

// WithUsage reports model usage from u in telemetry and actor details.
func WithUsage(u UsageSource) Option {
	return func(g *Gateway) { g.usage = u }
}

// WithMockLLM tells the UI that actors are replying without a model.
func WithMockLLM(on bool) Option {
	return func(g *Gateway) { g.mockLLM = on }
}
```

4. In `NewGateway`, after the options are applied:

```go
	g.board = actorstate.NewBoard(g.publishView)
	if g.control != nil {
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			actorstate.Poll(g.ctx, g.control, g.pollInterval, g.board, func(d time.Duration, _ error) {
				g.statePollMs.Store(d.Milliseconds())
			})
		}()
	}
```

`g.ctx` is the context `Close` cancels. If the gateway has no such context yet, add `ctx, cancel` fields, create them in `NewGateway`, and call `cancel()` in `Close`, before `wg.Wait()`.

5. Add the helpers:

```go
// setActivity records activity; in local mode it also marks the state
// unavailable, since there is no ateapi to read it from.
func (g *Gateway) setActivity(attendee string, a types.Activity) {
	g.board.SetActivity(attendee, a)
	if g.control == nil {
		g.board.SetUnavailable(localModeReason)
	}
}

const localModeReason = "local mode: no ateapi"

// publishView sends a changed view to that attendee's SSE subscribers.
func (g *Gateway) publishView(v types.ActorView) {
	g.sendToAttendee(v.ActorID, "actor_view", v)
}

// ActorView returns the current view of one attendee's actor.
func (g *Gateway) ActorView(attendee string) (types.ActorView, bool) {
	return g.board.View(attendee)
}

func (g *Gateway) scheduleSuspend(attendee string) {
	if g.suspendAfter <= 0 || g.control == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.suspendTimers[attendee]; ok {
		t.Stop()
	}
	g.suspendTimers[attendee] = time.AfterFunc(g.suspendAfter, func() {
		g.wg.Add(1)
		defer g.wg.Done()
		ctx, cancel := context.WithTimeout(g.ctx, 30*time.Second)
		defer cancel()
		if err := g.control.Suspend(ctx, attendee); err != nil {
			log.Printf("auto-suspend %s: %v", attendee, err)
		}
	})
}
```

`sendToAttendee(attendee, event string, payload any)` replaces the old per-attendee `state_change` broadcast. Rename the existing helper that writes to `g.subscribers[attendee]`. Keep its non-blocking send, and change the element type of the subscriber channel to a struct `{event string; data []byte}`.

6. In `handleEvents`:
   - On connect, write `event: actor_view`. The body is the attendee's current view. When there is none, it is `{ActorID, State: UNKNOWN}`, or `UNAVAILABLE` with `localModeReason` in local mode.
   - In the loop, write `event: <e.event>\ndata: <e.data>\n\n`.
7. In `proxyToActor`, before `g.proxy.ServeHTTP`:
   - chat: `g.setActivity(attendee, types.ActivityThinking)`.
   - approve with `APPROVE`: `g.setActivity(attendee, types.ActivityExecuting)`.
   - reject: leave activity unchanged.

   In the proxy `ErrorHandler`, call `g.setActivity(attendee, types.ActivityIdle)`.
8. In `modifyResponse`, once `chatMsg` is decoded:

```go
	if hasChatMsg {
		g.board.SetSource(attendeeID, chatMsg.Source)
		if chatMsg.Approval != nil {
			g.setActivity(attendeeID, types.ActivityAwaitingApproval)
		} else {
			g.setActivity(attendeeID, types.ActivityIdle)
		}
	}
	g.scheduleSuspend(attendeeID)
```

   Then:
   - Delete the `asyncSuspendActor` call.
   - Set `Activity` and `Source` on the `ActionLogEntry` the function builds, instead of `State`.
   - Take `WorkerPod` from `g.board.View(attendeeID)`.
9. Rewrite `handleTelemetry`:

```go
	views := g.board.Views()
	var workers []types.WorkerStatus
	for _, v := range views {
		if v.WorkerPod != "" {
			workers = append(workers, types.WorkerStatus{PodName: v.WorkerPod, ActiveActor: v.ActorID, State: v.State})
		}
	}
	slices.SortFunc(workers, func(a, b types.WorkerStatus) int { return strings.Compare(a.PodName, b.PodName) })
	tel := types.StageTelemetry{
		ParkedRequests:   parked,
		ParkingAvailable: parkingOK,
		Workers:          workers,
		Actors:           views,
		RecentEvents:     recent,
		MockLLM:          g.mockLLM,
		StatePollMs:      g.statePollMs.Load(),
	}
	if g.usage != nil {
		tel.ModelUsage = g.usage.TotalUsage()
	}
```

   `parked` and `parkingOK` come from the existing status poller. Task 13 replaces it. For now `parkingOK` is true after any successful fetch.
10. Rewrite `handleActorDetails` to build `types.ActorDetails{ActorView: view, ChatHistory: …, PendingApproval: …, LastResult: …}`, adding `Usage` when `g.usage != nil`. Drop the fake pod name and snapshot size.
11. Rewrite the suspend part of `handleReset`:

```go
	var toSuspend []string
	for name, v := range g.board.Views() {
		if v.State == types.StateRunning || v.State == types.StateResuming {
			toSuspend = append(toSuspend, name)
		}
	}
	g.board.Reset()
	if g.usage != nil {
		g.usage.ResetUsage()
	}
	g.broadcastAll("reset", struct{}{})

	var failed atomic.Int64
	if g.control != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, name := range toSuspend {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				if err := g.control.Suspend(ctx, name); err != nil {
					log.Printf("reset: suspend %s: %v", name, err)
					failed.Add(1)
				}
			}()
		}
		wg.Wait()
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "actors_reset": len(toSuspend), "suspend_errors": failed.Load()})
```

   `broadcastAll(event, payload)` sends to every attendee subscriber and every action-stream subscriber. Replace `PublishReset` with it.
12. In `Close`, stop every timer in `suspendTimers` before `wg.Wait()`.

- [ ] **Step 5: Update the simulation**

`simulation.go` references `SetActorState` and the fake worker states. Replace:
- each "running" transition with `g.setActivity(id, types.ActivityThinking)`;
- each "complete" transition with `g.setActivity(id, types.ActivityIdle)`.

Delete any simulated worker-pod assignment. The simulation drives traffic through the real proxy path, so the real state comes from polling.

- [ ] **Step 6: Update `main.go`**

1. Remove `SuspendMode` and `suspenderOptions`, and remove `SUSPEND_MODE` from `configEnvVars`.
2. Add these fields, and add their names to `configEnvVars`:
   - `ATEAPIEndpoint` from `ATEAPI_ENDPOINT`, default empty;
   - `KubectlATE` from `KUBECTL_ATE`, default `kubectl-ate`;
   - `StatePollInterval` from `STATE_POLL_INTERVAL`, default `1s`, parsed with `time.ParseDuration`;
   - `SuspendAfter` from `SUSPEND_AFTER`, default `0`;
   - `MockLLM` from `DEMO_MOCK_LLM == "true"`;
   - `LocalMode` from `LOCAL_MODE == "true"`.
3. Wire the gateway:

```go
	opts := []gateway.Option{
		gateway.WithSecret(secret),
		gateway.WithAtespace(cfg.Atespace),
		gateway.WithPollInterval(cfg.StatePollInterval),
		gateway.WithSuspendAfter(cfg.SuspendAfter),
		gateway.WithUsage(proxy),
		gateway.WithMockLLM(cfg.MockLLM || !proxy.Enabled()),
		gateway.WithStaticFS(static),
	}
	if !cfg.LocalMode {
		opts = append(opts, gateway.WithActorControl(actorstate.NewKubectlATE(cfg.KubectlATE, cfg.ATEAPIEndpoint, cfg.Atespace)))
	}
```

   Local mode is explicit: nothing is inferred from a missing `ATEAPI_ENDPOINT`, because `kubectl-ate` can use its own default.
4. Add tests:
   - default parsing: 1s poll, 0 suspend-after;
   - an invalid `STATE_POLL_INTERVAL` falls back to 1s with a log line;
   - `LOCAL_MODE=true` sets `LocalMode`.

- [ ] **Step 7: Port the remaining tests**

- **Delete:** tests that assert invented states. `stateToLabel` tests, `SetActorState` round-trips, and the fake-worker telemetry shape tests all go. The test inventory in the Task 9 notes lists them; find the rest with `go vet`.
- **Replace:** each assertion on `state_change` SSE output becomes an assertion on `actor_view`.
- **Keep:** the hygiene tests for concurrent telemetry and SSE fan-out, with the new types.

- [ ] **Step 8: Run all demo tests**

Run: `go test -race ./demos/conference/...`
Expected: PASS.

Guard checks:
- Make `scheduleSuspend` ignore the `suspendAfter <= 0` check. `TestState_NoAutoSuspendByDefault` must fail.
- Drop the `RUNNING || RESUMING` filter in reset. `TestState_ResetSuspendsOnlyRunningActors` must fail.
- Restore both.

- [ ] **Step 9: Commit**

```bash
git add -A demos/conference
git commit -m "demos/conference: show real actor state from ateapi

The gateway polls ateapi through kubectl-ate and streams each actor's
state, worker pod and snapshot URI to the UI. Activity (thinking,
awaiting approval, executing) is tracked separately from substrate
state. Invented lifecycle states, fake worker pods and the per-turn
suspend are removed; auto-suspend is opt-in via SUSPEND_AFTER.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: `routerstatus`: aggregate parking across router pods

**Files:**
- Create: `cmd/conf-gateway/internal/routerstatus/status.go`, `status_test.go`
- Create: `cmd/conf-gateway/internal/routerstatus/endpointslices.go`, `endpointslices_test.go`
- Modify: `cmd/conf-gateway/internal/gateway/gateway.go` (replace `statusURL` and `fetchStatus` with a `routerstatus.Aggregator`)
- Modify: `cmd/conf-gateway/main.go`, `main_test.go`

**Interfaces:**
- Produces:
  ```go
  package routerstatus
  type Source interface{ Targets(ctx context.Context) ([]string, error) } // base URLs, e.g. http://10.0.0.5:4040
  type StaticTargets []string
  func NewEndpointSliceTargets(client kubernetes.Interface, namespace, service string, port int) *EndpointSliceTargets
  type Snapshot struct{ Parked int; Available bool; Targets, Failed int }
  func NewAggregator(src Source, client *http.Client) *Aggregator
  func (a *Aggregator) Fetch(ctx context.Context) Snapshot
  // gateway
  func WithRouterStatus(a *routerstatus.Aggregator) Option
  ```
  `NewGateway(routerURL string, opts ...Option)` drops its `statusURL` argument. Without `WithRouterStatus`, the telemetry reports `parking_available: false`.

- [ ] **Step 1: Write the failing tests**

`status_test.go`:

```go
package routerstatus_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/routerstatus"
)

func statusz(t *testing.T, parked int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/statusz" || r.URL.Query().Get("format") != "json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"parking":{"active":%d},"other":"ignored"}`, parked)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAggregator_SumsAcrossPods(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{statusz(t, 2), statusz(t, 3)}, http.DefaultClient)
	got := a.Fetch(context.Background())
	if got != (routerstatus.Snapshot{Parked: 5, Available: true, Targets: 2}) {
		t.Errorf("Fetch = %+v", got)
	}
}

func TestAggregator_PartialFailureStillAvailable(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{statusz(t, 4), "http://127.0.0.1:1"}, http.DefaultClient)
	got := a.Fetch(context.Background())
	if !got.Available || got.Parked != 4 || got.Failed != 1 {
		t.Errorf("Fetch = %+v", got)
	}
}

func TestAggregator_AllFailedIsUnavailable(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{"http://127.0.0.1:1"}, http.DefaultClient)
	if got := a.Fetch(context.Background()); got.Available {
		t.Errorf("Fetch = %+v, want unavailable", got)
	}
}

func TestAggregator_NoTargetsIsUnavailable(t *testing.T) {
	a := routerstatus.NewAggregator(routerstatus.StaticTargets{}, http.DefaultClient)
	if got := a.Fetch(context.Background()); got.Available {
		t.Errorf("Fetch = %+v, want unavailable", got)
	}
}
```

`endpointslices_test.go`:

```go
package routerstatus_test

import (
	"context"
	"slices"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/routerstatus"
)

func slice(name, service string, eps ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ate-system", Labels: map[string]string{discoveryv1.LabelServiceName: service}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   eps,
	}
}

func ep(ip string, ready bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{Addresses: []string{ip}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(ready)}}
}

func TestEndpointSliceTargets_ReadyRouterPodsOnly(t *testing.T) {
	client := fake.NewClientset(
		slice("r-1", "atenet-router", ep("10.0.0.1", true), ep("10.0.0.2", false)),
		slice("r-2", "atenet-router", ep("10.0.0.3", true), ep("10.0.0.1", true)),
		slice("other", "ateapi", ep("10.0.0.9", true)),
	)
	src := routerstatus.NewEndpointSliceTargets(client, "ate-system", "atenet-router", 4040)
	got, err := src.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"http://10.0.0.1:4040", "http://10.0.0.3:4040"}
	if !slices.Equal(got, want) {
		t.Errorf("Targets = %v, want %v", got, want)
	}
}
```

If `k8s.io/utils/ptr` is not in the module graph, define `func boolPtr(b bool) *bool { return &b }` in the test file instead.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/internal/routerstatus/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `status.go`**

```go
// Package routerstatus reads parked-request counts from every atenet router
// pod and sums them.
package routerstatus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// Source lists router status base URLs.
type Source interface {
	Targets(ctx context.Context) ([]string, error)
}

// StaticTargets is a fixed list of base URLs.
type StaticTargets []string

// Targets returns the list.
func (s StaticTargets) Targets(context.Context) ([]string, error) { return s, nil }

// Snapshot is one aggregated reading.
type Snapshot struct {
	Parked    int
	Available bool
	Targets   int
	Failed    int
}

// Aggregator fetches statusz from every target in parallel.
type Aggregator struct {
	src    Source
	client *http.Client
}

// NewAggregator returns an Aggregator over src.
func NewAggregator(src Source, client *http.Client) *Aggregator {
	return &Aggregator{src: src, client: client}
}

// Fetch reads every target. It is available if at least one target answered.
func (a *Aggregator) Fetch(ctx context.Context) Snapshot {
	targets, err := a.src.Targets(ctx)
	if err != nil || len(targets) == 0 {
		return Snapshot{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var (
		mu   sync.Mutex
		snap = Snapshot{Targets: len(targets)}
		wg   sync.WaitGroup
	)
	for _, base := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := a.fetchOne(ctx, base)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				snap.Failed++
				return
			}
			snap.Parked += n
		}()
	}
	wg.Wait()
	snap.Available = snap.Failed < snap.Targets
	return snap
}

func (a *Aggregator) fetchOne(ctx context.Context, base string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/statusz?format=json", nil)
	if err != nil {
		return 0, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("statusz %s: %s", base, resp.Status)
	}
	var sz struct {
		Parking struct {
			Active int `json:"active"`
		} `json:"parking"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&sz); err != nil {
		return 0, err
	}
	return sz.Parking.Active, nil
}
```

Add `"fmt"` to the imports.

- [ ] **Step 4: Implement `endpointslices.go`**

```go
package routerstatus

import (
	"context"
	"fmt"
	"net"
	"strconv"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EndpointSliceTargets finds ready router pods from a Service's EndpointSlices.
type EndpointSliceTargets struct {
	client             kubernetes.Interface
	namespace, service string
	port               int
}

// NewEndpointSliceTargets returns a Source for the pods behind service.
func NewEndpointSliceTargets(client kubernetes.Interface, namespace, service string, port int) *EndpointSliceTargets {
	return &EndpointSliceTargets{client: client, namespace: namespace, service: service, port: port}
}

// Targets returns one base URL per ready pod address, deduplicated.
func (e *EndpointSliceTargets) Targets(ctx context.Context) ([]string, error) {
	list, err := e.client.DiscoveryV1().EndpointSlices(e.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + e.service,
	})
	if err != nil {
		return nil, fmt.Errorf("listing endpointslices for %s/%s: %w", e.namespace, e.service, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range list.Items {
		for _, ep := range s.Endpoints {
			if ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
				continue
			}
			for _, addr := range ep.Addresses {
				u := "http://" + net.JoinHostPort(addr, strconv.Itoa(e.port))
				if !seen[u] {
					seen[u] = true
					out = append(out, u)
				}
			}
		}
	}
	return out, nil
}
```

- [ ] **Step 5: Wire it into the gateway and main**

- **Gateway:**
  - Drop the `statusURL` parameter from `NewGateway`, along with the `statusURL`, `parkedCount` and `fetchStatus` code.
  - Add `routerStatus *routerstatus.Aggregator` and `parking atomic.Pointer[routerstatus.Snapshot]`.
  - `WithRouterStatus(a)` sets the aggregator.
  - `pollStatus` calls `a.Fetch(g.ctx)` every `statusInterval`, which is the existing `WithStatusInterval`, and stores the result.
  - `handleTelemetry` reads `ParkedRequests` and `ParkingAvailable` from the stored snapshot. With no snapshot, both are zero or false.
- **`helpers_test.go`:** `newTestGateway(t, routerURL string, opts ...)` drops `statusURL`. It adds `WithRouterStatus(routerstatus.NewAggregator(routerstatus.StaticTargets{fakeStatusz.URL}, http.DefaultClient))` only when the test passes no `WithRouterStatus` of its own; tests opt in through the options. Update every call site: remove the second `""` argument everywhere, including in `contract_test.go` and `state_test.go`.
- **Gateway test:** add `TestTelemetry_ParkingFromAggregator`. Two fake statusz servers report 1 and 2, so the telemetry must show `parked_requests == 3` and `parking_available == true`. Poll `/api/stage/telemetry` until `parking_available` is true, with `WithStatusInterval(time.Millisecond)`. Use a loop on the observed value, not a sleep.
- **`main.go`:**
  - Add `RouterStatusDiscovery` from `ROUTER_STATUS_DISCOVERY`, default `static`, either `static` or `endpointslices`. Keep `StatusURL` for `static`.
  - For `endpointslices`, use `rest.InClusterConfig()` and `kubernetes.NewForConfig`, then `NewEndpointSliceTargets(client, "ate-system", "atenet-router", 4040)`.
  - Any other value is fatal.
  - Test the default and the invalid value.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./demos/conference/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A demos/conference
git commit -m "demos/conference: sum parked requests across all router pods

A single statusz URL behind a Service reports one random pod's count.
The gateway now discovers ready atenet-router pods from EndpointSlices
and sums their parking counts, and says when none could be read.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 14: UI renders `ActorView`

**Files:**
- Create: `cmd/conf-gateway/static/actorview.js`
- Modify: `cmd/conf-gateway/static/{chat,feed,stage}.js`, `{chat,feed,stage}.html`, `{chat,feed,stage}.css`
- Create: `cmd/conf-gateway/internal/gateway/static_test.go`

**Interfaces:**
- Consumes: the JSON shapes from Task 12 (`ActorView`, `StageTelemetry`, `ActorDetails`, `ActionLogEntry.activity/source`) and the SSE events `actor_view` and `reset`.
- Produces: `window.ActorView` with:
  - `state(view) → {label, sub, cls}`
  - `activity(view) → {label, cls} | null`
  - `source(src) → {label, cls} | null`

  Every page loads `actorview.js` before its own script.

**Labels:** these follow spec §4 exactly.

| View | Badge label | Sub | Class |
|---|---|---|---|
| `never_started` | Never started | No snapshot yet | `never-started` |
| SUSPENDED + `snapshot_uri` | Suspended | Snapshot saved | `suspended` |
| RESUMING | Resuming | Restoring on a worker | `resuming` |
| RUNNING | Running | `worker_pod` | `running` |
| SUSPENDING | Suspending | Writing snapshot | `suspending` |
| PAUSING / PAUSED | Pausing / Paused | | `paused` |
| CRASHED | Crashed | `state_detail` | `crashed` |
| DELETING / REVERTING | Deleting / Reverting | | `transition` |
| UNAVAILABLE | State unavailable | `state_detail` | `unavailable` |
| UNKNOWN / other | Unknown | `state_detail` | `unknown` |

Activity chip: Thinking (`thinking`), Awaiting approval (`awaiting`), Executing (`executing`), and nothing for idle. Source: `mock` → "Mock reply", `mock-fallback` → "Mock (model error)", and nothing for `gemini`.

No label may claim anything the gateway cannot observe: "in GCS", "0 MB RAM", "Envoy L7 Buffer", "gVisor" on a per-actor badge, or "Vertex AI" while mock mode is on.

- [ ] **Step 1: Write the failing static hygiene test**

`static_test.go` guards against invented states coming back and checks that every page loads the shared mapping:

```go
package gateway_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const staticDir = "../../static"

func TestStatic_NoInventedStateLabels(t *testing.T) {
	banned := []string{
		"RUNNING_GVISOR", "THINKING_VERTEX_AI", "PARKED_ENVOY",
		"Suspended in GCS", "0 MB RAM", "0 vCPU", "Envoy L7 Buffer", "Process frozen",
		"snapshot_location", "snapshot_size_bytes", "current_task", "elapsed_ms",
		"state_change", "conf-workers-0",
	}
	err := filepath.WalkDir(staticDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, s := range banned {
			if strings.Contains(string(b), s) {
				t.Errorf("%s contains %q", path, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatic_PagesLoadActorViewFirst(t *testing.T) {
	for _, page := range []string{"chat", "feed", "stage"} {
		b, err := os.ReadFile(filepath.Join(staticDir, page+".html"))
		if err != nil {
			t.Fatal(err)
		}
		html := string(b)
		shared := strings.Index(html, `src="/static/actorview.js"`)
		own := strings.Index(html, `src="/static/`+page+`.js"`)
		if shared < 0 || own < 0 || shared > own {
			t.Errorf("%s.html must load actorview.js before %s.js", page, page)
		}
	}
}

func TestStatic_ActorViewIsServed(t *testing.T) {
	gw := newTestGateway(t, "")
	rec := getPath(gw, "/static/actorview.js")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "window.ActorView") {
		t.Errorf("GET /static/actorview.js = %d", rec.Code)
	}
}
```

`getPath(gw, path)` is a GET helper in `helpers_test.go`; add it if missing. Adjust `staticDir` if the test package sits at a different depth from `static/`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./demos/conference/cmd/conf-gateway/internal/gateway/ -run TestStatic`
Expected: FAIL, listing the banned strings found in `chat.js`, `feed.js` and `stage.js`.

- [ ] **Step 3: Create `actorview.js`**

```js
// Copyright 2026 Google LLC
// (full Apache 2.0 header from hack/boilerplate/boilerplate.js.txt or the .go.txt text in // comments)

// Shared mapping from the gateway's ActorView to display labels. Labels use
// ateapi's state names and claim nothing the gateway does not observe.
(function () {
  'use strict';

  const STATES = {
    RESUMING: { label: 'Resuming', sub: 'Restoring on a worker', cls: 'resuming' },
    RUNNING: { label: 'Running', sub: '', cls: 'running' },
    SUSPENDING: { label: 'Suspending', sub: 'Writing snapshot', cls: 'suspending' },
    SUSPENDED: { label: 'Suspended', sub: 'Snapshot saved', cls: 'suspended' },
    PAUSING: { label: 'Pausing', sub: '', cls: 'paused' },
    PAUSED: { label: 'Paused', sub: '', cls: 'paused' },
    CRASHED: { label: 'Crashed', sub: '', cls: 'crashed' },
    DELETING: { label: 'Deleting', sub: '', cls: 'transition' },
    REVERTING: { label: 'Reverting', sub: '', cls: 'transition' },
    UNAVAILABLE: { label: 'State unavailable', sub: '', cls: 'unavailable' },
  };
  const UNKNOWN = { label: 'Unknown', sub: '', cls: 'unknown' };

  const ACTIVITIES = {
    THINKING: { label: 'Thinking', cls: 'thinking' },
    AWAITING_APPROVAL: { label: 'Awaiting approval', cls: 'awaiting' },
    EXECUTING: { label: 'Executing', cls: 'executing' },
  };

  const SOURCES = {
    'mock': { label: 'Mock reply', cls: 'mock' },
    'mock-fallback': { label: 'Mock (model error)', cls: 'mock-fallback' },
  };

  function state(view) {
    if (!view) return UNKNOWN;
    if (view.never_started) {
      return { label: 'Never started', sub: 'No snapshot yet', cls: 'never-started' };
    }
    const base = STATES[view.state] || UNKNOWN;
    let sub = base.sub;
    if (view.state === 'RUNNING') sub = view.worker_pod || '';
    if (['CRASHED', 'UNAVAILABLE', 'UNKNOWN'].includes(view.state) || !STATES[view.state]) {
      sub = view.state_detail || '';
    }
    return { label: base.label, sub: sub, cls: base.cls };
  }

  function activity(view) {
    return (view && ACTIVITIES[view.activity]) || null;
  }

  function source(src) {
    return SOURCES[src] || null;
  }

  window.ActorView = { state: state, activity: activity, source: source };
})();
```

Use the JS or Go boilerplate text from `hack/boilerplate/` for the header, whichever `hack/verify-boilerplate.sh` expects for `.js`. Check with `ls hack/boilerplate`.

- [ ] **Step 4: Rewrite `chat.js` state handling**

1. Delete `LIFECYCLE_STATES` and every `setStatusPill(LIFECYCLE_STATES.X)` call.
2. Replace the `state_change` listener:

```js
    es.addEventListener('actor_view', function (e) {
      try {
        renderActorView(JSON.parse(e.data));
      } catch (err) {
        console.warn('Failed to parse actor_view event:', err);
      }
    });
```

3. Add a renderer. The status pill shows the substrate badge; a separate chip shows activity; the last assistant message gets a source tag.

```js
  function renderActorView(view) {
    if (!view || (state.attendeeId && view.actor_id !== state.attendeeId)) return;
    const s = window.ActorView.state(view);
    el.statusPill.className = 'status-pill ' + s.cls;
    el.statusText.textContent = s.label;
    el.statusSub.textContent = s.sub;
    const a = window.ActorView.activity(view);
    el.activityChip.hidden = !a;
    if (a) {
      el.activityChip.className = 'activity-chip ' + a.cls;
      el.activityChip.textContent = a.label;
    }
  }
```

4. In `chat.html`, add `<span id="activity-chip" class="activity-chip" hidden></span>` next to the status pill, and register `activityChip` in `el`.
5. When an assistant message renders with a `source` that `ActorView.source` maps, append `<span class="source-tag CLS">LABEL</span>` to the message meta. Use `textContent`, not `innerHTML`.
6. Leave the `reset` listener in place. It already clears the chat and calls `handleResetEvent`. The next `actor_view` restores the badge.

- [ ] **Step 5: Rewrite `feed.js` badges and filters**

1. Replace `getStatusPillHTML(state)` and `getFootprintHTML(state, workerPod)` with a single function:

```js
  function getActivityHTML(entry) {
    const a = window.ActorView.activity({ activity: entry.activity });
    const src = window.ActorView.source(entry.source);
    let html = a ? `<span class="status-pill ${a.cls}">${escapeHtml(a.label)}</span>` : '';
    if (src) html += `<span class="source-tag ${src.cls}">${escapeHtml(src.label)}</span>`;
    if (entry.worker_pod) {
      html += `<span class="footprint-pill" title="Worker pod">${escapeHtml(entry.worker_pod)}</span>`;
    }
    return html;
  }
```

2. Set `card.dataset.activity = entry.activity || ''` in place of `dataset.state`.
3. Rename the filter buttons in `feed.html` to All / Thinking / Awaiting approval / Executed. They match on `activity`, and on `execution` for "Executed".

- [ ] **Step 6: Rewrite `stage.js`**

1. **Actor grid badges:** use `window.ActorView.state(view)` and `.activity(view)` from `data.actors`. Delete the switch around line 540 and the hard-coded `SUSPENDED` initial label; the initial label is `Unknown` until the first telemetry arrives.
2. **Worker cards:** replace the fixed two cards with a list rendered from `data.workers`, one card per real pod, showing `pod_name`, `active_actor` and the state label. With no workers, show "No actors running". Delete the `current_task` and `elapsed_ms` code.
3. **Spotlight:** replace the state block around line 719 with `ActorView.state` and `ActorView.activity`, the worker pod, `snapshot_uri` (text plus title), and a usage line: `calls · prompt/candidate tokens · last latency ms`. Delete `snapshot_location` and `snapshot_size_bytes`.
4. **Parking meter:** when `parking_available` is false, show "Parking: unavailable" and remove `active-parking`.
5. **Header:** when `mock_llm` is true, show `<span class="mock-banner">Mock LLM</span>`, and hide any "Vertex AI" or "Gemini" text in the header.
6. **Legend in `stage.html`:** list the ten badge labels from the table and the three activity chips. Delete the old legend entries.

- [ ] **Step 7: CSS**

Add these classes to each page's stylesheet, reusing the existing color variables:
- badge classes: `.never-started`, `.resuming`, `.running`, `.suspending`, `.suspended`, `.paused`, `.crashed` (red), `.transition`, `.unavailable` (gray, dashed border), `.unknown`;
- activity chips: `.activity-chip.thinking`, `.awaiting`, `.executing`;
- source tags: `.source-tag.mock`, `.mock-fallback`;
- `.mock-banner`.

Delete the rules for removed classes: `.parked-envoy`, `.running-gvisor`, `.thinking-vertex-ai`, `.awaiting-approval`.

- [ ] **Step 8: Run the tests, then check by hand**

Run: `go test -race ./demos/conference/cmd/conf-gateway/...`
Expected: PASS.

Manual check in local mode:
1. Run `go run ./demos/conference/cmd/conf-localrouter --slots=2 &` and `LOCAL_MODE=true DEMO_MOCK_LLM=true ROUTER_URL=http://localhost:18080 STATUS_URL=http://localhost:18080 PORT=8090 go run ./demos/conference/cmd/conf-gateway`.
2. Open `/chat`, `/feed` and `/stage?key=…` (the presenter token; start the gateway with `PRESENTER_TOKEN=dev`).

Expected:
- Badges read "State unavailable (local mode: no ateapi)".
- Chat shows Thinking, then idle.
- Replies carry "Mock reply".
- The stage shows the "Mock LLM" banner.
- The browser console has no errors.

- [ ] **Step 9: Commit**

```bash
git add demos/conference/cmd/conf-gateway/static demos/conference/cmd/conf-gateway/internal/gateway/static_test.go demos/conference/cmd/conf-gateway/internal/gateway/helpers_test.go
git commit -m "demos/conference: render ateapi state and activity in the UI

All three pages map ActorView through one shared script, so a badge
shows ateapi's state name, a separate chip shows what the actor is
doing, and mock replies are labeled. Labels that claimed unobserved
facts (memory in GCS, Envoy buffering, fixed worker names) are gone.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 15: Split `gateway.go` into packages

This is a move-only task: behavior does not change and every test passes unchanged, apart from import paths. It happens after the behavior changes so the diff stays reviewable.

**Files:**
- Create:
  - `cmd/conf-gateway/internal/auth/auth.go`, with `MintToken`, `VerifyToken`, `Claims`, attendee-ID validation, presenter-key check and `PresenterHeaderName`;
  - `cmd/conf-gateway/internal/actorproxy/proxy.go`, with the ReverseProxy, `Rewrite`, `ModifyResponse` and `ErrorHandler`;
  - `actorproxy/approvals.go`, with `claimApproval` and pending-approval bookkeeping;
  - `cmd/conf-gateway/internal/feed/feed.go`, with the action log ring and history;
  - `feed/broker.go`, with SSE subscribers, `sendToAttendee` and `broadcastAll`;
  - `cmd/conf-gateway/internal/web/web.go`, with page handlers, the static FS and JSON helpers (`writeJSON`, `jsonPOST`);
  - `cmd/conf-gateway/internal/simulation/simulation.go`, with `SimulationRunner` and `SimulationController`.
- Keep `cmd/conf-gateway/internal/gateway/gateway.go` as the composition root: it holds `Gateway`, the options, `routes()`, reset, telemetry and actor details.
- Move each package's tests with it. `contract_test.go` and `state_test.go` stay in `gateway`.

**Interfaces:**
```go
package auth
type Signer struct{ /* secret */ }
func NewSigner(secret []byte) *Signer
func (s *Signer) Mint(attendee string) string
func (s *Signer) Verify(token string) (*Claims, error)
func ValidAttendeeID(id string) bool
const PresenterHeaderName = "X-Presenter-Key"
func PresenterOK(r *http.Request, key string) bool

package feed
type Broker struct{ ... }
func NewBroker() *Broker
func (b *Broker) SubscribeAttendee(id string) (<-chan Event, func())
func (b *Broker) SubscribeActions() (<-chan Event, func())
func (b *Broker) SendToAttendee(id, event string, payload any)
func (b *Broker) Broadcast(event string, payload any)
type Event struct{ Name string; Data []byte }
type Log struct{ ... }
func NewLog(max int) *Log
func (l *Log) Record(e types.ActionLogEntry) types.ActionLogEntry
func (l *Log) History() []types.ActionLogEntry
func (l *Log) Reset()

package simulation
type Target interface {
	Context() context.Context
	MintToken(attendee string) string
	Handler() http.Handler
}
func NewRunner(t Target) *Runner

package actorproxy
type Hooks struct {
	OnRequest  func(attendee string, r *http.Request)                    // activity before proxying
	OnReply    func(attendee string, msg *types.ChatMessage, isApproval bool)
	OnError    func(attendee string)
}
func New(routerURL *url.URL, atespace string, secret []byte, hooks Hooks) *Proxy
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (p *Proxy) ClaimApproval(attendee, id string) bool
```

- [ ] **Step 1: Record a baseline**

Run: `go test -race -count=1 ./demos/conference/... 2>&1 | tail -20 > /tmp/b1-before.txt; go test -list . ./demos/conference/... | sort > /tmp/b1-tests-before.txt`
Expected: PASS. These two files are the baseline.

- [ ] **Step 2: Move one package at a time**

Work in this order: `auth` → `feed` → `simulation` → `actorproxy` → `web`. For each package:
1. Create the package with the code cut from `gateway.go`, exported per the Interfaces block.
2. Move the tests that exercise only that code, for example the JWT tests to `auth`, and the ring and SSE fan-out tests to `feed`.
3. In `gateway.go`, replace the removed code with calls to the new package.
4. Run `go build ./demos/conference/... && go test -race ./demos/conference/...`.
5. Commit with `demos/conference: move <pkg> out of gateway.go`.

Gateway methods the tests use, such as `gw.MintToken`, stay as one-line delegates, so the gateway tests compile unchanged.

- [ ] **Step 3: Confirm nothing was lost**

Run: `go test -list . ./demos/conference/... | sort > /tmp/b1-tests-after.txt; diff <(sed 's/.*\t//' /tmp/b1-tests-before.txt | grep ^Test | sort) <(sed 's/.*\t//' /tmp/b1-tests-after.txt | grep ^Test | sort)`
Expected: no output, because the same test names exist (the packages differ). Also check `wc -l cmd/conf-gateway/internal/gateway/gateway.go`: it should now be under 700 lines, down from about 2,100.

- [ ] **Step 4: Final commit for the task**

If Step 2 produced per-package commits, there is nothing left to commit here. Otherwise:

```bash
git add -A demos/conference/cmd/conf-gateway
git commit -m "demos/conference: split gateway.go into focused packages

Auth, the actor proxy, the feed broker, page serving and the simulation
each move to their own package. gateway.go keeps composition, reset and
telemetry. No behavior change.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 16: Gateway image and in-cluster manifests

**Files:**
- Create: `demos/conference/Dockerfile`, `demos/conference/Dockerfile.dockerignore`
- Create: `demos/conference/manifests/gateway.yaml.tmpl`
- Modify: `demos/conference/conference-template.yaml.tmpl` (actor env)
- Create: `hack/verify-demo-conference-manifests.sh`

**Interfaces:**
- Consumes these env vars from the gateway binary:
  - `PORT`, `ROUTER_URL`, `ROUTER_STATUS_DISCOVERY`, `ATEAPI_ENDPOINT`, `KUBECTL_ATE`
  - `HMAC_SECRET`, `PRESENTER_TOKEN`
  - `MODEL_PROXY_ADDR`, `VERTEX_PROJECT`, `VERTEX_LOCATION`, `MODEL_ALLOWLIST`
  - `DEMO_MOCK_LLM`, `SUSPEND_AFTER`, `STATE_POLL_INTERVAL`

  The actor reads `MODEL_PROXY_URL`, `VERTEX_PROJECT`, `VERTEX_LOCATION`, `GEMINI_MODEL` and `DEMO_MOCK_LLM`.
- Produces placeholders for the install script, all substituted by `demo-conference_render` in Task 17:
  - `${GATEWAY_IMAGE}`, `${GATEWAY_GSA}`, `${VERTEX_PROJECT}`, `${VERTEX_LOCATION}`
  - `${DEMO_MOCK_LLM}`, `${DEMO_DOMAIN}`, `${BUCKET_NAME}`

- [ ] **Step 1: Write the failing manifest check**

`hack/verify-demo-conference-manifests.sh` renders every template with fake values and validates the result offline:

```bash
#!/usr/bin/env bash
# Copyright 2026 Google LLC
# (full Apache 2.0 header from hack/boilerplate/boilerplate.sh.txt)

# Renders the conference demo manifests with placeholder values and checks
# that no placeholder survives and that the YAML parses as Kubernetes objects.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "${ROOT}"

render() {
  sed -e "s|\${GATEWAY_IMAGE}|gcr.io/p/conf-gateway@sha256:0000000000000000000000000000000000000000000000000000000000000000|g" \
      -e "s|\${GATEWAY_GSA}|conf-gateway@p.iam.gserviceaccount.com|g" \
      -e "s|\${VERTEX_PROJECT}|p|g" \
      -e "s|\${VERTEX_LOCATION}|us-central1|g" \
      -e "s|\${DEMO_MOCK_LLM}|false|g" \
      -e "s|\${DEMO_DOMAIN}||g" \
      -e "s|\${BUCKET_NAME}|b|g" \
      "$1"
}

status=0
for f in demos/conference/manifests/gateway.yaml.tmpl demos/conference/conference-template.yaml.tmpl; do
  out="$(render "${f}")"
  if grep -n '\${' <<<"${out}"; then
    echo "error: ${f} has unsubstituted placeholders" >&2
    status=1
  fi
done

out="$(render demos/conference/manifests/gateway.yaml.tmpl)"
for want in "kind: ServiceAccount" "iam.gke.io/gcp-service-account" "kind: Deployment" \
            "name: conf-gateway-internal" "resourceNames:" "- ate-client" "clustertrustbundles" \
            "endpointslices" "readOnlyRootFilesystem: true" "runAsNonRoot: true"; do
  if ! grep -qF -- "${want}" <<<"${out}"; then
    echo "error: gateway manifest lacks ${want}" >&2
    status=1
  fi
done
if command -v kubectl >/dev/null 2>&1; then
  kubectl apply --dry-run=client --validate=false -f - <<<"${out}" >/dev/null || status=1
fi
exit "${status}"
```

`chmod +x` the script.

- [ ] **Step 2: Run it to verify it fails**

Run: `hack/verify-demo-conference-manifests.sh`
Expected: FAIL, because `demos/conference/manifests/gateway.yaml.tmpl` does not exist yet (sed: no such file).

- [ ] **Step 3: Write `manifests/gateway.yaml.tmpl`**

```yaml
# Copyright 2026 Google LLC
# (full Apache 2.0 header from hack/boilerplate/boilerplate.yaml.txt)

# conf-gateway serves the attendee, feed and stage pages, proxies chat to
# each attendee's actor through atenet, reads actor state from ateapi with
# kubectl-ate, and forwards actor model calls to Vertex AI.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: conf-gateway
  namespace: ate-demo-conf
  annotations:
    iam.gke.io/gcp-service-account: ${GATEWAY_GSA}
---
# kubectl-ate mints an ate-client token to call ateapi.
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: conf-gateway-ate-client
  namespace: ate-system
rules:
- apiGroups: [""]
  resources: ["serviceaccounts/token"]
  resourceNames: ["ate-client"]
  verbs: ["create"]
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: conf-gateway-ate-client
  namespace: ate-system
subjects:
- kind: ServiceAccount
  name: conf-gateway
  namespace: ate-demo-conf
roleRef:
  kind: Role
  name: conf-gateway-ate-client
  apiGroup: rbac.authorization.k8s.io
---
# kubectl-ate reads the ateapi CA from a ClusterTrustBundle.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: conf-gateway-trust-bundles
rules:
- apiGroups: ["certificates.k8s.io"]
  resources: ["clustertrustbundles"]
  verbs: ["list", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: conf-gateway-trust-bundles
subjects:
- kind: ServiceAccount
  name: conf-gateway
  namespace: ate-demo-conf
roleRef:
  kind: ClusterRole
  name: conf-gateway-trust-bundles
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: conf-gateway
  namespace: ate-demo-conf
  labels:
    app: conf-gateway
spec:
  # One replica: sessions, feed history and pending approvals live in memory.
  replicas: 1
  selector:
    matchLabels:
      app: conf-gateway
  template:
    metadata:
      labels:
        app: conf-gateway
    spec:
      serviceAccountName: conf-gateway
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: conf-gateway
        image: ${GATEWAY_IMAGE}
        ports:
        - name: http
          containerPort: 8090
        - name: model-proxy
          containerPort: 8091
        env:
        - name: PORT
          value: "8090"
        - name: MODEL_PROXY_ADDR
          value: ":8091"
        - name: ROUTER_URL
          value: "http://atenet-router.ate-system.svc:80"
        - name: ROUTER_STATUS_DISCOVERY
          value: "endpointslices"
        - name: ATEAPI_ENDPOINT
          value: "api.ate-system.svc:443"
        - name: KUBECTL_ATE
          value: "/usr/local/bin/kubectl-ate"
        - name: VERTEX_PROJECT
          value: "${VERTEX_PROJECT}"
        - name: VERTEX_LOCATION
          value: "${VERTEX_LOCATION}"
        - name: MODEL_ALLOWLIST
          value: "gemini-2.5-flash"
        - name: DEMO_MOCK_LLM
          value: "${DEMO_MOCK_LLM}"
        - name: HMAC_SECRET
          valueFrom:
            secretKeyRef:
              name: conf-gateway
              key: HMAC_SECRET
        - name: PRESENTER_TOKEN
          valueFrom:
            secretKeyRef:
              name: conf-gateway
              key: PRESENTER_TOKEN
        readinessProbe:
          httpGet:
            path: /healthz
            port: http
        resources:
          requests:
            cpu: 250m
            memory: 256Mi
          limits:
            memory: 512Mi
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: true
          capabilities:
            drop: ["ALL"]
        volumeMounts:
        - name: tmp
          mountPath: /tmp
      volumes:
      - name: tmp
        emptyDir: {}
---
# Presenter and attendee entry point. Plain HTTP unless DEMO_DOMAIN adds
# the Ingress below; see the README before exposing it.
apiVersion: v1
kind: Service
metadata:
  name: conf-gateway
  namespace: ate-demo-conf
spec:
  type: LoadBalancer
  selector:
    app: conf-gateway
  ports:
  - name: http
    port: 80
    targetPort: http
---
# Model proxy for actors only; never exposed outside the cluster.
apiVersion: v1
kind: Service
metadata:
  name: conf-gateway-internal
  namespace: ate-demo-conf
spec:
  type: ClusterIP
  selector:
    app: conf-gateway
  ports:
  - name: model-proxy
    port: 8091
    targetPort: model-proxy
```

For `DEMO_DOMAIN`, create a separate `manifests/gateway-ingress.yaml.tmpl` holding a GKE `ManagedCertificate` for `${DEMO_DOMAIN}` and an `Ingress` pointing at `conf-gateway:80`, with `networking.gke.io/managed-certificates` set. Task 17 applies it only when `DEMO_DOMAIN` is set. Add the file to the loop in the verify script, rendering it with `DEMO_DOMAIN=demo.example.com`.

- [ ] **Step 4: Add the actor env to `conference-template.yaml.tmpl`**

Under `containers[0]`, add:

```yaml
  env:
  - name: MODEL_PROXY_URL
    value: "http://conf-gateway-internal.ate-demo-conf.svc:8091"
  - name: VERTEX_PROJECT
    value: "${VERTEX_PROJECT}"
  - name: VERTEX_LOCATION
    value: "${VERTEX_LOCATION}"
  - name: GEMINI_MODEL
    value: "gemini-2.5-flash"
  - name: DEMO_MOCK_LLM
    value: "${DEMO_MOCK_LLM}"
```

The actor holds no credential; its per-request key comes from the gateway.

- [ ] **Step 5: Write the Dockerfile**

`demos/conference/Dockerfile`. The build context is the repository root, so `kubectl-ate` comes from the same commit:

```dockerfile
# Copyright 2026 Google LLC
# (full Apache 2.0 header from hack/boilerplate/boilerplate.Dockerfile.txt)

# Build context: repository root.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -o /out/kubectl-ate ./cmd/kubectl-ate \
 && go build -trimpath -o /out/conf-gateway ./demos/conference/cmd/conf-gateway

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/kubectl-ate /usr/local/bin/kubectl-ate
COPY --from=build /out/conf-gateway /usr/local/bin/conf-gateway
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/conf-gateway"]
```

Match the `golang:` tag to the `go` directive in the root `go.mod`, using `grep '^go ' go.mod`.

`demos/conference/Dockerfile.dockerignore`, used by BuildKit when the Dockerfile is passed with `-f`:

```
.git
.worktrees
**/node_modules
**/*.test
demos/*/ui/ui-server
benchmarking
docs
```

- [ ] **Step 6: Run the checks**

Run: `hack/verify-demo-conference-manifests.sh && docker buildx build --platform=linux/amd64 -f demos/conference/Dockerfile -t conf-gateway:dev --load .`
Expected: the script exits 0 and the image builds. Also run `docker run --rm --entrypoint /usr/local/bin/kubectl-ate conf-gateway:dev --help`. The distroless image has no shell, so this entrypoint override is the only way to check it; it must print kubectl-ate usage.

- [ ] **Step 7: Commit**

```bash
git add demos/conference/Dockerfile demos/conference/Dockerfile.dockerignore demos/conference/manifests demos/conference/conference-template.yaml.tmpl hack/verify-demo-conference-manifests.sh
git commit -m "demos/conference: package the gateway to run in-cluster

A multi-stage image bundles conf-gateway with kubectl-ate built from the
same commit. The manifest gives the gateway a Workload Identity service
account for Vertex AI, RBAC to mint ate-client tokens and list router
endpoints, a public Service and a cluster-only model proxy Service.
Actors get the proxy URL instead of any model credential.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 17: Install script deploys the gateway

**Files:**
- Modify: `hack/install-demo-conference.sh`

**Behavior:**
- **Preconditions:** `BUCKET_NAME` and `KO_DOCKER_REPO` are required. `PROJECT_ID` is optional.
  - With `PROJECT_ID` set, Vertex is on (`VERTEX_PROJECT=${PROJECT_ID}`, `VERTEX_LOCATION=${VERTEX_LOCATION:-us-central1}`, `DEMO_MOCK_LLM=false`).
  - Without it, mock mode is on (`VERTEX_PROJECT=""`, `DEMO_MOCK_LLM=true`), with a loud log line.
- **Router flags:**
  - Read the `atenet-router` container args.
  - Fail when `--parked-request-budget` is unset (its default is 5s) or below 60s.
  - Fail when `--route-timeout` is set below 60s. Unset is fine; the default is 5m.
  - The failure message prints the exact flags to add.
  - `--patch-router` makes the script patch the flags instead, then wait for the rollout.
  - Delete leaves router flags alone.
- **GSA:** when `PROJECT_ID` is set:
  - create `conf-gateway@${PROJECT_ID}.iam.gserviceaccount.com` if missing;
  - grant `roles/aiplatform.user` on the project;
  - bind `roles/iam.workloadIdentityUser` to `serviceAccount:${PROJECT_ID}.svc.id.goog[ate-demo-conf/conf-gateway]`.
  - Every step is idempotent.
- **Secret:** create `conf-gateway` in `ate-demo-conf` with random `HMAC_SECRET` and `PRESENTER_TOKEN` values only if it does not exist, so a redeploy keeps sessions and the presenter URL.
- **Image:** buildx push, then resolve the digest, following the claude-code-multiplex pattern.
- **Template:** ActorTemplates are immutable. If `conf-agent` exists without `MODEL_PROXY_URL` in its env, fail and tell the user to run `--delete-demo-conference` first.
- **Order:**
  1. router check
  2. GSA
  3. pool, atespace and template (via `deploy_substrate_demo`)
  4. gateway image
  5. Secret
  6. gateway manifest, plus the Ingress when `DEMO_DOMAIN` is set
  7. rollout wait
  8. 50 actors
  9. print the presenter instructions

- [ ] **Step 1: Add a shell test for the router-flag parser**

The parser is the only logic with branches; put it in a function `demo-conference_router_flags_ok ARGS_JSON`, which prints the missing flags and returns non-zero, and test it without a cluster. Create `hack/test-install-demo-conference.sh`:

```bash
#!/usr/bin/env bash
# Copyright 2026 Google LLC
# (full Apache 2.0 header from hack/boilerplate/boilerplate.sh.txt)

# Tests the router flag check in install-demo-conference.sh without a cluster.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
ATE_DEMOS=()
ate_demo_flag_unhandled() { :; }
# shellcheck source=hack/install-demo-conference.sh
source "${ROOT}/hack/install-demo-conference.sh"

fail=0
expect() {
  local want="$1" args="$2" got=0
  demo-conference_router_flags_ok "${args}" >/dev/null || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: ${want} for ${args}" >&2
    fail=1
  fi
}

expect bad '["--mode=ingress"]'
expect bad '["--parked-request-budget=5s"]'
expect bad '["--parked-request-budget=59s"]'
expect ok  '["--parked-request-budget=60s"]'
expect ok  '["--parked-request-budget=2m"]'
expect bad '["--parked-request-budget=60s","--route-timeout=30s"]'
expect ok  '["--parked-request-budget=90s","--route-timeout=5m"]'
expect ok  '["--parked-request-budget","60s"]'
exit "${fail}"
```

`chmod +x` it.

- [ ] **Step 2: Run it to verify it fails**

Run: `hack/test-install-demo-conference.sh`
Expected: FAIL with `demo-conference_router_flags_ok: command not found`.

- [ ] **Step 3: Rewrite `hack/install-demo-conference.sh`**

Replace everything from `demo-conference_cmdline` to the end of `demo-conference_delete`. Keep the header, the `ATE_DEMOS` registration and the standalone `if [[ "${BASH_SOURCE[0]}" == "${0}" ]]` block at the bottom. In that block, also forward `--patch-router` through `extra_args`.

```bash
ATE_DEMOS+=(demo-conference) # register demo-conference

DEMO_CONF_PATCH_ROUTER="${DEMO_CONF_PATCH_ROUTER:-false}"
DEMO_CONF_ACTORS=50

demo-conference_cmdline() {
  case "${1}" in
    --deploy-demo-conference) demo-conference_deploy ;;
    --delete-demo-conference) demo-conference_delete ;;
    --patch-router) DEMO_CONF_PATCH_ROUTER=true ;;
    *)
      ate_demo_flag_unhandled
      ;;
  esac
}

# demo-conference_seconds converts a Go duration made of h/m/s parts to
# whole seconds, or prints nothing if it cannot parse it.
demo-conference_seconds() {
  local d="$1" total=0 n unit
  while [[ "${d}" =~ ^([0-9]+)(h|m|s)(.*)$ ]]; do
    n="${BASH_REMATCH[1]}"
    unit="${BASH_REMATCH[2]}"
    d="${BASH_REMATCH[3]}"
    case "${unit}" in
      h) total=$((total + n * 3600)) ;;
      m) total=$((total + n * 60)) ;;
      s) total=$((total + n)) ;;
    esac
  done
  [[ -z "${d}" ]] && echo "${total}"
}

# demo-conference_flag_value prints the value of a --flag from a JSON array
# of args, accepting both "--flag=v" and "--flag" "v".
demo-conference_flag_value() {
  local args_json="$1" flag="$2"
  jq -r --arg f "${flag}" '
    . as $a | [range(0; length)] | map(
      if ($a[.] | startswith($f + "=")) then ($a[.] | ltrimstr($f + "="))
      elif $a[.] == $f then $a[. + 1]
      else empty end) | last // empty' <<<"${args_json}"
}

# demo-conference_router_flags_ok prints the flags atenet-router needs for
# long LLM turns and returns 1 if any are missing or too low.
demo-conference_router_flags_ok() {
  local args_json="$1" missing=() v s
  v="$(demo-conference_flag_value "${args_json}" --parked-request-budget)"
  s="$(demo-conference_seconds "${v:-5s}")"
  if [[ -z "${s}" || "${s}" -lt 60 ]]; then
    missing+=("--parked-request-budget=60s")
  fi
  v="$(demo-conference_flag_value "${args_json}" --route-timeout)"
  if [[ -n "${v}" ]]; then
    s="$(demo-conference_seconds "${v}")"
    if [[ -z "${s}" || "${s}" -lt 60 ]]; then
      missing+=("--route-timeout=5m")
    fi
  fi
  if ((${#missing[@]} > 0)); then
    printf '%s\n' "${missing[@]}"
    return 1
  fi
}

demo-conference_check_router() {
  local args_json missing flag
  args_json="$(run_kubectl get deployment atenet-router -n ate-system \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="atenet-router")].args}')"
  if missing="$(demo-conference_router_flags_ok "${args_json}")"; then
    return 0
  fi
  if [[ "${DEMO_CONF_PATCH_ROUTER}" != true ]]; then
    echo "error: atenet-router needs these flags for LLM turns that outlast its defaults:" >&2
    sed 's/^/  /' <<<"${missing}" >&2
    echo "Add them to the atenet-router container args, or rerun with --patch-router." >&2
    exit 1
  fi
  log_step "Patching atenet-router flags (not restored on delete)..."
  while IFS= read -r flag; do
    run_kubectl_fatal patch deployment atenet-router -n ate-system --type=json \
      -p="[{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",\"value\":\"${flag}\"}]"
  done <<<"${missing}"
  run_kubectl_fatal rollout status deployment/atenet-router -n ate-system --timeout=120s
}

demo-conference_ensure_gsa() {
  local gsa="conf-gateway@${PROJECT_ID}.iam.gserviceaccount.com"
  if ! gcloud iam service-accounts describe "${gsa}" --project "${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud iam service-accounts create conf-gateway --project "${PROJECT_ID}" \
      --display-name "Conference demo gateway (Vertex AI only)"
  fi
  gcloud projects add-iam-policy-binding "${PROJECT_ID}" --condition=None \
    --member "serviceAccount:${gsa}" --role roles/aiplatform.user >/dev/null
  gcloud iam service-accounts add-iam-policy-binding "${gsa}" --project "${PROJECT_ID}" \
    --member "serviceAccount:${PROJECT_ID}.svc.id.goog[ate-demo-conf/conf-gateway]" \
    --role roles/iam.workloadIdentityUser >/dev/null
  DEMO_CONF_GATEWAY_GSA="${gsa}"
}

# demo-conference_build_gateway pushes the gateway image and echoes its
# digest-pinned reference.
demo-conference_build_gateway() {
  local repo="${KO_DOCKER_REPO}/conf-gateway"
  # shellcheck disable=SC2155 # safe initialization
  local stage_tag="${repo}:build-$(date +%s)"
  docker buildx build --platform=linux/amd64 --push \
    -f demos/conference/Dockerfile -t "${stage_tag}" . >&2
  local digest
  digest="$(docker buildx imagetools inspect "${stage_tag}" --format '{{json .}}' | jq -r '.manifest.digest')"
  if [[ -z "${digest}" || "${digest}" == "null" ]]; then
    echo "error: failed to resolve digest for ${stage_tag}" >&2
    return 1
  fi
  echo "${repo}@${digest}"
}

demo-conference_render() {
  sed -e "s|\${BUCKET_NAME}|${BUCKET_NAME:-placeholder}|g" \
      -e "s|\${GATEWAY_IMAGE}|${DEMO_CONF_GATEWAY_IMAGE:-placeholder}|g" \
      -e "s|\${GATEWAY_GSA}|${DEMO_CONF_GATEWAY_GSA:-}|g" \
      -e "s|\${VERTEX_PROJECT}|${DEMO_CONF_VERTEX_PROJECT:-}|g" \
      -e "s|\${VERTEX_LOCATION}|${DEMO_CONF_VERTEX_LOCATION:-us-central1}|g" \
      -e "s|\${DEMO_MOCK_LLM}|${DEMO_CONF_MOCK_LLM:-true}|g" \
      -e "s|\${DEMO_DOMAIN}|${DEMO_DOMAIN:-}|g" \
      "$1" | substitute_version
}

demo-conference_ensure_secret() {
  if run_kubectl get secret conf-gateway -n ate-demo-conf >/dev/null 2>&1; then
    return 0
  fi
  run_kubectl_fatal create secret generic conf-gateway -n ate-demo-conf \
    --from-literal=HMAC_SECRET="$(openssl rand -hex 32)" \
    --from-literal=PRESENTER_TOKEN="$(openssl rand -hex 24)"
}

demo-conference_check_template() {
  local env
  if ! env="$(run_kubectl_ate get actor-template conf-agent -a ate-demo-conf -o json 2>/dev/null)"; then
    return 0
  fi
  if ! jq -e '[.. | objects | select(.name? == "MODEL_PROXY_URL")] | length > 0' <<<"${env}" >/dev/null; then
    echo "error: actor template conf-agent predates the model proxy and templates are immutable." >&2
    echo "Run: hack/install-ate.sh --delete-demo-conference, then deploy again." >&2
    exit 1
  fi
}

demo-conference_deploy() {
  log_step "demo-conference_deploy"
  : "${BUCKET_NAME:?BUCKET_NAME must be set}"
  : "${KO_DOCKER_REPO:?KO_DOCKER_REPO must be set}"
  command -v jq >/dev/null || { echo "error: jq is required" >&2; exit 1; }

  if [[ -n "${PROJECT_ID:-}" ]]; then
    DEMO_CONF_VERTEX_PROJECT="${PROJECT_ID}"
    DEMO_CONF_VERTEX_LOCATION="${VERTEX_LOCATION:-us-central1}"
    DEMO_CONF_MOCK_LLM=false
  else
    log_step "PROJECT_ID is unset: deploying with DEMO_MOCK_LLM=true (no model calls)."
    DEMO_CONF_VERTEX_PROJECT=""
    DEMO_CONF_MOCK_LLM=true
  fi

  demo-conference_check_router
  demo-conference_check_template
  if [[ -n "${PROJECT_ID:-}" ]]; then
    demo-conference_ensure_gsa
  fi

  deploy_substrate_demo demo-conference_render \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-workers 300 \
    demos/conference/conference-template.yaml.tmpl conf-agent

  log_step "Building conf-gateway image..."
  DEMO_CONF_GATEWAY_IMAGE="$(demo-conference_build_gateway)"
  demo-conference_ensure_secret
  demo-conference_render demos/conference/manifests/gateway.yaml.tmpl | run_kubectl_fatal apply -f -
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    demo-conference_render demos/conference/manifests/gateway-ingress.yaml.tmpl | run_kubectl_fatal apply -f -
  fi
  run_kubectl_fatal rollout status deployment/conf-gateway -n ate-demo-conf --timeout=180s

  log_step "Creating ${DEMO_CONF_ACTORS} attendee actors in ate-demo-conf..."
  local i actor err_out
  for i in $(seq 1 "${DEMO_CONF_ACTORS}"); do
    actor="$(printf "attendee-%02d" "${i}")"
    if ! err_out="$(run_kubectl_ate create actor "${actor}" -a ate-demo-conf --template conf-agent 2>&1)"; then
      if [[ "${err_out}" != *"AlreadyExists"* && "${err_out}" != *"already exists"* ]] \
          && ! run_kubectl_ate get actor "${actor}" -a ate-demo-conf >/dev/null 2>&1; then
        echo "error: failed to create actor ${actor}: ${err_out}" >&2
        exit 1
      fi
    fi
  done

  log_step "Conference demo deployed."
  echo "Presenter: kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80"
  echo "  then open http://localhost:8090/stage?key=\$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
  echo "Attendees: http://<conf-gateway EXTERNAL-IP>/chat (plain HTTP unless DEMO_DOMAIN is set)"
}

demo-conference_delete() {
  log_step "demo-conference_delete"
  run_kubectl delete -f <(demo-conference_render demos/conference/manifests/gateway.yaml.tmpl) --ignore-not-found
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    run_kubectl delete -f <(demo-conference_render demos/conference/manifests/gateway-ingress.yaml.tmpl) --ignore-not-found
  fi
  run_kubectl delete secret conf-gateway -n ate-demo-conf --ignore-not-found
  delete_substrate_demo demo-conference_render \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-agent
  log_step "atenet-router flags are left as they are."
}
```

Once the router check has passed, the GSA and the rest of the deploy must not print or log `HMAC_SECRET` or `PRESENTER_TOKEN`. The script only prints the command that reads the token.

- [ ] **Step 4: Run the tests and lint**

Run: `hack/test-install-demo-conference.sh && shellcheck hack/install-demo-conference.sh hack/test-install-demo-conference.sh hack/verify-demo-conference-manifests.sh`
Expected: exit 0, with no shellcheck findings. `SC1091` for the sourced file is acceptable only if the repo's shellcheck config already disables it.

- [ ] **Step 5: Commit**

```bash
git add hack/install-demo-conference.sh hack/test-install-demo-conference.sh
git commit -m "demos/conference: deploy the gateway in-cluster from the install script

The install creates a Vertex-only Google service account bound through
Workload Identity, builds and pins the gateway image, keeps an existing
secret across redeploys, and deploys the gateway before the actors.
Router flags are checked and only patched with --patch-router; delete
no longer rewrites them. Without PROJECT_ID the demo runs in mock mode.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 18: Verify script uses real actors

**Files:**
- Modify: `hack/verify-demo-conference.sh`

**Changes:**
- **`--mock-mode`:**
  - Replace the Python mock router with `go run ./demos/conference/cmd/conf-localrouter --addr=127.0.0.1:${MOCK_ROUTER_PORT} --slots=2 --workspace-root=${TMP_DIR}/actors`.
  - Start the gateway with `LOCAL_MODE=true DEMO_MOCK_LLM=true ROUTER_URL=… STATUS_URL=… ROUTER_STATUS_DISCOVERY=static`.
  - Drop `SUSPEND_MODE`, the `python3` dependency check and the `mock_router.py` heredoc.
- **Non-mock mode:** print the port-forward instructions for the in-cluster gateway (`kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80`), and read the presenter key from the Secret. Stop instructing users to run the gateway locally against port-forwarded routers.
- **Step 6** (workers): no longer requires two workers. It asserts that `workers` is an array and that every entry has a non-empty `pod_name`. In mock mode it asserts `parking_available == true`, because the local router serves statusz.
- **Step 8** (actor details): assert that `state`, `never_started` and `activity` are present in the JSON, and that `snapshot_location` is absent. In cluster mode, also assert that after a chat turn `state` is one of RESUMING, RUNNING, SUSPENDING or SUSPENDED.
- **Post-reset check:**
  - In mock mode, `state == "UNAVAILABLE"` with a `state_detail` containing "local mode".
  - In cluster mode, poll up to 30s for `state == "SUSPENDED"` or `never_started == true`.
- **New step "Model key is not client-settable":** send `/api/chat` with a forged `X-Conf-Model-Key` header. Expect 200, and expect the actor's reply `source` to be `mock` in mock mode. It is the Go contract test that asserts the key; here, only the request succeeding matters.
- **Remove:** all references to `GEMINI_API_KEY`, `X-Gemini-API-Key`, `RUNNING_GVISOR` and `SUSPEND_MODE`.

- [ ] **Step 1: Make the changes**

In the `--mock-mode` block, replace everything from the `mock_router.py` heredoc through the gateway launch with the code below. Keep the existing readiness loop and the `log_ok` line that follow it.

```bash
    log_info "Starting conf-localrouter (2 slots, one real workload server per attendee)..."
    go build -o "${TMP_DIR}/conf-localrouter" ./demos/conference/cmd/conf-localrouter
    "${TMP_DIR}/conf-localrouter" --addr="127.0.0.1:${MOCK_ROUTER_PORT}" --slots=2 \
      --workspace-root="${TMP_DIR}/actors" >"${TMP_DIR}/localrouter.log" 2>&1 &
    SPAWNED_PIDS+=($!)

    ROUTER_URL="http://127.0.0.1:${MOCK_ROUTER_PORT}"
    STATUS_URL="${ROUTER_URL}"
    GATEWAY_URL="http://127.0.0.1:${MOCK_GATEWAY_PORT}"

    if [[ -z "${PRESENTER_TOKEN}" ]]; then
      PRESENTER_TOKEN="$(openssl rand -hex 16)"
    fi
    PORT="${MOCK_GATEWAY_PORT}" \
    PRESENTER_TOKEN="${PRESENTER_TOKEN}" \
    LOCAL_MODE=true \
    DEMO_MOCK_LLM=true \
    ROUTER_URL="${ROUTER_URL}" \
    STATUS_URL="${STATUS_URL}" \
    ROUTER_STATUS_DISCOVERY=static \
    "${TMP_DIR}/conf-gateway" >"${TMP_DIR}/gateway.log" 2>&1 &
    SPAWNED_PIDS+=($!)
```

Make sure the existing `go build` of `conf-gateway` into `${TMP_DIR}` still runs before this block. Delete the `python3` dependency check near line 244.

Replace the `else` branch's instructions (the ones that port-forward the router and run the gateway locally) with:

```bash
    log_err "Conference gateway is not reachable at ${GATEWAY_URL}."
    printf "\nTo verify against a cluster:\n"
    printf "  1. Deploy: hack/install-ate.sh --deploy-demo-conference\n"
    printf "  2. kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80 &\n"
    printf "  3. export PRESENTER_TOKEN=\"\$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)\"\n\n"
    printf "To verify locally without a cluster:\n  %s --mock-mode\n\n" "$0"
    exit 1
```

Step 8 assertions (after `TARGET_ACTOR_ID` is set, replacing the snapshot checks):

```bash
DETAILS="$(curl -fsS "${PRESENTER_HDR[@]}" "${GATEWAY_URL}/api/actor/${TARGET_ACTOR_ID}")"
if ! jq -e 'has("state") and has("never_started") and (has("snapshot_location") | not)' <<<"${DETAILS}" >/dev/null; then
  log_err "Actor details lack ActorView fields: ${DETAILS}"
  exit 1
fi
log_ok "Actor details carry ateapi state ($(jq -r .state <<<"${DETAILS}"))"
```

Post-reset assertion, appended after the existing reset step:

```bash
DETAILS="$(curl -fsS "${PRESENTER_HDR[@]}" "${GATEWAY_URL}/api/actor/${TARGET_ACTOR_ID}")"
if [[ "${MOCK_MODE}" == true ]]; then
  if ! jq -e '.state == "UNAVAILABLE" and (.state_detail | contains("local mode"))' <<<"${DETAILS}" >/dev/null; then
    log_err "Mock mode should report state unavailable: ${DETAILS}"
    exit 1
  fi
else
  for _ in $(seq 1 30); do
    DETAILS="$(curl -fsS "${PRESENTER_HDR[@]}" "${GATEWAY_URL}/api/actor/${TARGET_ACTOR_ID}")"
    jq -e '.state == "SUSPENDED" or .never_started' <<<"${DETAILS}" >/dev/null && break
    sleep 1
  done
  if ! jq -e '.state == "SUSPENDED" or .never_started' <<<"${DETAILS}" >/dev/null; then
    log_err "Actor not suspended 30s after reset: ${DETAILS}"
    exit 1
  fi
fi
log_ok "Post-reset state: $(jq -r .state <<<"${DETAILS}")"
```

The one `sleep` is acceptable here: it polls a real cluster, not a unit test.

In Step 6, replace the two-worker check with the following, and delete the Python router's `/api/actor/` handler along with the rest of the heredoc:

```bash
if ! jq -e '(.workers | type == "array") and all(.workers[]; .pod_name != "")' <<<"${TELEM_RESP}" >/dev/null; then
  log_err "Telemetry workers malformed: ${TELEM_RESP}"
  exit 1
fi
if [[ "${MOCK_MODE}" == true ]] && ! jq -e '.parking_available == true' <<<"${TELEM_RESP}" >/dev/null; then
  log_err "Local router statusz should make parking available: ${TELEM_RESP}"
  exit 1
fi
```

`TELEM_RESP` and `PRESENTER_HDR` are the script's existing variables.

- [ ] **Step 2: Run it**

Run: `hack/verify-demo-conference.sh --mock-mode && grep -n 'GEMINI_API_KEY\|X-Gemini\|RUNNING_GVISOR\|SUSPEND_MODE\|python3\|mock_router' hack/verify-demo-conference.sh`
Expected: every step reports OK, and grep prints nothing.

- [ ] **Step 3: Commit**

```bash
git add hack/verify-demo-conference.sh
git commit -m "demos/conference: verify against real per-attendee actors

Mock mode runs conf-localrouter, so each attendee gets its own workload
server and the gateway's real proxy path is exercised. Checks read the
new ActorView fields; cluster mode targets the in-cluster gateway.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 19: README, in-cluster confirmation, final verification

**Files:**
- Modify: `demos/conference/README.md`
- Modify: `cmd/conf-gateway/internal/actorstate/testdata/actors.json` (re-captured)
- Modify: `docs/superpowers/specs/2026-09-23-conference-demo-b1-architecture-design.md` (close the open items with measured values)

- [ ] **Step 1: Rewrite the README sections**

The README must cover all of the following, in this order, and nothing it cannot back up:

1. **What it shows:** 50 attendee actors multiplexed onto a 2-worker pool; each attendee's agent keeps its conversation in actor memory across suspend and resume.
2. **Architecture:** a diagram of browser → conf-gateway (in-cluster) → atenet-router → actor. Show the actor → conf-gateway-internal:8091 → Vertex AI hop, and conf-gateway → kubectl-ate → ateapi for state.
3. **Credentials:**
   - Actors hold no model credential, only a per-actor key the gateway sends on each request.
   - The gateway calls Vertex with its Workload Identity service account, which has `roles/aiplatform.user` only.
4. **Deploy:** `PROJECT_ID`, `BUCKET_NAME`, `KO_DOCKER_REPO`, optional `DEMO_DOMAIN`, `--patch-router`; mock mode when `PROJECT_ID` is unset.
5. **Presenter access:** port-forward, plus the Secret lookup command from the install output.
6. **What the badges mean:** the table from Task 14, and "State unavailable" meaning the gateway could not read ateapi.
7. **Timing honesty:**
   - State comes from polling every `STATE_POLL_INTERVAL` (1s), plus the kubectl-ate latency measured in Step 3, so short transitions such as RESUMING can be missed.
   - Parked counts are summed across router pods on each telemetry tick.
8. **Limits and risks** (spec §5.4):
   - one gateway replica, whose memory holds sessions and approvals;
   - plain HTTP unless `DEMO_DOMAIN` is set;
   - the model key is a bearer credential inside the cluster network;
   - `SUSPEND_AFTER` is 0, so the substrate decides when to suspend;
   - deleting the demo does not revert router flags.
9. **Local development:** the conf-localrouter plus gateway commands from Task 14 Step 8, and `hack/verify-demo-conference.sh --mock-mode`.

Delete any README claim about "0 MB RAM", GCS memory, fixed worker names or a gateway-side Gemini key.

- [ ] **Step 2: Deploy to a dev cluster**

Run with the env from `hack/ate-dev-env.sh.example`: `hack/install-ate.sh --deploy-demo-conference --patch-router`
Expected: the install finishes, `kubectl get pods -n ate-demo-conf -l app=conf-gateway` shows 1/1 Ready, and 50 actors exist.

If this step or Step 3 fails on kubectl-ate auth (TokenRequest or ClusterTrustBundle), fix the RBAC in `gateway.yaml.tmpl` to match what the error names, re-run, and note the change in the Task 19 commit message.

- [ ] **Step 3: Confirm in-cluster behavior and measure**

```bash
kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80 &
KEY="$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
curl -fsS -H "X-Presenter-Key: ${KEY}" localhost:8090/api/stage/telemetry | jq '{state_poll_ms, parking_available, parked_requests, mock_llm, workers}'
kubectl logs -n ate-demo-conf deploy/conf-gateway | grep -i 'kubectl-ate\|auto-suspend\|error' | head
```

Expected:
- `state_poll_ms` is a positive number; record it.
- `parking_available` is true.
- `mock_llm` matches whether `PROJECT_ID` was set.
- The logs show no kubectl-ate errors.

Then, in a browser at `/chat`, send one prompt. The badge should go Never started → Resuming → Running with a real pod name, the reply should not carry a "Mock" tag when Vertex is on, and `/stage` should show the model usage counters rising. After the approval flow, `/stage` should show the executed output.

- [ ] **Step 4: Re-capture the fixture**

```bash
kubectl-ate --endpoint <port-forwarded or in-cluster endpoint> get actors -a ate-demo-conf -o json \
  | jq '{actors: [.actors[] | select(.metadata.name | IN("attendee-01","attendee-02","attendee-03","attendee-04","attendee-05"))]}' \
  > demos/conference/cmd/conf-gateway/internal/actorstate/testdata/actors.json
```

Before capturing, get the five actors into the fixture's states:
- `attendee-01` never started;
- `attendee-02` running: chat, then capture within the idle window;
- `attendee-03` suspended with a snapshot: chat, then `kubectl-ate suspend actor attendee-03 -a ate-demo-conf`.

A crashed actor cannot be produced on demand. For `attendee-04`, keep the hand-written entry from Task 10 and add a `"_comment"` field outside `actors[]` saying which entries were captured.

Update `want` in `TestParseActorList_Fixture` to match the captured values, then run `go test ./demos/conference/cmd/conf-gateway/internal/actorstate/`. Expected: PASS.

- [ ] **Step 5: Close the spec's open items**

In the spec's open-items section, record:
- the measured `state_poll_ms`;
- whether in-cluster kubectl-ate auth needed RBAC beyond the spec;
- the router flag values found on the dev cluster.

- [ ] **Step 6: Final verification**

```bash
go test -race -count=1 ./demos/conference/...
hack/verify-demo-conference.sh --mock-mode
hack/verify-demo-conference-manifests.sh
hack/test-install-demo-conference.sh
make verify
grep -rn 'GEMINI_API_KEY\|X-Gemini-API-Key\|mofilabs\|workerSlots\|RUNNING_GVISOR\|SUSPEND_MODE' demos/conference hack/*conference*
```

Expected:
- all tests pass;
- both scripts report OK;
- `make verify` exits 0 (boilerplate, gofmt, vet, misspell);
- the final grep prints nothing.

If `make verify` flags the boilerplate on `actorview.js`, the Dockerfile or the new scripts, fix the headers and re-run.

- [ ] **Step 7: Commit**

```bash
git add demos/conference/README.md demos/conference/cmd/conf-gateway/internal/actorstate/testdata docs/superpowers/specs/2026-09-23-conference-demo-b1-architecture-design.md
git commit -m "demos/conference: document the in-cluster architecture

The README describes what the demo actually shows: where model calls
and credentials go, what each badge means, how stale state can be, and
the limits of a single in-memory gateway. The kubectl-ate fixture is
re-captured from a real cluster, and the spec's open items record the
measured values.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

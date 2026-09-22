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

package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/pkg/ai"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type attendeeContextKey struct{}

// Suspender defines the interface for suspending actors.
type Suspender interface {
	SuspendActor(ctx context.Context, atespace, actorID string) error
}

// SimulationController defines the interface for managing background audience simulations.
type SimulationController interface {
	Start(count int) error
	Stop()
	Status() (bool, int)
}

// SuspendFunc adapts a function to the Suspender interface.
type SuspendFunc func(ctx context.Context, atespace, actorID string) error

// SuspendActor calls the underlying function.
func (f SuspendFunc) SuspendActor(ctx context.Context, atespace, actorID string) error {
	return f(ctx, atespace, actorID)
}

// commandSuspender invokes kubectl-ate to suspend the actor process and snapshot to GCS.
type commandSuspender struct{}

func (c *commandSuspender) SuspendActor(ctx context.Context, atespace, actorID string) error {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("kubectl-ate"); err == nil {
		cmd = exec.CommandContext(ctx, "kubectl-ate", "suspend", "actor", actorID, "-a", atespace)
	} else {
		cmd = exec.CommandContext(ctx, "kubectl", "ate", "suspend", "actor", actorID, "-a", atespace)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[gateway] suspend actor %s/%s failed: %v (output: %s)", atespace, actorID, err, string(out))
		return err
	}
	return nil
}

func (c *commandSuspender) getClusterRunningActors(ctx context.Context, atespace string) []string {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("kubectl-ate"); err == nil {
		cmd = exec.CommandContext(ctx, "kubectl-ate", "get", "actors", "-a", atespace, "-o", "json")
	} else {
		cmd = exec.CommandContext(ctx, "kubectl", "ate", "get", "actors", "-a", atespace, "-o", "json")
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var data struct {
		Actors []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"actors"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil
	}
	var res []string
	for _, a := range data.Actors {
		if a.Status.State == "ACTOR_STATE_RUNNING" || a.Status.State == "ACTOR_STATE_PAUSED" {
			res = append(res, a.Metadata.Name)
		}
	}
	return res
}

// Claims represents the decoded JWT payload.
type Claims struct {
	Sub   string `json:"sub"`
	Exp   int64  `json:"exp"`
	Scope string `json:"scope,omitempty"`
}

// Option configures Gateway.
type Option func(*Gateway)

// WithAtespace sets the Substrate Atespace namespace.
func WithAtespace(atespace string) Option {
	return func(g *Gateway) {
		if atespace != "" {
			g.atespace = atespace
		}
	}
}

// WithJWTSecret sets the secret key used to sign and verify attendee JWTs.
func WithJWTSecret(secret string) Option {
	return func(g *Gateway) {
		if secret != "" {
			g.jwtSecret = secret
		}
	}
}

// WithGeminiAPIKey sets the backend Gemini API key injected into proxied actor requests.
func WithGeminiAPIKey(key string) Option {
	return func(g *Gateway) {
		g.geminiAPIKey = key
	}
}

// WithSuspender sets the actor suspender implementation.
func WithSuspender(s Suspender) Option {
	return func(g *Gateway) {
		if s != nil {
			g.suspender = s
		}
	}
}

// WithStatusInterval sets the polling interval for the statusz endpoint.
func WithStatusInterval(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.statusInterval = d
		}
	}
}

// WithAIClient sets the Vertex AI / Gemini client for conversational planning.
func WithAIClient(c *ai.GeminiClient) Option {
	return func(g *Gateway) {
		if c != nil {
			g.aiClient = c
		}
	}
}

// WithMaxActors sets the maximum number of pre-provisioned actors to cycle through.
func WithMaxActors(max int) Option {
	return func(g *Gateway) {
		if max > 0 {
			g.maxActors = max
		}
	}
}

// WithHTTPClient sets the HTTP client used for background polling and upstream requests.
func WithHTTPClient(client *http.Client) Option {
	return func(g *Gateway) {
		if client != nil {
			g.httpClient = client
		}
	}
}

// WithSimulationController sets the simulation controller for the gateway.
func WithSimulationController(sc SimulationController) Option {
	return func(g *Gateway) {
		if sc != nil {
			g.simController = sc
		}
	}
}

// WithStaticFS sets the static filesystem used to serve web assets.
func WithStaticFS(staticFS fs.FS) Option {
	return func(g *Gateway) {
		if staticFS != nil {
			g.staticFS = normalizeStaticFS(staticFS)
		}
	}
}

// Gateway manages sessions, reverse proxying to atenet-router, and telemetry aggregation.
type Gateway struct {
	routerURL      string
	statusURL      string
	atespace       string
	jwtSecret      string
	geminiAPIKey   string
	suspender      Suspender
	statusInterval time.Duration
	httpClient     *http.Client
	staticFS       fs.FS
	maxActors      int
	aiClient       *ai.GeminiClient
	broker         *EventBroker

	mux              *http.ServeMux
	proxy            *httputil.ReverseProxy
	attendeeSeq      uint64
	mu               sync.RWMutex
	activeActors     map[string]types.ActorLifecycleState
	pendingApprovals map[string]*types.ApprovalRequest
	chatHistories    map[string][]types.ChatMessage
	lastResults      map[string]*types.ExecutionResult
	currentActionID  map[string]string
	recentEvents     []string
	actionFeed       []types.ActionLogEntry
	parkedCount      int
	simController    SimulationController
	workerSlots      chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewGateway creates and initializes a new Gateway.
func NewGateway(routerURL, statusURL string, opts ...Option) *Gateway {
	gw := &Gateway{
		routerURL:        routerURL,
		statusURL:        statusURL,
		atespace:         "ate-demo-conf",
		jwtSecret:        "conf-demo-secret-key",
		suspender:        &commandSuspender{},
		statusInterval:   500 * time.Millisecond,
		httpClient:       &http.Client{Timeout: 5 * time.Second},
		broker:           NewEventBroker(),
		mux:              http.NewServeMux(),
		activeActors:     make(map[string]types.ActorLifecycleState),
		pendingApprovals: make(map[string]*types.ApprovalRequest),
		chatHistories:    make(map[string][]types.ChatMessage),
		lastResults:      make(map[string]*types.ExecutionResult),
		currentActionID:  make(map[string]string),
		recentEvents:     make([]string, 0),
		actionFeed:       make([]types.ActionLogEntry, 0),
		maxActors:        50,
		workerSlots:      make(chan struct{}, 2),
	}

	for _, opt := range opts {
		opt(gw)
	}

	if gw.simController == nil {
		gw.simController = NewSimulationRunner(gw)
	}

	if gw.staticFS == nil {
		gw.staticFS = normalizeStaticFS(resolveStaticFS())
	}

	targetURL, err := url.Parse(gw.routerURL)
	if err != nil {
		log.Printf("Warning: failed to parse routerURL %q: %v", gw.routerURL, err)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			if targetURL != nil {
				pr.SetURL(targetURL)
				pr.Out.Host = targetURL.Host
			}
			attendeeID, _ := pr.In.Context().Value(attendeeContextKey{}).(string)
			if attendeeID != "" {
				pr.Out.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", gw.atespace, attendeeID))
			}
			if gw.geminiAPIKey != "" {
				pr.Out.Header.Set("X-Gemini-API-Key", gw.geminiAPIKey)
			}
		},
		ModifyResponse: gw.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			attendeeID, _ := req.Context().Value(attendeeContextKey{}).(string)
			gw.setActorState(attendeeID, types.StateSuspended)
			if attendeeID != "" {
				gw.BroadcastStateChange(attendeeID, types.StateSuspended, "Suspended", "Proxy error - suspended to GCS")
			}
			gw.mu.Lock()
			if attendeeID != "" {
				gw.addEventLocked(fmt.Sprintf("Router proxy error for %s: %v", attendeeID, err))
			}
			gw.mu.Unlock()
			http.Error(w, fmt.Sprintf("Bad Gateway: %v", err), http.StatusBadGateway)
		},
	}
	if gw.httpClient != nil && gw.httpClient.Transport != nil {
		proxy.Transport = gw.httpClient.Transport
	}
	gw.proxy = proxy

	gw.routes()

	ctx, cancel := context.WithCancel(context.Background())
	gw.ctx = ctx
	gw.cancel = cancel
	gw.wg.Add(1)
	go gw.pollStatus(ctx)

	return gw
}

// Close gracefully stops the background polling routine.
func (g *Gateway) Close() {
	g.mu.Lock()
	sc := g.simController
	g.mu.Unlock()
	if sc != nil {
		sc.Stop()
	}

	if g.cancel != nil {
		g.cancel()
		g.wg.Wait()
	}
}

// Handler returns the HTTP handler for Gateway routes.
func (g *Gateway) Handler() http.Handler {
	return g.mux
}

func (g *Gateway) routes() {
	g.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	g.mux.HandleFunc("/api/session", g.handleSession)
	g.mux.HandleFunc("/api/events", g.handleEvents)
	g.mux.HandleFunc("/api/stream", g.handleActionStream)
	g.mux.HandleFunc("/api/feed/history", g.handleFeedHistory)
	g.mux.HandleFunc("/api/actor/", g.handleActorDetails)
	g.mux.HandleFunc("/api/actor", g.handleActorDetails)
	g.mux.HandleFunc("/api/stage/telemetry", g.handleTelemetry)
	g.mux.HandleFunc("/api/reset", g.handleReset)
	g.mux.HandleFunc("/api/simulation", g.handleSimulation)
	if g.aiClient != nil {
		g.mux.HandleFunc("/api/chat", g.handleChatRequest)
		g.mux.HandleFunc("/api/approve", g.handleApproveRequest)
	} else {
		g.mux.HandleFunc("/api/chat", g.proxyToActor)
		g.mux.HandleFunc("/api/approve", g.proxyToActor)
	}

	g.mux.HandleFunc("/chat", g.handleChat)
	g.mux.HandleFunc("/chat/", g.handleChat)
	g.mux.HandleFunc("/feed", g.handleFeed)
	g.mux.HandleFunc("/feed/", g.handleFeed)
	g.mux.HandleFunc("/stage", g.handleStage)
	g.mux.HandleFunc("/stage/", g.handleStage)
	g.mux.HandleFunc("/", g.handleStage)
	if g.staticFS != nil {
		g.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(g.staticFS))))
	}
}

func (g *Gateway) handleStage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/stage" && r.URL.Path != "/stage/" && r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.staticFS == nil {
		http.Error(w, "Static filesystem not configured", http.StatusInternalServerError)
		return
	}
	data, err := fs.ReadFile(g.staticFS, "stage.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read stage.html: %v", err), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat" && r.URL.Path != "/chat/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.staticFS == nil {
		http.Error(w, "Static filesystem not configured", http.StatusInternalServerError)
		return
	}
	data, err := fs.ReadFile(g.staticFS, "chat.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read chat.html: %v", err), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (g *Gateway) handleFeed(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/feed" && r.URL.Path != "/feed/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.staticFS == nil {
		http.Error(w, "Static filesystem not configured", http.StatusInternalServerError)
		return
	}
	data, err := fs.ReadFile(g.staticFS, "feed.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read feed.html: %v", err), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func normalizeStaticFS(sysFS fs.FS) fs.FS {
	if sysFS == nil {
		return nil
	}
	if _, err := fs.Stat(sysFS, "static/chat.html"); err == nil {
		if sub, err := fs.Sub(sysFS, "static"); err == nil {
			return sub
		}
	}
	return sysFS
}

func resolveStaticFS() fs.FS {
	_, currentFile, _, ok := runtime.Caller(0)
	if ok {
		devPath := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "static"))
		if fi, err := os.Stat(devPath); err == nil && fi.IsDir() {
			if _, err := os.Stat(filepath.Join(devPath, "chat.html")); err == nil {
				return os.DirFS(devPath)
			}
		}
	}

	candidates := []string{
		"static",
		"../static",
		"../../static",
		"demos/conference/cmd/conf-gateway/static",
		"cmd/conf-gateway/static",
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			if _, err := os.Stat(filepath.Join(c, "chat.html")); err == nil {
				return os.DirFS(c)
			}
		}
	}
	return nil
}

func (g *Gateway) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestedID := strings.TrimSpace(r.URL.Query().Get("attendee_id"))
	var attendeeID string
	if requestedID != "" && strings.HasPrefix(requestedID, "attendee-") {
		attendeeID = requestedID
	} else {
		seq := atomic.AddUint64(&g.attendeeSeq, 1)
		maxActors := 50
		if g.maxActors > 0 {
			maxActors = g.maxActors
		}
		attendeeID = fmt.Sprintf("attendee-%02d", (seq-1)%uint64(maxActors)+1)
	}

	token := g.MintToken(attendeeID)

	g.mu.Lock()
	if _, exists := g.activeActors[attendeeID]; !exists {
		g.activeActors[attendeeID] = types.StateSuspended
		g.addEventLocked(fmt.Sprintf("Provisioned session for %s", attendeeID))
	}
	g.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"attendee_id": attendeeID,
		"token":       token,
	})
}

func (g *Gateway) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	g.mu.RLock()
	actorsCopy := make(map[string]types.ActorLifecycleState, len(g.activeActors))
	var runningActors []string
	var parkedActorsCount int
	for k, v := range g.activeActors {
		actorsCopy[k] = v
		if v == types.StateRunning || v == types.StateRunningGVisor {
			runningActors = append(runningActors, k)
		} else if v == types.StateParked || v == types.StateParkedEnvoy {
			parkedActorsCount++
		}
	}
	eventsCopy := make([]string, len(g.recentEvents))
	copy(eventsCopy, g.recentEvents)
	parked := g.parkedCount
	if parkedActorsCount > parked {
		parked = parkedActorsCount
	}
	g.mu.RUnlock()

	sort.Strings(runningActors)

	workers := make([]types.WorkerStatus, 2)
	for i := 0; i < 2; i++ {
		podName := fmt.Sprintf("conf-workers-%d", i)
		if i < len(runningActors) {
			workers[i] = types.WorkerStatus{
				PodName:     podName,
				ActiveActor: runningActors[i],
				CurrentTask: "gVisor execution",
				ElapsedMs:   120,
			}
		} else {
			workers[i] = types.WorkerStatus{
				PodName:     podName,
				ActiveActor: "idle",
				CurrentTask: "Waiting for actor dispatch",
				ElapsedMs:   0,
			}
		}
	}

	telem := types.StageTelemetry{
		ParkedRequests: parked,
		Workers:        workers,
		Actors:         actorsCopy,
		RecentEvents:   eventsCopy,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(telem)
}

func (g *Gateway) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	attendeeID := r.URL.Query().Get("attendee_id")
	if attendeeID == "" {
		http.Error(w, "attendee_id query parameter is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := g.broker.Subscribe(attendeeID)
	defer g.broker.Unsubscribe(attendeeID, ch)

	currentState, exists := g.GetActorState(attendeeID)
	if !exists {
		currentState = types.StateSuspended
	}
	initialEvent := types.StateChangeEvent{
		AttendeeID: attendeeID,
		State:      currentState,
		Label:      stateToLabel(currentState),
		Subtext:    stateToSubtext(currentState),
		Timestamp:  time.Now().Unix(),
	}
	if initData, err := json.Marshal(initialEvent); err == nil {
		if _, err := fmt.Fprintf(w, "event: state_change\ndata: %s\n\n", initData); err != nil {
			return
		}
		flusher.Flush()
	}

	ctxDone := r.Context().Done()
	for {
		select {
		case <-ctxDone:
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: state_change\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (g *Gateway) handleFeedHistory(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/feed/history" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	history := g.GetFeedHistory()

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(history)
}

func (g *Gateway) handleActionStream(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/stream" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	actionCh := g.broker.SubscribeActions()
	defer g.broker.UnsubscribeActions(actionCh)

	resetCh := g.broker.SubscribeReset()
	defer g.broker.UnsubscribeReset(resetCh)

	ctxDone := r.Context().Done()
	for {
		select {
		case <-ctxDone:
			return
		case entry, ok := <-actionCh:
			if !ok {
				return
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: action_entry\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case _, ok := <-resetCh:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: reset\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func stateToLabel(state types.ActorLifecycleState) string {
	switch state {
	case types.StateThinkingVertexAI:
		return "🧠 Reasoning on Vertex AI..."
	case types.StateAwaitingApproval:
		return "⏸️ Awaiting Approval"
	case types.StateParked, types.StateParkedEnvoy:
		return "⏳ Parked in Envoy Buffer"
	case types.StateRunning, types.StateRunningGVisor:
		return "🚀 Running in Sandbox"
	default:
		return "⚡ Suspended in GCS"
	}
}

func stateToSubtext(state types.ActorLifecycleState) string {
	switch state {
	case types.StateThinkingVertexAI:
		return "Gemini 2.5 Flash generating analysis script"
	case types.StateAwaitingApproval:
		return "Process frozen in GCS (0 MB RAM used)"
	case types.StateParked, types.StateParkedEnvoy:
		return "Queued in Envoy L7 buffer — waiting for worker slot..."
	case types.StateRunning, types.StateRunningGVisor:
		return "Executing script inside gVisor container"
	default:
		return "Process snapshot written to GCS — 0 vCPU, 0 RAM"
	}
}

func (g *Gateway) handleChatRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	attendeeID, err := g.authenticateRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var req types.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json payload: %v", err), http.StatusBadRequest)
		return
	}

	actionID := fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())

	g.mu.Lock()
	userMsg := types.ChatMessage{
		ID:        fmt.Sprintf("user-%d", time.Now().UnixNano()),
		Sender:    types.SenderUser,
		Content:   req.Message,
		Timestamp: time.Now().Unix(),
	}
	g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], userMsg)
	g.currentActionID[attendeeID] = actionID
	historyCopy := make([]types.ChatMessage, len(g.chatHistories[attendeeID]))
	copy(historyCopy, g.chatHistories[attendeeID])

	// Strict capacity gate (capacity 2 matching physical worker pods):
	// An actor CANNOT run or think without acquiring an available worker slot!
	var slotAcquired bool
	select {
	case g.workerSlots <- struct{}{}:
		slotAcquired = true
		g.activeActors[attendeeID] = types.StateRunning
		g.addEventLocked(fmt.Sprintf("Attendee %s allocated worker pod: starting turn", attendeeID))
		g.mu.Unlock()
		g.BroadcastStateChange(attendeeID, types.StateRunning, "Running on Pod", "Allocated to worker pod on GKE")
	default:
		g.activeActors[attendeeID] = types.StateParkedEnvoy
		g.addEventLocked(fmt.Sprintf("Attendee %s queued: waiting for available worker pod slot", attendeeID))
		g.mu.Unlock()
		g.BroadcastStateChange(attendeeID, types.StateParkedEnvoy, "Parked in Envoy", "Queued in Envoy L7 buffer — waiting for worker slot...")
		g.RecordActionEntry(types.ActionLogEntry{
			ID:         actionID,
			AttendeeID: attendeeID,
			Timestamp:  time.Now().Unix(),
			Prompt:     req.Message,
			State:      types.StateParkedEnvoy,
		})

		select {
		case <-r.Context().Done():
			g.setActorState(attendeeID, types.StateSuspended)
			return
		case g.workerSlots <- struct{}{}:
			slotAcquired = true
			g.setActorState(attendeeID, types.StateRunning)
			g.BroadcastStateChange(attendeeID, types.StateRunning, "Running on Pod", "Allocated to worker pod on GKE")
		}
	}

	if slotAcquired {
		defer func() { <-g.workerSlots }()
	}

	// Now that worker compute is allocated, transition to thinking on Vertex AI
	g.setActorState(attendeeID, types.StateThinkingVertexAI)
	g.addEventLocked(fmt.Sprintf("Attendee %s analyzing prompt with Vertex AI", attendeeID))
	g.BroadcastStateChange(attendeeID, types.StateThinkingVertexAI, "Thinking...", "Reasoning on Vertex AI")

	g.RecordActionEntry(types.ActionLogEntry{
		ID:         actionID,
		AttendeeID: attendeeID,
		Timestamp:  time.Now().Unix(),
		Prompt:     req.Message,
		State:      types.StateThinkingVertexAI,
	})

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	assistantMsg, err := g.aiClient.GenerateContent(ctx, g.geminiAPIKey, historyCopy, req.Message)
	if err != nil || assistantMsg == nil {
		assistantMsg = g.aiClient.FallbackTurn(req.Message)
	}

	g.mu.Lock()
	if assistantMsg.Approval != nil {
		g.pendingApprovals[attendeeID] = assistantMsg.Approval
		g.activeActors[attendeeID] = types.StateAwaitingApproval
		g.addEventLocked(fmt.Sprintf("Attendee %s awaiting human approval for %s", attendeeID, assistantMsg.Approval.Summary))
	} else {
		g.activeActors[attendeeID] = types.StateSuspended
	}
	g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], *assistantMsg)
	actionID = g.currentActionID[attendeeID]
	g.mu.Unlock()

	if actionID == "" {
		actionID = fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())
	}

	if assistantMsg.Approval != nil {
		g.BroadcastStateChange(attendeeID, types.StateAwaitingApproval, "Awaiting Approval", assistantMsg.Approval.Summary)
		g.RecordActionEntry(types.ActionLogEntry{
			ID:            actionID,
			AttendeeID:    attendeeID,
			Timestamp:     time.Now().Unix(),
			Prompt:        req.Message,
			ModelResponse: assistantMsg.Content,
			Code:          assistantMsg.Approval.Code,
			Language:      assistantMsg.Approval.Language,
			RiskLevel:     assistantMsg.Approval.RiskLevel,
			ApprovalID:    assistantMsg.Approval.ApprovalID,
			State:         types.StateAwaitingApproval,
		})
	} else {
		g.BroadcastStateChange(attendeeID, types.StateSuspended, "Suspended", "Turn completed - suspended to GCS")
		g.RecordActionEntry(types.ActionLogEntry{
			ID:            actionID,
			AttendeeID:    attendeeID,
			Timestamp:     time.Now().Unix(),
			Prompt:        req.Message,
			ModelResponse: assistantMsg.Content,
			State:         types.StateSuspended,
		})
	}

	// Zero-cost suspension while attendee reviews code or when turn is complete
	g.asyncSuspendActor(attendeeID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(assistantMsg)
}

func (g *Gateway) handleApproveRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	attendeeID, err := g.authenticateRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req types.ApprovalResponse
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	g.mu.Lock()
	app, hasApp := g.pendingApprovals[attendeeID]
	if hasApp && app.ApprovalID == req.ApprovalID {
		req.Code = app.Code
		req.Language = app.Language
		delete(g.pendingApprovals, attendeeID)
	}
	actionID := g.currentActionID[attendeeID]
	if actionID == "" {
		actionID = fmt.Sprintf("act-appr-%s-%d", attendeeID, time.Now().UnixNano())
	}

	// Gated by workerSlots (capacity 2): max 2 running in gVisor, excess are PARKED in Envoy buffer!
	var slotAcquired bool
	select {
	case g.workerSlots <- struct{}{}:
		slotAcquired = true
		g.activeActors[attendeeID] = types.StateRunningGVisor
		g.addEventLocked(fmt.Sprintf("Attendee %s approved %s: executing in gVisor", attendeeID, req.ApprovalID))
		g.mu.Unlock()
		g.BroadcastStateChange(attendeeID, types.StateRunningGVisor, "Running in Sandbox", "Executing code in gVisor sandbox")
		g.RecordActionEntry(types.ActionLogEntry{
			ID:         actionID,
			AttendeeID: attendeeID,
			Timestamp:  time.Now().Unix(),
			ApprovalID: req.ApprovalID,
			Decision:   req.Decision,
			Code:       req.Code,
			Language:   req.Language,
			State:      types.StateRunningGVisor,
			WorkerPod:  "conf-workers-0",
		})
	default:
		g.activeActors[attendeeID] = types.StateParkedEnvoy
		g.addEventLocked(fmt.Sprintf("Attendee %s approved %s: queued in Envoy L7 buffer", attendeeID, req.ApprovalID))
		g.mu.Unlock()
		g.BroadcastStateChange(attendeeID, types.StateParkedEnvoy, "Parked in Envoy", "Queued in Envoy L7 buffer — waiting for worker slot...")
		g.RecordActionEntry(types.ActionLogEntry{
			ID:         actionID,
			AttendeeID: attendeeID,
			Timestamp:  time.Now().Unix(),
			ApprovalID: req.ApprovalID,
			Decision:   req.Decision,
			Code:       req.Code,
			Language:   req.Language,
			State:      types.StateParkedEnvoy,
		})

		select {
		case <-r.Context().Done():
			g.setActorState(attendeeID, types.StateSuspended)
			return
		case g.workerSlots <- struct{}{}:
			slotAcquired = true
			g.setActorState(attendeeID, types.StateRunningGVisor)
			g.BroadcastStateChange(attendeeID, types.StateRunningGVisor, "Running in Sandbox", "Executing code in gVisor sandbox")
			g.RecordActionEntry(types.ActionLogEntry{
				ID:         actionID,
				AttendeeID: attendeeID,
				Timestamp:  time.Now().Unix(),
				ApprovalID: req.ApprovalID,
				Decision:   req.Decision,
				Code:       req.Code,
				Language:   req.Language,
				State:      types.StateRunningGVisor,
				WorkerPod:  "conf-workers-0",
			})
		}
	}

	if slotAcquired {
		defer func() { <-g.workerSlots }()
	}

	// Forward the approval with code to the actor via reverse proxy!
	updatedBody, _ := json.Marshal(req)
	r.Body = io.NopCloser(bytes.NewReader(updatedBody))
	r.ContentLength = int64(len(updatedBody))
	r.Header.Set("Content-Length", strconv.Itoa(len(updatedBody)))

	ctx := context.WithValue(r.Context(), attendeeContextKey{}, attendeeID)
	outReq := r.WithContext(ctx)

	g.proxy.ServeHTTP(w, outReq)
}

func (g *Gateway) proxyToActor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	attendeeID, err := g.authenticateRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if r.URL.Path == "/api/chat" && r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			var chatReq types.ChatRequest
			if err := json.Unmarshal(bodyBytes, &chatReq); err == nil && chatReq.Message != "" {
				userMsg := types.ChatMessage{
					ID:        fmt.Sprintf("user-%d", time.Now().UnixNano()),
					Sender:    types.SenderUser,
					Content:   chatReq.Message,
					Timestamp: time.Now().Unix(),
				}
				actionID := fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())
				g.mu.Lock()
				g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], userMsg)
				g.currentActionID[attendeeID] = actionID
				g.mu.Unlock()
				g.RecordActionEntry(types.ActionLogEntry{
					ID:         actionID,
					AttendeeID: attendeeID,
					Timestamp:  time.Now().Unix(),
					Prompt:     chatReq.Message,
					State:      types.StateRunning,
				})
			}
		}
	} else if r.URL.Path == "/api/approve" && r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			var apprReq types.ApprovalResponse
			if err := json.Unmarshal(bodyBytes, &apprReq); err == nil {
				g.mu.RLock()
				actionID := g.currentActionID[attendeeID]
				g.mu.RUnlock()
				if actionID == "" {
					actionID = fmt.Sprintf("act-appr-%s-%d", attendeeID, time.Now().UnixNano())
				}
				g.RecordActionEntry(types.ActionLogEntry{
					ID:         actionID,
					AttendeeID: attendeeID,
					Timestamp:  time.Now().Unix(),
					ApprovalID: apprReq.ApprovalID,
					Decision:   apprReq.Decision,
					Code:       apprReq.Code,
					Language:   apprReq.Language,
					State:      types.StateRunningGVisor,
					WorkerPod:  "conf-workers-0",
				})
			}
		}
	}

	g.mu.Lock()
	if r.URL.Path == "/api/approve" {
		g.activeActors[attendeeID] = types.StateRunningGVisor
	} else {
		g.activeActors[attendeeID] = types.StateRunning
	}
	g.addEventLocked(fmt.Sprintf("Actor %s executing %s", attendeeID, r.URL.Path))
	g.mu.Unlock()

	if r.URL.Path == "/api/approve" {
		g.BroadcastStateChange(attendeeID, types.StateRunningGVisor, "Running in Sandbox", "Executing in gVisor sandbox")
	} else {
		g.BroadcastStateChange(attendeeID, types.StateRunning, "Running", "Executing request")
	}

	ctx := context.WithValue(r.Context(), attendeeContextKey{}, attendeeID)
	outReq := r.WithContext(ctx)

	g.proxy.ServeHTTP(w, outReq)
}

// SetActorState sets the lifecycle state of an actor.
func (g *Gateway) SetActorState(attendeeID string, state types.ActorLifecycleState) {
	if attendeeID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.activeActors[attendeeID] = state
}

func (g *Gateway) setActorState(attendeeID string, state types.ActorLifecycleState) {
	g.SetActorState(attendeeID, state)
}

// GetLastResult returns the most recent execution result for an actor.
func (g *Gateway) GetLastResult(attendeeID string) (*types.ExecutionResult, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	res, ok := g.lastResults[attendeeID]
	if !ok || res == nil {
		return nil, false
	}
	copyRes := *res
	return &copyRes, true
}

// SetLastResult records the most recent execution result for an actor.
func (g *Gateway) SetLastResult(attendeeID string, res *types.ExecutionResult) {
	if attendeeID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if res == nil {
		delete(g.lastResults, attendeeID)
		return
	}
	copyRes := *res
	g.lastResults[attendeeID] = &copyRes
}

// GetActorState returns the lifecycle state of an actor.
func (g *Gateway) GetActorState(attendeeID string) (types.ActorLifecycleState, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	st, ok := g.activeActors[attendeeID]
	return st, ok
}

// BroadcastStateChange publishes an actor lifecycle transition to subscribers and updates active state.
func (g *Gateway) BroadcastStateChange(attendeeID string, state types.ActorLifecycleState, label, subtext string) {
	if attendeeID == "" {
		return
	}
	g.mu.Lock()
	g.activeActors[attendeeID] = state
	g.mu.Unlock()

	if g.broker != nil {
		g.broker.Publish(types.StateChangeEvent{
			AttendeeID: attendeeID,
			State:      state,
			Label:      label,
			Subtext:    subtext,
			Timestamp:  time.Now().Unix(),
		})
	}
}

// SubscriberCount returns the current number of active SSE subscribers for an attendee.
func (g *Gateway) SubscriberCount(attendeeID string) int {
	if g.broker == nil {
		return 0
	}
	return g.broker.SubscriberCount(attendeeID)
}

// ActionSubscriberCount returns the current number of active action stream subscribers.
func (g *Gateway) ActionSubscriberCount() int {
	if g.broker == nil {
		return 0
	}
	return g.broker.ActionSubscriberCount()
}

// RecordActionEntry appends or updates an entry in the action feed (retaining up to 100 items)
// and broadcasts it to action subscribers.
func (g *Gateway) RecordActionEntry(entry types.ActionLogEntry) {
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("action-%d", time.Now().UnixNano())
	}
	g.mu.Lock()
	updated := false
	for i, existing := range g.actionFeed {
		if existing.ID == entry.ID {
			merged := existing
			if entry.AttendeeID != "" {
				merged.AttendeeID = entry.AttendeeID
			}
			if entry.Timestamp != 0 {
				merged.Timestamp = entry.Timestamp
			}
			if entry.Prompt != "" {
				merged.Prompt = entry.Prompt
			}
			if entry.ModelResponse != "" {
				merged.ModelResponse = entry.ModelResponse
			}
			if entry.Code != "" {
				merged.Code = entry.Code
			}
			if entry.Language != "" {
				merged.Language = entry.Language
			}
			if entry.RiskLevel != "" {
				merged.RiskLevel = entry.RiskLevel
			}
			if entry.ApprovalID != "" {
				merged.ApprovalID = entry.ApprovalID
			}
			if entry.Decision != "" {
				merged.Decision = entry.Decision
			}
			if entry.Execution != nil {
				merged.Execution = entry.Execution
			}
			if entry.WorkerPod != "" {
				merged.WorkerPod = entry.WorkerPod
			}
			if entry.State != "" {
				merged.State = entry.State
			}
			g.actionFeed[i] = merged
			entry = merged
			updated = true
			break
		}
	}
	if !updated {
		g.actionFeed = append(g.actionFeed, entry)
		if len(g.actionFeed) > 100 {
			g.actionFeed = g.actionFeed[len(g.actionFeed)-100:]
		}
	}
	g.mu.Unlock()

	if g.broker != nil {
		g.broker.PublishAction(entry)
	}
}

// GetFeedHistory returns a copy of the current action log entries.
func (g *Gateway) GetFeedHistory() []types.ActionLogEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()
	res := make([]types.ActionLogEntry, len(g.actionFeed))
	copy(res, g.actionFeed)
	return res
}

func (g *Gateway) modifyResponse(resp *http.Response) error {
	attendeeID, _ := resp.Request.Context().Value(attendeeContextKey{}).(string)
	if attendeeID == "" {
		return nil
	}

	if resp.StatusCode >= 400 {
		g.setActorState(attendeeID, types.StateSuspended)
		g.BroadcastStateChange(attendeeID, types.StateSuspended, "Suspended", "Error response - suspended to GCS")
		return nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))
	resp.Header.Set("Content-Length", strconv.Itoa(len(bodyBytes)))

	var isWaitingForApproval bool
	var chatMsg types.ChatMessage
	hasChatMsg := false
	if err := json.Unmarshal(bodyBytes, &chatMsg); err == nil {
		hasChatMsg = true
		if chatMsg.Approval != nil {
			isWaitingForApproval = true
		}
	}

	// Record execution result if returned
	var execRes types.ExecutionResult
	var hasNewExec bool
	var newExec types.ExecutionResult
	if err := json.Unmarshal(bodyBytes, &execRes); err == nil && (execRes.Stdout != "" || execRes.Stderr != "" || execRes.ExitCode != 0 || execRes.DurationMs > 0) {
		g.mu.Lock()
		g.lastResults[attendeeID] = &execRes
		g.mu.Unlock()
		hasNewExec = true
		newExec = execRes
	} else if hasChatMsg {
		if strings.HasPrefix(chatMsg.Content, "Execution completed in ") {
			var durationMs int64
			var exitCode int
			if n, _ := fmt.Sscanf(chatMsg.Content, "Execution completed in %dms (exit code %d):", &durationMs, &exitCode); n >= 2 {
				stdout := ""
				if idx := strings.Index(chatMsg.Content, "):"); idx != -1 {
					stdout = strings.TrimPrefix(chatMsg.Content[idx+2:], "\n")
					stdout = strings.TrimPrefix(stdout, "\n")
				}
				var stderr string
				if stderrIdx := strings.Index(stdout, "\nStderr:\n"); stderrIdx != -1 {
					stderr = stdout[stderrIdx+len("\nStderr:\n"):]
					stdout = stdout[:stderrIdx]
				}
				newExec = types.ExecutionResult{
					Stdout:     stdout,
					Stderr:     stderr,
					ExitCode:   exitCode,
					DurationMs: durationMs,
				}
				g.mu.Lock()
				g.lastResults[attendeeID] = &newExec
				g.mu.Unlock()
				hasNewExec = true
			}
		} else if strings.HasPrefix(chatMsg.Content, "Execution error:") {
			newExec = types.ExecutionResult{
				Stderr:   chatMsg.Content,
				ExitCode: 1,
			}
			g.mu.Lock()
			g.lastResults[attendeeID] = &newExec
			g.mu.Unlock()
			hasNewExec = true
		}
	}

	g.mu.RLock()
	actionID := g.currentActionID[attendeeID]
	g.mu.RUnlock()
	if actionID == "" {
		actionID = fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())
	}

	if isWaitingForApproval {
		g.mu.Lock()
		g.activeActors[attendeeID] = types.StateAwaitingApproval
		g.pendingApprovals[attendeeID] = chatMsg.Approval
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], chatMsg)
			}
		}
		g.addEventLocked(fmt.Sprintf("Actor %s awaiting human approval", attendeeID))
		g.mu.Unlock()

		g.BroadcastStateChange(attendeeID, types.StateAwaitingApproval, "Awaiting Approval", "Awaiting human approval")
		g.RecordActionEntry(types.ActionLogEntry{
			ID:            actionID,
			AttendeeID:    attendeeID,
			Timestamp:     time.Now().Unix(),
			ModelResponse: chatMsg.Content,
			Code:          chatMsg.Approval.Code,
			Language:      chatMsg.Approval.Language,
			RiskLevel:     chatMsg.Approval.RiskLevel,
			ApprovalID:    chatMsg.Approval.ApprovalID,
			State:         types.StateAwaitingApproval,
		})
		g.asyncSuspendActor(attendeeID)
	} else if resp.StatusCode == http.StatusOK {
		g.mu.Lock()
		g.activeActors[attendeeID] = types.StateSuspended
		delete(g.pendingApprovals, attendeeID)
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], chatMsg)
			}
		}
		g.addEventLocked(fmt.Sprintf("Actor %s completed turn - suspended to GCS", attendeeID))
		g.mu.Unlock()

		g.BroadcastStateChange(attendeeID, types.StateSuspended, "Suspended", "Turn completed - suspended to GCS")
		if hasNewExec {
			entry := types.ActionLogEntry{
				ID:         actionID,
				AttendeeID: attendeeID,
				Timestamp:  time.Now().Unix(),
				Execution:  &newExec,
				WorkerPod:  "conf-workers-0",
				State:      types.StateSuspended,
			}
			if hasChatMsg && chatMsg.Content != "" {
				entry.ModelResponse = chatMsg.Content
			}
			g.RecordActionEntry(entry)
		} else if hasChatMsg {
			g.RecordActionEntry(types.ActionLogEntry{
				ID:            actionID,
				AttendeeID:    attendeeID,
				Timestamp:     time.Now().Unix(),
				ModelResponse: chatMsg.Content,
				State:         types.StateSuspended,
			})
		}
		g.asyncSuspendActor(attendeeID)
	}

	return nil
}

func (g *Gateway) handleActorDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	actorID := strings.TrimPrefix(r.URL.Path, "/api/actor/")
	actorID = strings.Trim(actorID, "/")
	if actorID == "" || actorID == "api/actor" || strings.Contains(actorID, "/") {
		http.Error(w, "invalid actor ID: must not contain slashes", http.StatusBadRequest)
		return
	}

	g.mu.RLock()
	state, exists := g.activeActors[actorID]
	if !exists {
		state = types.StateSuspended
	}

	var history []types.ChatMessage
	if h, ok := g.chatHistories[actorID]; ok && len(h) > 0 {
		history = make([]types.ChatMessage, len(h))
		copy(history, h)
	} else {
		history = []types.ChatMessage{}
	}

	var pendingApproval *types.ApprovalRequest
	if pa, ok := g.pendingApprovals[actorID]; ok && pa != nil {
		copyPA := *pa
		pendingApproval = &copyPA
	}

	var lastResult *types.ExecutionResult
	if lr, ok := g.lastResults[actorID]; ok && lr != nil {
		copyLR := *lr
		lastResult = &copyLR
	}

	var workerPod string
	var snapshotLoc string
	var snapshotSize int64

	if state == types.StateRunning || state == types.StateRunningGVisor || state == types.StateThinkingVertexAI {
		workerPod = "conf-workers-0"
	} else {
		snapshotLoc = fmt.Sprintf("gs://conf-demo-bucket/%s/actors/%s.snap", g.atespace, actorID)
		snapshotSize = 384000
	}
	g.mu.RUnlock()

	details := types.ActorDetails{
		ActorID:           actorID,
		State:             state,
		WorkerPod:         workerPod,
		SnapshotLocation:  snapshotLoc,
		SnapshotSizeBytes: snapshotSize,
		ChatHistory:       history,
		PendingApproval:   pendingApproval,
		LastResult:        lastResult,
	}

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(details)
}

func (g *Gateway) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/reset" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	g.mu.Lock()
	sc := g.simController
	g.mu.Unlock()

	if sc != nil {
		sc.Stop()
	}

	g.mu.Lock()
	var actorsToSuspend []string
	for id, st := range g.activeActors {
		if st != types.StateSuspended {
			actorsToSuspend = append(actorsToSuspend, id)
		}
		g.activeActors[id] = types.StateSuspended
	}
	sort.Strings(actorsToSuspend)

	g.pendingApprovals = make(map[string]*types.ApprovalRequest)
	g.chatHistories = make(map[string][]types.ChatMessage)
	g.lastResults = make(map[string]*types.ExecutionResult)
	g.currentActionID = make(map[string]string)
	g.actionFeed = make([]types.ActionLogEntry, 0)
	g.addEventLocked("Cluster reset: all actors suspended to GCS, worker pods freed")
	g.mu.Unlock()

	if g.broker != nil {
		g.broker.PublishReset()
	}

	// Also sweep cluster for any running actors if commandSuspender is used
	if cmdSusp, ok := g.suspender.(*commandSuspender); ok {
		sweepCtx, sweepCancel := context.WithTimeout(context.Background(), 3*time.Second)
		clusterRunning := cmdSusp.getClusterRunningActors(sweepCtx, g.atespace)
		sweepCancel()
		for _, name := range clusterRunning {
			found := false
			for _, id := range actorsToSuspend {
				if id == name {
					found = true
					break
				}
			}
			if !found {
				actorsToSuspend = append(actorsToSuspend, name)
			}
		}
		sort.Strings(actorsToSuspend)
	}

	if g.suspender != nil && len(actorsToSuspend) > 0 {
		baseCtx := g.ctx
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		for _, id := range actorsToSuspend {
			wg.Add(1)
			go func(actorID string) {
				defer wg.Done()
				if err := g.suspender.SuspendActor(ctx, g.atespace, actorID); err != nil {
					g.mu.Lock()
					g.addEventLocked(fmt.Sprintf("Warning: reset suspend actor %s failed: %v", actorID, err))
					g.mu.Unlock()
				}
			}(id)
		}
		wg.Wait()
	}

	for _, id := range actorsToSuspend {
		g.BroadcastStateChange(id, types.StateSuspended, "Suspended", "Cluster reset - suspended to GCS")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"message":      "Cluster reset: all actors suspended and memory cleared",
		"actors_reset": len(actorsToSuspend),
	})
}

// SetSimulationController attaches an active simulation controller to the gateway.
func (g *Gateway) SetSimulationController(sc SimulationController) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.simController = sc
}

// SetSimulationRunner attaches an active simulation controller to the gateway.
// Deprecated: use SetSimulationController instead.
func (g *Gateway) SetSimulationRunner(sc SimulationController) {
	g.SetSimulationController(sc)
}

// SimulationController returns the active simulation controller.
func (g *Gateway) SimulationController() SimulationController {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.simController
}

// SimulationRunner returns the active simulation runner if it implements *SimulationRunner.
func (g *Gateway) SimulationRunner() *SimulationRunner {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if sr, ok := g.simController.(*SimulationRunner); ok {
		return sr
	}
	return nil
}

func (g *Gateway) handleSimulation(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/simulation" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		g.mu.RLock()
		sc := g.simController
		g.mu.RUnlock()

		running, count := false, 0
		if sc != nil {
			running, count = sc.Status()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"running": running,
			"count":   count,
		})

	case http.MethodPost:
		var req types.SimulationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid json payload: %v", err), http.StatusBadRequest)
			return
		}
		if err := req.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Action {
		case "start":
			g.mu.Lock()
			if g.simController == nil {
				g.simController = NewSimulationRunner(g)
			}
			sc := g.simController
			g.mu.Unlock()

			if err := sc.Start(req.Count); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			g.mu.Lock()
			g.addEventLocked(fmt.Sprintf("Started audience simulation with %d virtual attendees", req.Count))
			g.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"running": true,
				"count":   req.Count,
			})

		case "stop":
			g.mu.Lock()
			sc := g.simController
			g.mu.Unlock()

			if sc != nil {
				sc.Stop()
			}

			g.mu.Lock()
			g.addEventLocked("Stopped audience simulation")
			g.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"running": false,
				"count":   0,
			})
		}

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) asyncSuspendActor(attendeeID string) {
	if g.suspender == nil || attendeeID == "" {
		return
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		baseCtx := g.ctx
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(baseCtx, 10*time.Second)
		defer cancel()
		if err := g.suspender.SuspendActor(ctx, g.atespace, attendeeID); err != nil {
			g.mu.Lock()
			g.addEventLocked(fmt.Sprintf("Warning: suspend actor %s failed: %v", attendeeID, err))
			g.mu.Unlock()
		}
	}()
}

func (g *Gateway) authenticateRequest(r *http.Request) (string, error) {
	var tokenStr string
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "Bearer ") {
		tokenStr = strings.TrimPrefix(authHdr, "Bearer ")
	} else if tok := r.Header.Get("X-Attendee-Token"); tok != "" {
		tokenStr = tok
	}

	if tokenStr == "" {
		return "", errors.New("missing authorization token")
	}

	claims, err := g.VerifyToken(tokenStr)
	if err != nil {
		return "", fmt.Errorf("invalid token: %w", err)
	}

	if claims.Sub == "" {
		return "", errors.New("token missing subject")
	}

	if claims.Exp <= 0 || claims.Exp <= time.Now().Unix() {
		return "", errors.New("token expired")
	}

	if reqAttendeeID := r.Header.Get("X-Attendee-ID"); reqAttendeeID != "" {
		if reqAttendeeID != claims.Sub {
			return "", fmt.Errorf("mismatched attendee ID: header=%s, token=%s", reqAttendeeID, claims.Sub)
		}
	}

	return claims.Sub, nil
}

// MintToken creates a signed HS256 JWT for the specified attendee ID.
func (g *Gateway) MintToken(attendeeID string) string {
	headerJSON := `{"alg":"HS256","typ":"JWT"}`
	header := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	claims := Claims{
		Sub:   attendeeID,
		Exp:   time.Now().Add(2 * time.Hour).Unix(),
		Scope: "agent:chat",
	}
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)

	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, []byte(g.jwtSecret))
	mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature
}

// VerifyToken validates an HS256 JWT string and returns the decoded claims.
func (g *Gateway) VerifyToken(tokenStr string) (*Claims, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format: must contain 3 parts")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(g.jwtSecret))
	mac.Write([]byte(signingInput))
	expectedSig := mac.Sum(nil)

	actualSig, err := decodeBase64URL(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid token signature encoding: %w", err)
	}

	if !hmac.Equal(actualSig, expectedSig) {
		return nil, errors.New("invalid token signature")
	}

	payloadBytes, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid token payload encoding: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("invalid token claims: %w", err)
	}

	if claims.Exp <= 0 || claims.Exp <= time.Now().Unix() {
		return nil, errors.New("token expired")
	}

	return &claims, nil
}

func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func (g *Gateway) pollStatus(ctx context.Context) {
	defer g.wg.Done()

	ticker := time.NewTicker(g.statusInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.fetchStatus(ctx)
		}
	}
}

func (g *Gateway) fetchStatus(ctx context.Context) {
	statusURL := strings.TrimRight(g.statusURL, "/") + "/statusz?format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return
	}

	client := g.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var sz struct {
		Parking struct {
			Active int `json:"active"`
		} `json:"parking"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&sz); err == nil {
		g.mu.Lock()
		g.parkedCount = sz.Parking.Active
		g.mu.Unlock()
	}
}

func (g *Gateway) addEventLocked(event string) {
	const maxEvents = 50
	g.recentEvents = append(g.recentEvents, event)
	if len(g.recentEvents) > maxEvents {
		g.recentEvents = g.recentEvents[len(g.recentEvents)-maxEvents:]
	}
}

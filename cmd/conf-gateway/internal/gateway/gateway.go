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

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type attendeeContextKey struct{}

// Suspender defines the interface for suspending actors.
type Suspender interface {
	SuspendActor(ctx context.Context, atespace, actorID string) error
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
	cmd := exec.CommandContext(ctx, "kubectl-ate", "suspend", "actor", actorID, "-a", atespace)
	return cmd.Run()
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

// WithHTTPClient sets the HTTP client used for background polling and upstream requests.
func WithHTTPClient(client *http.Client) Option {
	return func(g *Gateway) {
		if client != nil {
			g.httpClient = client
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

	mux          *http.ServeMux
	proxy        *httputil.ReverseProxy
	attendeeSeq  uint64
	mu           sync.RWMutex
	activeActors map[string]types.ActorLifecycleState
	recentEvents []string
	parkedCount  int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewGateway creates and initializes a new Gateway.
func NewGateway(routerURL, statusURL string, opts ...Option) *Gateway {
	gw := &Gateway{
		routerURL:      routerURL,
		statusURL:      statusURL,
		atespace:       "ate-demo-conf",
		jwtSecret:      "conf-demo-secret-key",
		suspender:      &commandSuspender{},
		statusInterval: 500 * time.Millisecond,
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		mux:            http.NewServeMux(),
		activeActors:   make(map[string]types.ActorLifecycleState),
		recentEvents:   make([]string, 0),
	}

	for _, opt := range opts {
		opt(gw)
	}

	if gw.staticFS == nil {
		gw.staticFS = normalizeStaticFS(resolveStaticFS())
	}

	targetURL, err := url.Parse(gw.routerURL)
	if err != nil {
		log.Printf("Warning: failed to parse routerURL %q: %v", gw.routerURL, err)
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			if targetURL != nil {
				req.URL.Scheme = targetURL.Scheme
				req.URL.Host = targetURL.Host
				req.Host = targetURL.Host
				if targetURL.Path != "" && targetURL.Path != "/" {
					req.URL.Path = singleJoiningSlash(targetURL.Path, req.URL.Path)
				}
			}
			attendeeID, _ := req.Context().Value(attendeeContextKey{}).(string)
			if attendeeID != "" {
				req.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", gw.atespace, attendeeID))
			}
			if gw.geminiAPIKey != "" {
				req.Header.Set("X-Gemini-API-Key", gw.geminiAPIKey)
			}
		},
		ModifyResponse: gw.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			attendeeID, _ := req.Context().Value(attendeeContextKey{}).(string)
			gw.setActorState(attendeeID, types.StateSuspended)
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
	g.mux.HandleFunc("/api/stage/telemetry", g.handleTelemetry)
	g.mux.HandleFunc("/api/chat", g.proxyToActor)
	g.mux.HandleFunc("/api/approve", g.proxyToActor)

	g.mux.HandleFunc("/chat", g.handleChat)
	g.mux.HandleFunc("/chat/", g.handleChat)
	if g.staticFS != nil {
		g.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(g.staticFS))))
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

	seq := atomic.AddUint64(&g.attendeeSeq, 1)
	attendeeID := fmt.Sprintf("attendee-%02d", (seq-1)%100+1)

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
	for k, v := range g.activeActors {
		actorsCopy[k] = v
		if v == types.StateRunning {
			runningActors = append(runningActors, k)
		}
	}
	eventsCopy := make([]string, len(g.recentEvents))
	copy(eventsCopy, g.recentEvents)
	parked := g.parkedCount
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

	g.mu.Lock()
	g.activeActors[attendeeID] = types.StateRunning
	g.addEventLocked(fmt.Sprintf("Actor %s executing %s", attendeeID, r.URL.Path))
	g.mu.Unlock()

	ctx := context.WithValue(r.Context(), attendeeContextKey{}, attendeeID)
	outReq := r.WithContext(ctx)

	g.proxy.ServeHTTP(w, outReq)
}

func (g *Gateway) setActorState(attendeeID string, state types.ActorLifecycleState) {
	if attendeeID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.activeActors[attendeeID] = state
}

// GetActorState returns the lifecycle state of an actor.
func (g *Gateway) GetActorState(attendeeID string) (types.ActorLifecycleState, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	st, ok := g.activeActors[attendeeID]
	return st, ok
}

func (g *Gateway) modifyResponse(resp *http.Response) error {
	attendeeID, _ := resp.Request.Context().Value(attendeeContextKey{}).(string)
	if attendeeID == "" {
		return nil
	}

	if resp.StatusCode >= 400 {
		g.setActorState(attendeeID, types.StateSuspended)
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
	if err := json.Unmarshal(bodyBytes, &chatMsg); err == nil && chatMsg.Approval != nil {
		isWaitingForApproval = true
	}

	if isWaitingForApproval {
		g.mu.Lock()
		g.activeActors[attendeeID] = types.StateAwaitingApproval
		g.addEventLocked(fmt.Sprintf("Actor %s awaiting human approval", attendeeID))
		g.mu.Unlock()

		g.asyncSuspendActor(attendeeID)
	} else if resp.StatusCode == http.StatusOK {
		g.mu.Lock()
		g.activeActors[attendeeID] = types.StateSuspended
		g.addEventLocked(fmt.Sprintf("Actor %s completed turn - suspended to GCS", attendeeID))
		g.mu.Unlock()

		g.asyncSuspendActor(attendeeID)
	}

	return nil
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

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

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
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/feed"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/routerstatus"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/simulation"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

type attendeeContextKey struct{}

const (
	// PresenterCookieName is the cookie a presenter page sets once the
	// presenter key is supplied.
	PresenterCookieName = auth.PresenterCookieName
	// PresenterHeaderName carries the presenter key for scripts.
	PresenterHeaderName = auth.PresenterHeaderName
)

// SimulationController defines the interface for managing background audience simulations.
type SimulationController = simulation.SimulationController

// SimulationRunner manages background virtual attendees interacting with the conference gateway.
type SimulationRunner = simulation.SimulationRunner

// SimulationOption configures SimulationRunner.
type SimulationOption = simulation.SimulationOption

// WithSimulationDelays sets the reading delay and loop delay for virtual attendees.
func WithSimulationDelays(readingDelay, loopDelay time.Duration) SimulationOption {
	return simulation.WithSimulationDelays(readingDelay, loopDelay)
}

// NewSimulationRunner constructs a SimulationRunner bound to the provided Gateway.
func NewSimulationRunner(gw *Gateway, opts ...SimulationOption) *SimulationRunner {
	var t simulation.Target
	if gw != nil {
		t = gw
	}
	return simulation.NewRunner(t, opts...)
}

// Claims represents the decoded JWT payload.
type Claims = auth.Claims

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

// WithSecret sets the HMAC secret that signs attendee tokens and derives actor model keys.
func WithSecret(secret string) Option {
	return func(g *Gateway) {
		if secret != "" {
			g.secret = secret
		}
	}
}

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

// WithSuspendAfter sets how long after a completed turn the gateway suspends
// the actor. Zero suspends it as soon as the turn completes. Without an
// ActorControl the gateway never suspends actors.
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

// WithStatusInterval sets the polling interval for the statusz endpoint.
func WithStatusInterval(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.statusInterval = d
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

// WithPresenterKey sets the key that unlocks the stage, feed, and presenter APIs.
// When unset, NewGateway generates a random key; read it with PresenterKey.
func WithPresenterKey(key string) Option {
	return func(g *Gateway) {
		g.presenterKey = key
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

// WithRouterStatus polls a every statusInterval and reports its Snapshot
// through telemetry. Without this option, telemetry always reports
// parking_available: false.
func WithRouterStatus(a *routerstatus.Aggregator) Option {
	return func(g *Gateway) {
		if a != nil {
			g.routerStatus = a
		}
	}
}

// Gateway manages sessions, reverse proxying to atenet-router, and telemetry aggregation.
type Gateway struct {
	routerURL      string
	routerStatus   *routerstatus.Aggregator
	atespace       string
	secret         string
	signer         *auth.Signer
	presenterKey   string
	statusInterval time.Duration
	httpClient     *http.Client
	staticFS       fs.FS
	maxActors      int
	broker         *feed.Broker
	actions        *feed.Log

	control      actorstate.ActorControl
	board        *actorstate.Board
	pollInterval time.Duration
	suspendAfter time.Duration
	usage        UsageSource
	mockLLM      bool
	statePollMs  atomic.Int64

	mux              *http.ServeMux
	proxy            *httputil.ReverseProxy
	attendeeSeq      uint64
	mu               sync.RWMutex
	pendingApprovals map[string]*types.ApprovalRequest
	// approvalsInFlight holds the approval ID each attendee is currently
	// forwarding, so a second copy of the same decision cannot run concurrently.
	approvalsInFlight map[string]string
	chatHistories     map[string][]types.ChatMessage
	lastResults       map[string]*types.ExecutionResult
	currentActionID   map[string]string
	recentEvents      []string
	parking           atomic.Pointer[routerstatus.Snapshot]
	simController     SimulationController
	// suspendTimers holds each attendee's pending auto-suspend when
	// suspendAfter is positive. Each pending timer holds one wg count.
	suspendTimers map[string]*time.Timer
	// turnsInFlight counts each attendee's proxied turns that have not
	// finished. An actor is only scheduled for suspend when it reaches zero.
	turnsInFlight map[string]int
	// closed is set by Close; no background work starts after it.
	closed bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewGateway creates and initializes a new Gateway.
func NewGateway(routerURL string, opts ...Option) *Gateway {
	gw := &Gateway{
		routerURL:         routerURL,
		atespace:          "ate-demo-conf",
		statusInterval:    500 * time.Millisecond,
		pollInterval:      time.Second,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		broker:            feed.NewBroker(),
		actions:           feed.NewLog(100),
		mux:               http.NewServeMux(),
		pendingApprovals:  make(map[string]*types.ApprovalRequest),
		approvalsInFlight: make(map[string]string),
		chatHistories:     make(map[string][]types.ChatMessage),
		lastResults:       make(map[string]*types.ExecutionResult),
		currentActionID:   make(map[string]string),
		recentEvents:      make([]string, 0),
		suspendTimers:     make(map[string]*time.Timer),
		turnsInFlight:     make(map[string]int),
		maxActors:         50,
	}

	for _, opt := range opts {
		opt(gw)
	}

	if gw.secret == "" {
		gw.secret = randomHex(32)
	}
	gw.signer = auth.NewSigner([]byte(gw.secret))
	if gw.presenterKey == "" {
		gw.presenterKey = randomHex(16)
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
			// Gateway credentials stay here; the actor gets only its own model key.
			for _, h := range []string{"Authorization", "Cookie", PresenterHeaderName, types.ModelKeyHeader} {
				pr.Out.Header.Del(h)
			}
			if attendeeID != "" {
				pr.Out.Header.Set("ate-target-actor", fmt.Sprintf("%s/%s", gw.atespace, attendeeID))
				pr.Out.Header.Set(types.ModelKeyHeader, modelproxy.Key([]byte(gw.secret), gw.atespace, attendeeID))
			}
		},
		ModifyResponse: gw.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			attendeeID, _ := req.Context().Value(attendeeContextKey{}).(string)
			if attendeeID != "" {
				gw.setActivity(attendeeID, types.ActivityIdle)
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

	gw.board = actorstate.NewBoard(gw.publishView)

	ctx, cancel := context.WithCancel(context.Background())
	gw.ctx = ctx
	gw.cancel = cancel
	gw.wg.Add(1)
	go gw.pollStatus(ctx)
	if gw.control != nil {
		gw.wg.Add(1)
		go func() {
			defer gw.wg.Done()
			actorstate.Poll(ctx, gw.control, gw.pollInterval, gw.board, func(d time.Duration, _ error) {
				gw.statePollMs.Store(d.Milliseconds())
			})
		}()
	}

	return gw
}

// Close stops background work, including pending auto-suspends, and waits
// for any suspend already running to finish.
func (g *Gateway) Close() {
	g.mu.Lock()
	sc := g.simController
	g.mu.Unlock()
	if sc != nil {
		sc.Stop()
	}

	g.mu.Lock()
	g.closed = true
	for name, t := range g.suspendTimers {
		if t.Stop() {
			g.wg.Done()
		}
		delete(g.suspendTimers, name)
	}
	g.mu.Unlock()

	g.cancel()
	g.wg.Wait()
}

const localModeReason = "local mode: no ateapi"

// setActivity records activity; in local mode it also marks the state
// unavailable, since there is no ateapi to read it from.
func (g *Gateway) setActivity(attendee string, a types.Activity) {
	g.board.SetActivity(attendee, a)
	if g.control == nil {
		g.board.SetUnavailable(localModeReason)
	}
}

// publishView sends a changed view to that attendee's SSE subscribers. It
// runs inside Board updates, so it must not read the Board.
func (g *Gateway) publishView(v types.ActorView) {
	g.broker.SendToAttendee(v.ActorID, "actor_view", v)
}

// ActorView returns the current view of one attendee's actor.
func (g *Gateway) ActorView(attendee string) (types.ActorView, bool) {
	return g.board.View(attendee)
}

// currentView returns the attendee's view, or a placeholder for an actor
// the gateway has not seen yet.
func (g *Gateway) currentView(attendee string) types.ActorView {
	if v, ok := g.board.View(attendee); ok {
		return v
	}
	if g.control == nil {
		return types.ActorView{ActorID: attendee, State: types.StateUnavailable, StateDetail: localModeReason}
	}
	return types.ActorView{ActorID: attendee, State: types.StateUnknown}
}

// beginTurn marks a turn in flight and cancels any pending suspend, so an
// actor is never suspended while it is serving a turn.
func (g *Gateway) beginTurn(attendee string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turnsInFlight[attendee]++
	if t, ok := g.suspendTimers[attendee]; ok {
		if t.Stop() {
			g.wg.Done()
		}
		delete(g.suspendTimers, attendee)
	}
}

// endTurn marks a turn finished and, once no turn is in flight, schedules
// the actor's suspend.
func (g *Gateway) endTurn(attendee string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turnsInFlight[attendee]--
	if g.turnsInFlight[attendee] > 0 {
		return
	}
	delete(g.turnsInFlight, attendee)
	g.suspendAfterTurnLocked(attendee)
}

// suspendAfterTurnLocked suspends the attendee's actor right away when
// suspendAfter is zero, or after suspendAfter of idleness. g.mu must be held.
func (g *Gateway) suspendAfterTurnLocked(attendee string) {
	if g.control == nil || g.closed {
		return
	}
	g.wg.Add(1)
	if g.suspendAfter <= 0 {
		go func() {
			defer g.wg.Done()
			g.suspendActor(attendee)
		}()
		return
	}
	if t, ok := g.suspendTimers[attendee]; ok && t.Stop() {
		g.wg.Done()
	}
	var t *time.Timer
	t = time.AfterFunc(g.suspendAfter, func() {
		defer g.wg.Done()
		g.mu.Lock()
		if g.suspendTimers[attendee] == t {
			delete(g.suspendTimers, attendee)
		}
		g.mu.Unlock()
		g.suspendActor(attendee)
	})
	g.suspendTimers[attendee] = t
}

func (g *Gateway) suspendActor(attendee string) {
	ctx, cancel := context.WithTimeout(g.ctx, 30*time.Second)
	defer cancel()
	if err := g.control.Suspend(ctx, attendee); err != nil {
		if g.ctx.Err() != nil {
			return // the gateway is closing
		}
		log.Printf("[gateway] auto-suspend %s: %v", attendee, err)
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Warning: suspend actor %s failed: %v", attendee, err))
		g.mu.Unlock()
	}
}

// Context returns the gateway's context, which Close cancels.
func (g *Gateway) Context() context.Context {
	return g.ctx
}

// PresenterKey returns the key that unlocks presenter pages and APIs.
func (g *Gateway) PresenterKey() string {
	return g.presenterKey
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// isPresenter reports whether the request carries the presenter key, either in
// the cookie set by a presenter page or in the PresenterHeaderName header.
func (g *Gateway) isPresenter(r *http.Request) bool {
	return auth.PresenterOK(r, g.presenterKey)
}

func (g *Gateway) requirePresenter(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.isPresenter(r) {
			http.Error(w, "presenter key required", http.StatusUnauthorized)
			return
		}
		next(w, r)
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
	g.mux.HandleFunc("/api/stream", g.requirePresenter(g.handleActionStream))
	g.mux.HandleFunc("/api/feed/history", g.requirePresenter(g.handleFeedHistory))
	g.mux.HandleFunc("/api/actor/", g.requirePresenter(g.handleActorDetails))
	g.mux.HandleFunc("/api/actor", g.requirePresenter(g.handleActorDetails))
	g.mux.HandleFunc("/api/stage/telemetry", g.requirePresenter(g.handleTelemetry))
	g.mux.HandleFunc("/api/reset", g.requirePresenter(g.handleReset))
	g.mux.HandleFunc("/api/simulation", g.requirePresenter(jsonPOST(g.handleSimulation)))
	g.mux.HandleFunc("/api/chat", jsonPOST(g.proxyToActor))
	g.mux.HandleFunc("/api/approve", jsonPOST(g.proxyToActor))

	chat := g.page("chat.html", false, "/chat", "/chat/")
	feedPage := g.page("feed.html", true, "/feed", "/feed/")
	stage := g.page("stage.html", true, "/stage", "/stage/", "/")
	g.mux.HandleFunc("/chat", chat)
	g.mux.HandleFunc("/chat/", chat)
	g.mux.HandleFunc("/feed", feedPage)
	g.mux.HandleFunc("/feed/", feedPage)
	g.mux.HandleFunc("/stage", stage)
	g.mux.HandleFunc("/stage/", stage)
	g.mux.HandleFunc("/", stage)
	if g.staticFS != nil {
		g.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(g.staticFS))))
	}
}

const presenterKeyRequiredPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>Presenter key required</title></head>
<body style="font-family:sans-serif;padding:2rem">
<h1>Presenter key required</h1>
<p>Open this page with <code>?key=&lt;presenter key&gt;</code>. The gateway logs the full URL at startup.</p>
</body></html>
`

// page serves a static HTML file on the given paths. A presenter page first
// requires the presenter key: a correct ?key= query sets the presenter cookie
// and redirects to the same path without the key.
func (g *Gateway) page(file string, presenter bool, paths ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(paths, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if presenter {
			if key := r.URL.Query().Get("key"); key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(g.presenterKey)) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name:     PresenterCookieName,
					Value:    g.presenterKey,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
				return
			}
			if !g.isPresenter(r) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, presenterKeyRequiredPage)
				return
			}
		}
		if g.staticFS == nil {
			http.Error(w, "Static filesystem not configured", http.StatusInternalServerError)
			return
		}
		data, err := fs.ReadFile(g.staticFS, file)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read %s: %v", file, err), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
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

	maxActors := 50
	if g.maxActors > 0 {
		maxActors = g.maxActors
	}

	requestedID := strings.TrimSpace(r.URL.Query().Get("attendee_id"))
	var attendeeID string
	if requestedID != "" && g.isPresenter(r) && auth.ValidAttendeeID(requestedID, maxActors) {
		attendeeID = requestedID
	} else {

		startOffset := 0
		sc := g.SimulationController()

		if sc != nil {
			running, count := sc.Status()
			if running && count > 0 && count < maxActors {
				startOffset = count
			}
		}

		availableRange := maxActors - startOffset
		if availableRange <= 0 {
			availableRange = maxActors
			startOffset = 0
		}

		seq := atomic.AddUint64(&g.attendeeSeq, 1)
		index := startOffset + int((seq-1)%uint64(availableRange)) + 1
		attendeeID = fmt.Sprintf("attendee-%02d", index)
	}

	token := g.MintToken(attendeeID)

	if _, exists := g.board.View(attendeeID); !exists {
		g.setActivity(attendeeID, types.ActivityIdle)
		g.mu.Lock()
		g.addEventLocked(fmt.Sprintf("Provisioned session for %s", attendeeID))
		g.mu.Unlock()
	}

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

	views := g.board.Views()
	names := slices.Sorted(maps.Keys(views))
	var workers []types.WorkerStatus
	seenPods := make(map[string]bool)
	for _, name := range names {
		v := views[name]
		if v.WorkerPod == "" || seenPods[v.WorkerPod] {
			continue
		}
		seenPods[v.WorkerPod] = true
		workers = append(workers, types.WorkerStatus{PodName: v.WorkerPod, ActiveActor: v.ActorID, State: v.State})
	}
	slices.SortFunc(workers, func(a, b types.WorkerStatus) int { return strings.Compare(a.PodName, b.PodName) })

	g.mu.RLock()
	eventsCopy := slices.Clone(g.recentEvents)
	g.mu.RUnlock()

	var parked int
	var parkingOK bool
	if snap := g.parking.Load(); snap != nil {
		parked, parkingOK = snap.Parked, snap.Available
	}

	telem := types.StageTelemetry{
		ParkedRequests:   parked,
		ParkingAvailable: parkingOK,
		Workers:          workers,
		Actors:           views,
		RecentEvents:     eventsCopy,
		MockLLM:          g.mockLLM,
		StatePollMs:      g.statePollMs.Load(),
	}
	if g.usage != nil {
		telem.ModelUsage = g.usage.TotalUsage()
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
	// EventSource cannot set headers, so the token may come in the query.
	if tok := r.URL.Query().Get("token"); tok != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	sub, err := g.authenticateRequest(r)
	if err != nil || sub != attendeeID {
		http.Error(w, "a token for this attendee is required", http.StatusUnauthorized)
		return
	}

	g.broker.ServeAttendee(w, r, attendeeID, func() any { return g.currentView(attendeeID) })
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

	g.broker.ServeActions(w, r)
}

// MaxRequestBodyBytes caps JSON request bodies accepted by the gateway.
const MaxRequestBodyBytes = 64 << 10

// jsonPOST requires POST bodies to be JSON no larger than MaxRequestBodyBytes.
// The body is buffered so downstream handlers and the proxy can re-read it.
func jsonPOST(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next(w, r)
	}
}

// claimApproval binds an approval decision to the approval this attendee was
// last shown. On success it rewrites r.Body to carry the proposed code, never
// code supplied by the client, and marks the approval in flight until release
// is called. The pending approval itself is cleared by modifyResponse once the
// actor answers 200. On failure it writes the error response.
func (g *Gateway) claimApproval(w http.ResponseWriter, r *http.Request, attendeeID string) (types.ApprovalResponse, func(), bool) {
	var req types.ApprovalResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return req, nil, false
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return req, nil, false
	}

	g.mu.Lock()
	pending, ok := g.pendingApprovals[attendeeID]
	if !ok || pending == nil || pending.ApprovalID != req.ApprovalID {
		g.mu.Unlock()
		http.Error(w, "no matching pending approval", http.StatusConflict)
		return req, nil, false
	}
	if g.approvalsInFlight[attendeeID] == req.ApprovalID {
		g.mu.Unlock()
		http.Error(w, "approval already in progress", http.StatusConflict)
		return req, nil, false
	}
	g.approvalsInFlight[attendeeID] = req.ApprovalID
	req.Code = pending.Code
	req.Language = pending.Language
	g.mu.Unlock()

	release := func() {
		g.mu.Lock()
		if g.approvalsInFlight[attendeeID] == req.ApprovalID {
			delete(g.approvalsInFlight, attendeeID)
		}
		g.mu.Unlock()
	}

	body, _ := json.Marshal(req)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return req, release, true
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
					Activity:   types.ActivityThinking,
				})
			}
		}
	} else if r.URL.Path == "/api/approve" {
		apprReq, release, ok := g.claimApproval(w, r, attendeeID)
		if !ok {
			return
		}
		defer release()
		g.mu.RLock()
		actionID := g.currentActionID[attendeeID]
		g.mu.RUnlock()
		if actionID == "" {
			actionID = fmt.Sprintf("act-appr-%s-%d", attendeeID, time.Now().UnixNano())
		}
		entry := types.ActionLogEntry{
			ID:         actionID,
			AttendeeID: attendeeID,
			Timestamp:  time.Now().Unix(),
			ApprovalID: apprReq.ApprovalID,
			Decision:   apprReq.Decision,
			Code:       apprReq.Code,
			Language:   apprReq.Language,
			WorkerPod:  g.currentView(attendeeID).WorkerPod,
		}
		if apprReq.Decision == types.DecisionApprove {
			entry.Activity = types.ActivityExecuting
			g.setActivity(attendeeID, types.ActivityExecuting)
		}
		g.RecordActionEntry(entry)
	}
	if r.URL.Path != "/api/approve" {
		g.setActivity(attendeeID, types.ActivityThinking)
	}
	g.beginTurn(attendeeID)
	defer g.endTurn(attendeeID)

	g.mu.Lock()
	g.addEventLocked(fmt.Sprintf("Actor %s executing %s", attendeeID, r.URL.Path))
	g.mu.Unlock()

	ctx := context.WithValue(r.Context(), attendeeContextKey{}, attendeeID)
	outReq := r.WithContext(ctx)

	g.proxy.ServeHTTP(w, outReq)
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
	entry = g.actions.Record(entry)
	if g.broker != nil {
		g.broker.PublishAction(entry)
	}
}

// GetFeedHistory returns a copy of the current action log entries.
func (g *Gateway) GetFeedHistory() []types.ActionLogEntry {
	return g.actions.History()
}

func (g *Gateway) modifyResponse(resp *http.Response) error {
	resp.Header.Del(types.ModelKeyHeader)
	attendeeID, _ := resp.Request.Context().Value(attendeeContextKey{}).(string)
	if attendeeID == "" {
		return nil
	}

	if resp.StatusCode >= 400 {
		g.setActivity(attendeeID, types.ActivityIdle)
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

	if hasChatMsg {
		g.board.SetSource(attendeeID, chatMsg.Source)
		if isWaitingForApproval {
			g.setActivity(attendeeID, types.ActivityAwaitingApproval)
		} else {
			g.setActivity(attendeeID, types.ActivityIdle)
		}
	} else {
		g.setActivity(attendeeID, types.ActivityIdle)
	}
	workerPod := g.currentView(attendeeID).WorkerPod

	var newExec types.ExecutionResult
	hasNewExec := hasChatMsg && chatMsg.Execution != nil
	if hasNewExec {
		newExec = *chatMsg.Execution
		g.mu.Lock()
		g.lastResults[attendeeID] = &newExec
		g.mu.Unlock()
	}

	g.mu.RLock()
	actionID := g.currentActionID[attendeeID]
	g.mu.RUnlock()
	if actionID == "" {
		actionID = fmt.Sprintf("act-%s-%d", attendeeID, time.Now().UnixNano())
	}

	if isWaitingForApproval {
		g.mu.Lock()
		g.pendingApprovals[attendeeID] = chatMsg.Approval
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], chatMsg)
			}
		}
		g.addEventLocked(fmt.Sprintf("Actor %s awaiting human approval", attendeeID))
		g.mu.Unlock()

		g.RecordActionEntry(types.ActionLogEntry{
			ID:            actionID,
			AttendeeID:    attendeeID,
			Timestamp:     time.Now().Unix(),
			ModelResponse: chatMsg.Content,
			Code:          chatMsg.Approval.Code,
			Language:      chatMsg.Approval.Language,
			RiskLevel:     chatMsg.Approval.RiskLevel,
			ApprovalID:    chatMsg.Approval.ApprovalID,
			WorkerPod:     workerPod,
			Activity:      types.ActivityAwaitingApproval,
			Source:        chatMsg.Source,
		})
	} else if resp.StatusCode == http.StatusOK {
		g.mu.Lock()
		delete(g.pendingApprovals, attendeeID)
		if hasChatMsg && (chatMsg.ID != "" || chatMsg.Content != "") {
			history := g.chatHistories[attendeeID]
			if len(history) == 0 || history[len(history)-1].ID != chatMsg.ID {
				g.chatHistories[attendeeID] = append(g.chatHistories[attendeeID], chatMsg)
			}
		}
		g.addEventLocked(fmt.Sprintf("Actor %s completed turn", attendeeID))
		g.mu.Unlock()

		if hasNewExec {
			entry := types.ActionLogEntry{
				ID:         actionID,
				AttendeeID: attendeeID,
				Timestamp:  time.Now().Unix(),
				Execution:  &newExec,
				WorkerPod:  workerPod,
				Source:     chatMsg.Source,
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
				WorkerPod:     workerPod,
				Source:        chatMsg.Source,
			})
		}
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

	view := g.currentView(actorID)

	g.mu.RLock()
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
	g.mu.RUnlock()

	details := types.ActorDetails{
		ActorView:       view,
		ChatHistory:     history,
		PendingApproval: pendingApproval,
		LastResult:      lastResult,
	}
	if g.usage != nil {
		details.Usage = g.usage.Usage(actorID)
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

	var toSuspend []string
	for name, v := range g.board.Views() {
		if v.State == types.StateRunning || v.State == types.StateResuming {
			toSuspend = append(toSuspend, name)
		}
	}
	slices.Sort(toSuspend)

	g.mu.Lock()
	g.pendingApprovals = make(map[string]*types.ApprovalRequest)
	g.chatHistories = make(map[string][]types.ChatMessage)
	g.lastResults = make(map[string]*types.ExecutionResult)
	g.currentActionID = make(map[string]string)
	g.actions.Reset()
	g.addEventLocked("Stage reset: chats cleared, running actors suspended")
	g.mu.Unlock()

	g.board.Reset()
	if g.usage != nil {
		g.usage.ResetUsage()
	}
	g.broker.Broadcast("reset", struct{}{})

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
					log.Printf("[gateway] reset: suspend %s: %v", name, err)
					failed.Add(1)
					g.mu.Lock()
					g.addEventLocked(fmt.Sprintf("Warning: reset suspend actor %s failed: %v", name, err))
					g.mu.Unlock()
				}
			}()
		}
		wg.Wait()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"actors_reset":   len(toSuspend),
		"suspend_errors": failed.Load(),
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

func (g *Gateway) authenticateRequest(r *http.Request) (string, error) {
	return g.signer.Authenticate(r)
}

// MintToken creates a signed HS256 JWT for the specified attendee ID.
func (g *Gateway) MintToken(attendeeID string) string {
	return g.signer.Mint(attendeeID)
}

// VerifyToken validates an HS256 JWT string and returns the decoded claims.
func (g *Gateway) VerifyToken(tokenStr string) (*Claims, error) {
	return g.signer.Verify(tokenStr)
}

func (g *Gateway) pollStatus(ctx context.Context) {
	defer g.wg.Done()

	if g.routerStatus == nil {
		return
	}

	ticker := time.NewTicker(g.statusInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := g.routerStatus.Fetch(ctx)
			g.parking.Store(&snap)
		}
	}
}

func (g *Gateway) addEventLocked(event string) {
	const maxEvents = 50
	g.recentEvents = append(g.recentEvents, event)
	if len(g.recentEvents) > maxEvents {
		g.recentEvents = g.recentEvents[len(g.recentEvents)-maxEvents:]
	}
}

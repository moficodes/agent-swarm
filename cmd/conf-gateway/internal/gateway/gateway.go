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
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorproxy"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/feed"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/routerstatus"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/web"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const (
	// PresenterCookieName is the cookie a presenter page sets once the
	// presenter key is supplied.
	PresenterCookieName = auth.PresenterCookieName
	// PresenterHeaderName carries the presenter key for scripts.
	PresenterHeaderName = auth.PresenterHeaderName
)

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

// attendeeControl filters ActorControl.List to the gateway's pre-provisioned
// attendee-NN slots so transient actors in the same atespace (such as the
// golden-snapshot builder actor Substrate creates under the ActorTemplate UID
// and then deletes) never enter the stage board or remain stuck as UNKNOWN.
type attendeeControl struct {
	actorstate.ActorControl
	maxActors int
}

func (c attendeeControl) List(ctx context.Context) ([]actorstate.Status, error) {
	statuses, err := c.ActorControl.List(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]actorstate.Status, 0, len(statuses))
	for _, s := range statuses {
		if auth.ValidAttendeeID(s.Name, c.maxActors) {
			filtered = append(filtered, s)
		}
	}
	return filtered, nil
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

// WithSuspendDrainTimeout sets how long Close waits for in-flight actor
// suspends to finish before canceling them, preventing gateway rollouts from
// interrupting ateapi mid-checkpoint and leaving actors in SUSPENDING.
func WithSuspendDrainTimeout(d time.Duration) Option {
	return func(g *Gateway) { g.suspendDrainTimeout = d }
}

// WithStuckSuspendingAfter sets how long an actor may remain in SUSPENDING
// with no local turn or suspend in flight before the gateway re-drives
// Substrate's idempotent SuspendActor workflow. Non-positive disables recovery.
func WithStuckSuspendingAfter(d time.Duration) Option {
	return func(g *Gateway) { g.stuckSuspendingAfter = d }
}

// WithStuckRunningAfter sets how long an actor may remain in RUNNING with no
// local turn, timer, history clear, or suspend in flight before the gateway
// suspends it. Non-positive disables recovery.
func WithStuckRunningAfter(d time.Duration) Option {
	return func(g *Gateway) { g.stuckRunningAfter = d }
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

// WithModel makes the model calls actors hand to the gateway through m.
// Without it every model call is answered with 503.
func WithModel(m actorproxy.ModelCaller) Option {
	return func(g *Gateway) { g.model = m }
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

// WithSeatsPerWorker sets how many actors one worker pod hosts at once.
func WithSeatsPerWorker(n int) Option {
	return func(g *Gateway) {
		if n > 0 {
			g.seatsPerWorker = n
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
			g.staticFS = web.NormalizeStaticFS(staticFS)
		}
	}
}

// WithRouterStatus polls the aggregator a every statusInterval and reports
// its Snapshot through telemetry. Without this option, telemetry always
// reports parking_available: false.
func WithRouterStatus(a *routerstatus.Aggregator) Option {
	return func(g *Gateway) {
		if a != nil {
			g.routerStatus = a
		}
	}
}

// WorkerScaleSource reports the worker pool's size.
type WorkerScaleSource interface {
	Fetch(ctx context.Context) types.WorkerPods
}

// WithWorkerScale polls s every workerScaleInterval and reports the worker
// pool's size through telemetry. Without this option, telemetry reports
// worker_pods.available: false.
func WithWorkerScale(s WorkerScaleSource) Option {
	return func(g *Gateway) { g.workerScale = s }
}

// WithWorkerScaleInterval sets how often the worker pool's size is read.
func WithWorkerScaleInterval(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.workerScaleInterval = d
		}
	}
}

// Gateway manages sessions, reverse proxying to atenet-router, and telemetry aggregation.
type Gateway struct {
	routerURL           string
	routerStatus        *routerstatus.Aggregator
	atespace            string
	secret              string
	signer              *auth.Signer
	presenterKey        string
	googleVerifier      *auth.GoogleVerifier
	chatRateLimitRPM    int
	sessionRateLimitRPM int
	chatLimiter         *rateLimiter
	sessionLimiter      *rateLimiter
	statusInterval      time.Duration
	workerScale         WorkerScaleSource
	workerScaleInterval time.Duration
	httpClient          *http.Client
	staticFS            fs.FS
	maxActors           int
	seatsPerWorker      int
	broker              *feed.Broker
	actions             *feed.Log

	control              actorstate.ActorControl
	board                *actorstate.Board
	pollInterval         time.Duration
	suspendAfter         time.Duration
	suspendDrainTimeout  time.Duration
	stuckSuspendingAfter time.Duration
	stuckRunningAfter    time.Duration
	usage                UsageSource
	model                actorproxy.ModelCaller
	mockLLM              bool
	statePollMs          atomic.Int64

	mux         *http.ServeMux
	proxy       *actorproxy.Proxy
	attendeeSeq uint64
	// mu guards chatHistories through closed. Lock order: mu first, then the locks
	// inside actorproxy (approvals) and feed (Log, Broker) as leaves. Those
	// packages never call back into the gateway while holding their own lock.
	mu              sync.RWMutex
	chatHistories   map[string][]types.ChatMessage
	lastResults     map[string]*types.ExecutionResult
	currentActionID map[string]string
	recentEvents    []string
	parking         atomic.Pointer[routerstatus.Snapshot]
	workerPods      atomic.Pointer[types.WorkerPods]
	simController   SimulationController
	// suspendTimers holds each attendee's pending auto-suspend when
	// suspendAfter is positive. Each pending timer holds one wg count.
	suspendTimers map[string]*time.Timer
	// suspendsInFlight counts active Suspend RPCs per attendee.
	suspendsInFlight map[string]int
	// suspendingSince records when ateapi first reported an actor in SUSPENDING.
	suspendingSince map[string]time.Time
	// runningIdleSince records when ateapi first reported an actor in RUNNING
	// while this gateway had no turn, timer, history clear, or suspend for it.
	runningIdleSince map[string]time.Time
	// turnsInFlight counts each attendee's proxied turns that have not
	// finished. An actor is only scheduled for suspend when it reaches zero.
	turnsInFlight map[string]int
	// turnSeq counts each attendee's turns, so a scheduled suspend can tell
	// that a newer turn has started since it was scheduled.
	turnSeq map[string]uint64
	// beforeSuspendCheck, when set by tests, runs just before a scheduled
	// suspend re-checks for newer turns.
	beforeSuspendCheck func(attendee string)
	// historyClears tracks the actor history clears owed per attendee.
	historyClears map[string]*historyClear
	// waitingForHistoryClear, when set by tests, runs when a caller starts
	// waiting for another's clear to finish.
	waitingForHistoryClear func(attendee string)
	// closed is set by Close; no background work starts after it.
	closed bool

	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	suspendCtx    context.Context
	suspendCancel context.CancelFunc
	suspendWG     sync.WaitGroup
}

// NewGateway creates and initializes a new Gateway.
func NewGateway(routerURL string, opts ...Option) *Gateway {
	gw := &Gateway{
		routerURL:            routerURL,
		atespace:             "ate-demo-conf",
		statusInterval:       500 * time.Millisecond,
		workerScaleInterval:  2 * time.Second,
		pollInterval:         time.Second,
		stuckSuspendingAfter: 20 * time.Second,
		stuckRunningAfter:    20 * time.Second,
		httpClient:           &http.Client{Timeout: 5 * time.Second},
		broker:               feed.NewBroker(),
		actions:              feed.NewLog(100),
		mux:                  http.NewServeMux(),
		chatHistories:        make(map[string][]types.ChatMessage),
		lastResults:          make(map[string]*types.ExecutionResult),
		currentActionID:      make(map[string]string),
		recentEvents:         make([]string, 0),
		suspendTimers:        make(map[string]*time.Timer),
		suspendsInFlight:     make(map[string]int),
		suspendingSince:      make(map[string]time.Time),
		runningIdleSince:     make(map[string]time.Time),
		turnsInFlight:        make(map[string]int),
		turnSeq:              make(map[string]uint64),
		historyClears:        make(map[string]*historyClear),
		maxActors:            50,
		seatsPerWorker:       1,
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
	gw.chatLimiter = newRateLimiter(gw.chatRateLimitRPM, nil)
	gw.sessionLimiter = newRateLimiter(gw.sessionRateLimitRPM, nil)

	if gw.simController == nil {
		gw.simController = NewSimulationRunner(gw)
	}

	if gw.staticFS == nil {
		gw.staticFS = web.NormalizeStaticFS(web.ResolveStaticFS())
	}

	targetURL, err := url.Parse(gw.routerURL)
	if err != nil {
		log.Printf("Warning: failed to parse routerURL %q: %v", gw.routerURL, err)
	}

	var transport http.RoundTripper
	if gw.httpClient != nil && gw.httpClient.Transport != nil {
		transport = gw.httpClient.Transport
	}
	gw.proxy = actorproxy.New(targetURL, gw.atespace, []byte(gw.secret), transport, gw.model, actorproxy.Hooks{
		OnActorError: func(attendeeID string) { gw.setActivity(attendeeID, types.ActivityIdle) },
		OnReply:      gw.onActorReply,
		OnError:      gw.onProxyError,
	})

	gw.routes()

	gw.board = actorstate.NewBoard(gw.publishView)

	ctx, cancel := context.WithCancel(context.Background())
	suspendCtx, suspendCancel := context.WithCancel(context.Background())
	gw.ctx = ctx
	gw.cancel = cancel
	gw.suspendCtx = suspendCtx
	gw.suspendCancel = suspendCancel
	gw.wg.Add(1)
	go gw.pollStatus(ctx)
	if gw.workerScale != nil {
		gw.wg.Add(1)
		go gw.pollWorkerScale(ctx)
	}
	if gw.control != nil {
		gw.control = attendeeControl{ActorControl: gw.control, maxActors: gw.maxActors}
		gw.wg.Add(1)
		go func() {
			defer gw.wg.Done()
			actorstate.Poll(ctx, gw.control, gw.pollInterval, gw.board, func(d time.Duration, err error) {
				gw.statePollMs.Store(d.Milliseconds())
				if err == nil {
					now := time.Now()
					gw.reconcileStuckSuspending(now)
					gw.reconcileStuckRunning(now)
				}
			})
		}()
	}

	return gw
}

// StopSimulation stops any active audience simulation so no new simulated turns
// start while the HTTP server drains.
func (g *Gateway) StopSimulation() {
	g.mu.Lock()
	sc := g.simController
	g.mu.Unlock()
	if sc != nil {
		sc.Stop()
	}
}

// Close stops background work, including pending auto-suspends, and waits
// for any suspend already running to finish.
func (g *Gateway) Close() {
	g.StopSimulation()

	g.mu.Lock()
	g.closed = true
	for name, t := range g.suspendTimers {
		if t.Stop() {
			g.wg.Done()
		}
		delete(g.suspendTimers, name)
	}
	drainTimeout := g.suspendDrainTimeout
	g.mu.Unlock()

	g.cancel()
	if drainTimeout > 0 {
		drained := make(chan struct{})
		go func() {
			g.suspendWG.Wait()
			close(drained)
		}()
		timer := time.NewTimer(drainTimeout)
		select {
		case <-drained:
			timer.Stop()
		case <-timer.C:
		}
	}
	g.suspendCancel()
	g.suspendWG.Wait()
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
	g.mux.HandleFunc("/api/simulation", g.requirePresenter(web.JSONPOST(g.handleSimulation)))
	g.mux.HandleFunc("/api/auth/google", web.JSONPOST(g.handleGoogleAuth))
	g.mux.HandleFunc("/api/auth/logout", g.handleLogout)
	g.mux.HandleFunc("/api/chat", web.JSONPOST(g.proxyToActor))
	g.mux.HandleFunc("/api/approve", web.JSONPOST(g.proxyToActor))

	pages := web.Pages{
		FS:           g.staticFS,
		PresenterKey: g.presenterKey,
		GoogleAuth:   g.googleVerifier.Config(),
	}
	chat := pages.Page("chat.html", false, "/chat", "/chat/")
	feedPage := pages.Page("feed.html", true, "/feed", "/feed/")
	stage := pages.Page("stage.html", true, "/stage", "/stage/")
	g.mux.HandleFunc("/chat", chat)
	g.mux.HandleFunc("/chat/", chat)
	g.mux.HandleFunc("/feed", feedPage)
	g.mux.HandleFunc("/feed/", feedPage)
	g.mux.HandleFunc("/stage", stage)
	g.mux.HandleFunc("/stage/", stage)
	// Attendees who type the bare address land on the chat, not the
	// presenter's key prompt.
	g.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/chat", http.StatusFound)
	})
	if g.staticFS != nil {
		g.mux.Handle("/static/", http.StripPrefix("/static/", web.StaticHandler(g.staticFS)))
	}
}

// MaxRequestBodyBytes caps JSON request bodies accepted by the gateway.
const MaxRequestBodyBytes = web.MaxRequestBodyBytes

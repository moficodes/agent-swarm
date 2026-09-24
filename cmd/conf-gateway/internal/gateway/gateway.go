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

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorproxy"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/feed"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/routerstatus"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/web"
	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
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
			g.staticFS = web.NormalizeStaticFS(staticFS)
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

	mux             *http.ServeMux
	proxy           *actorproxy.Proxy
	attendeeSeq     uint64
	mu              sync.RWMutex
	chatHistories   map[string][]types.ChatMessage
	lastResults     map[string]*types.ExecutionResult
	currentActionID map[string]string
	recentEvents    []string
	parking         atomic.Pointer[routerstatus.Snapshot]
	simController   SimulationController
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
		routerURL:       routerURL,
		atespace:        "ate-demo-conf",
		statusInterval:  500 * time.Millisecond,
		pollInterval:    time.Second,
		httpClient:      &http.Client{Timeout: 5 * time.Second},
		broker:          feed.NewBroker(),
		actions:         feed.NewLog(100),
		mux:             http.NewServeMux(),
		chatHistories:   make(map[string][]types.ChatMessage),
		lastResults:     make(map[string]*types.ExecutionResult),
		currentActionID: make(map[string]string),
		recentEvents:    make([]string, 0),
		suspendTimers:   make(map[string]*time.Timer),
		turnsInFlight:   make(map[string]int),
		maxActors:       50,
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
	gw.proxy = actorproxy.New(targetURL, gw.atespace, []byte(gw.secret), transport, actorproxy.Hooks{
		OnActorError: func(attendeeID string) { gw.setActivity(attendeeID, types.ActivityIdle) },
		OnReply:      gw.onActorReply,
		OnError:      gw.onProxyError,
	})

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
	g.mux.HandleFunc("/api/chat", web.JSONPOST(g.proxyToActor))
	g.mux.HandleFunc("/api/approve", web.JSONPOST(g.proxyToActor))

	pages := web.Pages{FS: g.staticFS, PresenterKey: g.presenterKey}
	chat := pages.Page("chat.html", false, "/chat", "/chat/")
	feedPage := pages.Page("feed.html", true, "/feed", "/feed/")
	stage := pages.Page("stage.html", true, "/stage", "/stage/", "/")
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

// MaxRequestBodyBytes caps JSON request bodies accepted by the gateway.
const MaxRequestBodyBytes = web.MaxRequestBodyBytes

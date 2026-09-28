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

package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2/google"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/routerstatus"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/workerscale"
)

// routerStatusNamespace and routerStatusService identify the atenet-router
// Service whose EndpointSlices are read for router pod addresses.
const (
	routerStatusNamespace = "ate-system"
	routerStatusService   = "atenet-router"
	routerStatusPort      = 4040
)

// workerPoolNamespace and workerPoolName identify the WorkerPool, and the
// HPA of the same name, whose size the stage header shows.
const (
	workerPoolNamespace = "ate-demo-conf"
	workerPoolName      = "conf-workers"
)

//go:embed static/*
var staticFS embed.FS

// Config holds the configuration for the conference edge gateway.
type Config struct {
	Port         string
	RouterURL    string
	StatusURL    string
	HMACSecret   string
	PresenterKey string
	Atespace     string

	// RouterStatusDiscovery selects how router pods are found for parking
	// aggregation: "static" polls StatusURL alone, "endpointslices" discovers
	// every ready atenet-router pod from Kubernetes EndpointSlices.
	RouterStatusDiscovery string

	// ATEAPIEndpoint is passed to kubectl-ate; empty uses its own default.
	ATEAPIEndpoint string
	// KubectlATE is the kubectl-ate binary used to read and suspend actors.
	KubectlATE string
	// StatePollInterval is how often actor state is read from ateapi.
	StatePollInterval time.Duration
	// SuspendAfter is how long after a turn an actor is suspended; zero
	// suspends it as soon as the turn completes.
	SuspendAfter time.Duration
	// MockLLM tells the UI that actors reply without a model.
	MockLLM bool
	// LocalMode runs without ateapi: actor state shows as unavailable and
	// nothing is suspended.
	LocalMode bool

	// VertexProject and VertexLocation identify the Vertex AI endpoint the
	// gateway makes actors' model calls against. An empty VertexProject
	// disables model calls.
	VertexProject  string
	VertexLocation string
	// ModelAllowlist restricts which models actors may call.
	ModelAllowlist []string
	// ActorCount is how many attendee actors the install created.
	ActorCount int
	// ActorsPerWorker is how many actors one worker pod hosts at once.
	ActorsPerWorker int
}

// LoadConfig loads the configuration from environment variables with defaults.
func LoadConfig() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	routerURL := os.Getenv("ROUTER_URL")
	if routerURL == "" {
		routerURL = "http://localhost:8000"
	}
	statusURL := os.Getenv("STATUS_URL")
	if statusURL == "" {
		statusURL = "http://localhost:4040"
	}
	hmacSecret := os.Getenv("HMAC_SECRET")
	presenterKey := os.Getenv("PRESENTER_TOKEN")
	atespace := os.Getenv("ATESPACE")
	if atespace == "" {
		atespace = "ate-demo-conf"
	}
	vertexLocation := os.Getenv("VERTEX_LOCATION")
	if vertexLocation == "" {
		vertexLocation = "us-central1"
	}

	kubectlATE := os.Getenv("KUBECTL_ATE")
	if kubectlATE == "" {
		kubectlATE = "kubectl-ate"
	}
	routerStatusDiscovery := os.Getenv("ROUTER_STATUS_DISCOVERY")
	if routerStatusDiscovery == "" {
		routerStatusDiscovery = "static"
	}

	return Config{
		Port:                  port,
		RouterURL:             routerURL,
		StatusURL:             statusURL,
		HMACSecret:            hmacSecret,
		PresenterKey:          presenterKey,
		Atespace:              atespace,
		RouterStatusDiscovery: routerStatusDiscovery,
		ATEAPIEndpoint:        os.Getenv("ATEAPI_ENDPOINT"),
		KubectlATE:            kubectlATE,
		StatePollInterval:     envDuration("STATE_POLL_INTERVAL", time.Second),
		SuspendAfter:          envDuration("SUSPEND_AFTER", 0),
		MockLLM:               os.Getenv("DEMO_MOCK_LLM") == "true",
		LocalMode:             os.Getenv("LOCAL_MODE") == "true",
		VertexProject:         os.Getenv("VERTEX_PROJECT"),
		VertexLocation:        vertexLocation,
		ModelAllowlist:        splitList(os.Getenv("MODEL_ALLOWLIST"), "gemini-2.5-flash"),
		ActorCount:            envPositiveInt("ACTOR_COUNT", 50),
		ActorsPerWorker:       envPositiveInt("ACTORS_PER_WORKER", 1),
	}
}

// envPositiveInt parses the named variable as a positive integer. It returns
// def when the variable is unset, or, with a log line, when it is invalid.
func envPositiveInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("invalid %s %q; using %d", name, v, def)
		return def
	}
	return n
}

// envDuration parses the named variable as a duration. It returns def when
// the variable is unset, or, with a log line, when it is invalid or negative.
func envDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Printf("invalid %s %q; using %s", name, v, def)
		return def
	}
	return d
}

// splitList splits a comma-separated list, trimming whitespace and dropping
// empty entries. It returns a single-element slice with def when s is empty.
func splitList(s, def string) []string {
	if s == "" {
		return []string{def}
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{def}
	}
	return out
}

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

// newModelProxy builds the model proxy from cfg. An empty VertexProject
// yields a disabled proxy; a non-empty one requires application default
// credentials.
func newModelProxy(ctx context.Context, cfg Config) (*modelproxy.Proxy, error) {
	pc := modelproxy.Config{
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

// newRouterStatusSource builds the router pod source cfg.RouterStatusDiscovery
// selects. "static" polls cfg.StatusURL alone; "endpointslices" discovers
// every ready atenet-router pod from the in-cluster Kubernetes API. Any other
// value is an error.
func newRouterStatusSource(cfg Config) (routerstatus.Source, error) {
	switch cfg.RouterStatusDiscovery {
	case "static":
		return routerstatus.StaticTargets{cfg.StatusURL}, nil
	case "endpointslices":
		restCfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("loading in-cluster config: %w", err)
		}
		client, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("building Kubernetes client: %w", err)
		}
		return routerstatus.NewEndpointSliceTargets(client, routerStatusNamespace, routerStatusService, routerStatusPort), nil
	default:
		return nil, fmt.Errorf("invalid ROUTER_STATUS_DISCOVERY %q; want %q or %q", cfg.RouterStatusDiscovery, "static", "endpointslices")
	}
}

// newWorkerScaleReader builds a reader for the conf-workers pool's size from
// the in-cluster Kubernetes API.
func newWorkerScaleReader() (*workerscale.Reader, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("loading in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building Kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	return workerscale.New(client, dyn, workerPoolNamespace, workerPoolName), nil
}

func main() {
	cfg := LoadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	secret, generated := resolveSecret(cfg.HMACSecret)
	if generated {
		log.Printf("HMAC_SECRET is unset; generated one. Sessions and model keys will not survive a restart.")
	}

	proxy, err := newModelProxy(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if !proxy.Enabled() {
		log.Printf("VERTEX_PROJECT is unset; model calls are disabled and actors reply with the mock engine.")
	}

	routerStatusSrc, err := newRouterStatusSource(cfg)
	if err != nil {
		log.Fatal(err)
	}

	opts := []gateway.Option{
		gateway.WithAtespace(cfg.Atespace),
		gateway.WithSecret(secret),
		gateway.WithPresenterKey(cfg.PresenterKey),
		gateway.WithPollInterval(cfg.StatePollInterval),
		gateway.WithMaxActors(cfg.ActorCount),
		gateway.WithSeatsPerWorker(cfg.ActorsPerWorker),
		gateway.WithSuspendAfter(cfg.SuspendAfter),
		gateway.WithUsage(proxy),
		gateway.WithModel(proxy),
		gateway.WithMockLLM(cfg.MockLLM || !proxy.Enabled()),
		gateway.WithStaticFS(staticFS),
		gateway.WithRouterStatus(routerstatus.NewAggregator(routerStatusSrc, http.DefaultClient)),
	}
	if cfg.LocalMode {
		log.Printf("LOCAL_MODE=true; actor state is not read from ateapi and actors are never suspended.")
	} else {
		opts = append(opts, gateway.WithActorControl(actorstate.NewKubectlATE(cfg.KubectlATE, cfg.ATEAPIEndpoint, cfg.Atespace)))
		if r, err := newWorkerScaleReader(); err != nil {
			log.Printf("Worker pod counts are unavailable: %v", err)
		} else {
			opts = append(opts, gateway.WithWorkerScale(r))
		}
	}

	gw := gateway.NewGateway(cfg.RouterURL, opts...)
	defer gw.Close()
	if cfg.PresenterKey == "" {
		log.Printf("PRESENTER_TOKEN is unset; generated a presenter key. Open /stage?key=%s to unlock the stage and feed.", gw.PresenterKey())
	} else {
		log.Printf("Presenter key set from PRESENTER_TOKEN. Open /stage?key=<PRESENTER_TOKEN> to unlock the stage and feed.")
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("Starting conference edge gateway on :%s (router: %s, statusz: %s, atespace: %s)...", cfg.Port, cfg.RouterURL, cfg.StatusURL, cfg.Atespace)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("gateway server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("Shutting down conference edge gateway...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

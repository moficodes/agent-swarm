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
	"os/exec"
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
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/auth"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/routerstatus"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/routertls"
	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/workerscale"
)

// routerStatusNamespace and routerStatusService identify the atenet-router
// Service whose EndpointSlices are read for router pod addresses. Port 9090 is
// the Prometheus metrics port admitted by ate-system's observability
// NetworkPolicy (whereas /statusz on 4040 is loopback-only in v0.5.0+).
const (
	routerStatusNamespace = "ate-system"
	routerStatusService   = "atenet-router"
	routerStatusPort      = 9090
)

// defaultWorkerPoolName is the default WorkerPool, and HPA of the same name,
// whose size the stage header shows.
const defaultWorkerPoolName = "conf-workers"

//go:embed static/*
var staticFS embed.FS

// Config holds the configuration for the conference edge gateway.
type Config struct {
	Port                string
	MetricsPort         string
	RouterURL           string
	StatusURL           string
	HMACSecret          string
	PresenterKey        string
	Atespace            string
	WorkerPoolNamespace string
	WorkerPoolName      string

	// RouterStatusDiscovery selects how router pods are found for parking
	// aggregation: "static" polls StatusURL alone, "endpointslices" discovers
	// every ready atenet-router pod from Kubernetes EndpointSlices.
	RouterStatusDiscovery string

	// RouterCAFile and RouterCredBundle configure mTLS for in-cluster
	// requests to atenet-router. Both empty uses http.DefaultClient.
	RouterCAFile     string
	RouterCredBundle string

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
	// AllowedGoogleDomains lists Google Workspace domains whose verified users
	// may sign in to presenter pages (/stage and /feed). Empty disables Google Sign-In.
	AllowedGoogleDomains []string
	// GoogleClientID is the OAuth 2.0 Web Client ID for Google Sign-In.
	GoogleClientID string
	// FirebaseAPIKey, FirebaseAuthDomain, and FirebaseProjectID configure
	// Firebase Authentication / Identity Platform popup Google Sign-In.
	FirebaseAPIKey     string
	FirebaseAuthDomain string
	FirebaseProjectID  string
	// ChatRateLimitRPM is the per-attendee rate limit (requests per minute) on
	// POST /api/chat. Zero disables the limit.
	ChatRateLimitRPM int
	// SessionRateLimitRPM is the per-IP rate limit (requests per minute) on
	// GET /api/session for non-presenter requests. Zero disables the limit.
	SessionRateLimitRPM int
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
	workerPoolNamespace := strings.TrimSpace(os.Getenv("WORKER_POOL_NAMESPACE"))
	if workerPoolNamespace == "" {
		workerPoolNamespace = atespace
	}
	workerPoolName := strings.TrimSpace(os.Getenv("WORKER_POOL_NAME"))
	if workerPoolName == "" {
		workerPoolName = defaultWorkerPoolName
	}
	vertexLocation := os.Getenv("VERTEX_LOCATION")
	if vertexLocation == "" {
		vertexLocation = "global"
	}

	kubectlATE := os.Getenv("KUBECTL_ATE")
	if kubectlATE == "" {
		kubectlATE = "kubectl-ate"
	}
	routerStatusDiscovery := os.Getenv("ROUTER_STATUS_DISCOVERY")
	if routerStatusDiscovery == "" {
		routerStatusDiscovery = "static"
	}

	vertexProject := os.Getenv("VERTEX_PROJECT")
	firebaseProjectID := strings.TrimSpace(os.Getenv("FIREBASE_PROJECT_ID"))
	if firebaseProjectID == "" {
		firebaseProjectID = vertexProject
	}

	return Config{
		Port:                  port,
		MetricsPort:           strings.TrimSpace(os.Getenv("METRICS_PORT")),
		RouterURL:             routerURL,
		StatusURL:             statusURL,
		HMACSecret:            hmacSecret,
		PresenterKey:          presenterKey,
		Atespace:              atespace,
		WorkerPoolNamespace:   workerPoolNamespace,
		WorkerPoolName:        workerPoolName,
		RouterStatusDiscovery: routerStatusDiscovery,
		RouterCAFile:          os.Getenv("ROUTER_CA_FILE"),
		RouterCredBundle:      os.Getenv("ROUTER_CRED_BUNDLE"),
		ATEAPIEndpoint:        os.Getenv("ATEAPI_ENDPOINT"),
		KubectlATE:            kubectlATE,
		StatePollInterval:     envDuration("STATE_POLL_INTERVAL", time.Second),
		SuspendAfter:          envDuration("SUSPEND_AFTER", 0),
		MockLLM:               os.Getenv("DEMO_MOCK_LLM") == "true",
		LocalMode:             os.Getenv("LOCAL_MODE") == "true",
		VertexProject:         vertexProject,
		VertexLocation:        vertexLocation,
		ModelAllowlist:        splitList(os.Getenv("MODEL_ALLOWLIST"), "gemini-3.8-flash"),
		ActorCount:            envPositiveInt("ACTOR_COUNT", 50),
		ActorsPerWorker:       envPositiveInt("ACTORS_PER_WORKER", 1),
		AllowedGoogleDomains:  auth.NormalizeDomains([]string{os.Getenv("ALLOWED_GOOGLE_DOMAINS")}),
		GoogleClientID:        strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")),
		FirebaseAPIKey:        strings.TrimSpace(os.Getenv("FIREBASE_API_KEY")),
		FirebaseAuthDomain:    strings.TrimSpace(os.Getenv("FIREBASE_AUTH_DOMAIN")),
		FirebaseProjectID:     firebaseProjectID,
		ChatRateLimitRPM:      envNonNegativeInt("CHAT_RATE_LIMIT_RPM", 10),
		SessionRateLimitRPM:   envNonNegativeInt("SESSION_RATE_LIMIT_RPM", 10),
	}
}

// envNonNegativeInt parses the named variable as a non-negative integer (0
// disables a rate limit). It returns def when the variable is unset or invalid.
func envNonNegativeInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("invalid %s %q; using %d", name, v, def)
		return def
	}
	return n
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

// newRouterHTTPClient builds the HTTP client for atenet-router. When neither
// RouterCAFile nor RouterCredBundle is set (for example in local mode), it
// returns http.DefaultClient; otherwise both must be set for mTLS.
func newRouterHTTPClient(cfg Config) (*http.Client, error) {
	if cfg.RouterCAFile == "" && cfg.RouterCredBundle == "" {
		return http.DefaultClient, nil
	}
	return routertls.NewHTTPClient(cfg.RouterCAFile, cfg.RouterCredBundle)
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

// newActorControl resolves the kubectl-ate binary path and returns an
// ActorControl backed by it. Failing at startup when the binary is absent
// keeps Kubernetes from replacing a healthy gateway ReplicaSet with a broken
// ko-built image that lacks /usr/local/bin/kubectl-ate.
func newActorControl(cfg Config) (*actorstate.KubectlATE, error) {
	resolved, err := exec.LookPath(cfg.KubectlATE)
	if err != nil {
		return nil, fmt.Errorf("finding kubectl-ate binary %q (build conf-gateway with build/gateway.Dockerfile, not ko): %w", cfg.KubectlATE, err)
	}
	return actorstate.NewKubectlATE(resolved, cfg.ATEAPIEndpoint, cfg.Atespace), nil
}

// newWorkerScaleReader builds a reader for the configured worker pool's size
// from the in-cluster Kubernetes API.
func newWorkerScaleReader(cfg Config) (*workerscale.Reader, error) {
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
	return workerscale.New(client, dyn, cfg.WorkerPoolNamespace, cfg.WorkerPoolName), nil
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

	routerHTTPClient, err := newRouterHTTPClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	routerStatusSrc, err := newRouterStatusSource(cfg)
	if err != nil {
		log.Fatal(err)
	}

	opts := []gateway.Option{
		gateway.WithAtespace(cfg.Atespace),
		gateway.WithSecret(secret),
		gateway.WithPresenterKey(cfg.PresenterKey),
		gateway.WithGoogleAuth(auth.GoogleAuthConfig{
			AllowedDomains:     cfg.AllowedGoogleDomains,
			ClientID:           cfg.GoogleClientID,
			FirebaseAPIKey:     cfg.FirebaseAPIKey,
			FirebaseAuthDomain: cfg.FirebaseAuthDomain,
			FirebaseProjectID:  cfg.FirebaseProjectID,
		}),
		gateway.WithChatRateLimitRPM(cfg.ChatRateLimitRPM),
		gateway.WithSessionRateLimitRPM(cfg.SessionRateLimitRPM),
		gateway.WithPollInterval(cfg.StatePollInterval),
		gateway.WithMaxActors(cfg.ActorCount),
		gateway.WithSeatsPerWorker(cfg.ActorsPerWorker),
		gateway.WithSuspendAfter(cfg.SuspendAfter),
		gateway.WithSuspendDrainTimeout(20 * time.Second),
		gateway.WithHTTPClient(routerHTTPClient),
		gateway.WithUsage(proxy),
		gateway.WithModel(proxy),
		gateway.WithMockLLM(cfg.MockLLM || !proxy.Enabled()),
		gateway.WithStaticFS(staticFS),
		gateway.WithRouterStatus(routerstatus.NewAggregator(routerStatusSrc, http.DefaultClient)),
	}
	if cfg.LocalMode {
		log.Printf("LOCAL_MODE=true; actor state is not read from ateapi and actors are never suspended.")
	} else {
		ctrl, err := newActorControl(cfg)
		if err != nil {
			log.Fatal(err)
		}
		opts = append(opts, gateway.WithActorControl(ctrl))
		if r, err := newWorkerScaleReader(cfg); err != nil {
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

	var metricsSrv *http.Server
	if cfg.MetricsPort != "" {
		metricsSrv = &http.Server{
			Addr:              ":" + cfg.MetricsPort,
			Handler:           gw.MetricsHandler(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			log.Printf("Starting gateway Prometheus metrics server on :%s/metrics...", cfg.MetricsPort)
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("metrics server failed: %v", err)
			}
		}()
	}

	go func() {
		log.Printf("Starting conference edge gateway on :%s (router: %s, statusz: %s, atespace: %s)...", cfg.Port, cfg.RouterURL, cfg.StatusURL, cfg.Atespace)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("gateway server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("Shutting down conference edge gateway...")
	gw.StopSimulation()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("metrics server shutdown failed: %v", err)
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

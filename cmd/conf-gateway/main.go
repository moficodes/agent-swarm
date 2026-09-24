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
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/modelproxy"
	"github.com/agent-substrate/substrate/demos/conference/pkg/ai"
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
	GeminiAPIKey string
	Atespace     string
	// SuspendMode selects how actors are suspended after a turn: empty for
	// kubectl-ate, or "noop" for local runs with no cluster.
	SuspendMode string

	// ModelProxyAddr is the listen address for actor model calls.
	ModelProxyAddr string
	// VertexProject and VertexLocation identify the Vertex AI endpoint the
	// model proxy forwards to. An empty VertexProject disables the proxy.
	VertexProject  string
	VertexLocation string
	// ModelAllowlist restricts which models actors may call through the
	// proxy.
	ModelAllowlist []string
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
	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	atespace := os.Getenv("ATESPACE")
	if atespace == "" {
		atespace = "ate-demo-conf"
	}
	modelProxyAddr := os.Getenv("MODEL_PROXY_ADDR")
	if modelProxyAddr == "" {
		modelProxyAddr = ":8091"
	}
	vertexLocation := os.Getenv("VERTEX_LOCATION")
	if vertexLocation == "" {
		vertexLocation = "us-central1"
	}

	return Config{
		Port:           port,
		RouterURL:      routerURL,
		StatusURL:      statusURL,
		HMACSecret:     hmacSecret,
		PresenterKey:   presenterKey,
		GeminiAPIKey:   geminiAPIKey,
		Atespace:       atespace,
		SuspendMode:    os.Getenv("SUSPEND_MODE"),
		ModelProxyAddr: modelProxyAddr,
		VertexProject:  os.Getenv("VERTEX_PROJECT"),
		VertexLocation: vertexLocation,
		ModelAllowlist: splitList(os.Getenv("MODEL_ALLOWLIST"), "gemini-2.5-flash"),
	}
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

// suspenderOptions returns the gateway options for a SUSPEND_MODE value.
func suspenderOptions(mode string) ([]gateway.Option, error) {
	switch mode {
	case "":
		return nil, nil
	case "noop":
		return []gateway.Option{gateway.WithSuspender(gateway.SuspendFunc(func(context.Context, string, string) error {
			return nil
		}))}, nil
	default:
		return nil, fmt.Errorf("unknown SUSPEND_MODE %q (want empty or \"noop\")", mode)
	}
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

func main() {
	cfg := LoadConfig()

	suspendOpts, err := suspenderOptions(cfg.SuspendMode)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	secret, generated := resolveSecret(cfg.HMACSecret)
	if generated {
		log.Printf("HMAC_SECRET is unset; generated one. Sessions and model keys will not survive a restart.")
	}

	proxy, err := newModelProxy(ctx, cfg, []byte(secret))
	if err != nil {
		log.Fatal(err)
	}
	if !proxy.Enabled() {
		log.Printf("VERTEX_PROJECT is unset; the model proxy is disabled and actors must run with DEMO_MOCK_LLM=true.")
	}

	opts := []gateway.Option{
		gateway.WithAtespace(cfg.Atespace),
		gateway.WithJWTSecret(secret),
		gateway.WithPresenterKey(cfg.PresenterKey),
		gateway.WithGeminiAPIKey(cfg.GeminiAPIKey),
		gateway.WithStaticFS(staticFS),
		gateway.WithAIClient(ai.NewGeminiClient()),
	}
	opts = append(opts, suspendOpts...)

	gw := gateway.NewGateway(cfg.RouterURL, cfg.StatusURL, opts...)
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
	modelSrv := &http.Server{
		Addr:              cfg.ModelProxyAddr,
		Handler:           proxy,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
	}

	go func() {
		log.Printf("Starting conference edge gateway on :%s (router: %s, statusz: %s, atespace: %s)...", cfg.Port, cfg.RouterURL, cfg.StatusURL, cfg.Atespace)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("gateway server failed: %v", err)
		}
	}()
	go func() {
		log.Printf("Starting model proxy on %s (project: %s, location: %s)...", cfg.ModelProxyAddr, cfg.VertexProject, cfg.VertexLocation)
		if err := modelSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("model proxy server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("Shutting down conference edge gateway...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
	if err := modelSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("model proxy graceful shutdown failed: %v", err)
	}
}

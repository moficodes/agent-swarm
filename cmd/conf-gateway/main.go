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
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/gateway"
	"github.com/agent-substrate/substrate/demos/conference/pkg/ai"
)

//go:embed static/*
var staticFS embed.FS

// Config holds the configuration for the conference edge gateway.
type Config struct {
	Port         string
	RouterURL    string
	StatusURL    string
	JWTSecret    string
	PresenterKey string
	GeminiAPIKey string
	Atespace     string
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
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "conf-demo-secret-key"
	}
	presenterKey := os.Getenv("PRESENTER_TOKEN")
	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	atespace := os.Getenv("ATESPACE")
	if atespace == "" {
		atespace = "ate-demo-conf"
	}

	return Config{
		Port:         port,
		RouterURL:    routerURL,
		StatusURL:    statusURL,
		JWTSecret:    jwtSecret,
		PresenterKey: presenterKey,
		GeminiAPIKey: geminiAPIKey,
		Atespace:     atespace,
	}
}

func main() {
	cfg := LoadConfig()

	opts := []gateway.Option{
		gateway.WithAtespace(cfg.Atespace),
		gateway.WithJWTSecret(cfg.JWTSecret),
		gateway.WithPresenterKey(cfg.PresenterKey),
		gateway.WithGeminiAPIKey(cfg.GeminiAPIKey),
		gateway.WithStaticFS(staticFS),
		gateway.WithAIClient(ai.NewGeminiClient()),
	}

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

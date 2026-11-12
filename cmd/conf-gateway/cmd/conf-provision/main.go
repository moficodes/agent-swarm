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

// Command conf-provision creates attendee-01..attendee-N actors in parallel
// over persistent gRPC connections to ate-api-server, supporting both single
// local runs and sharded Kubernetes Indexed Jobs for 16,777,216-actor fleets.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/actorstate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var forwardRe = regexp.MustCompile(`Forwarding from (127\.0\.0\.1:\d+)`)

func buildK8sClient(kubeContext string) (kubernetes.Interface, error) {
	if cfg, err := rest.InClusterConfig(); err == nil && kubeContext == "" {
		cfg.QPS = 100
		cfg.Burst = 200
		return kubernetes.NewForConfig(cfg)
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	cfg.QPS = 100
	cfg.Burst = 200
	return kubernetes.NewForConfig(cfg)
}

func startPortForward(ctx context.Context, kubectlBin, kubeContext string) (string, func(), error) {
	var args []string
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	args = append(args, "-n", "ate-system", "port-forward", "--address=127.0.0.1", "svc/api", ":443")
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("starting kubectl port-forward: %w", err)
	}
	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}

	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if m := forwardRe.FindStringSubmatch(line); len(m) == 2 {
				addrCh <- m[1]
				for scanner.Scan() {
				}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		} else {
			errCh <- fmt.Errorf("kubectl port-forward exited without reporting local address")
		}
	}()

	select {
	case addr := <-addrCh:
		return addr, cleanup, nil
	case err := <-errCh:
		cleanup()
		return "", nil, err
	case <-time.After(20 * time.Second):
		cleanup()
		return "", nil, fmt.Errorf("timed out waiting for kubectl port-forward to bind")
	}
}

func resolveRange(count, startFlag, endFlag, shards int) (int, int, error) {
	if count < 1 {
		return 0, 0, fmt.Errorf("count must be >= 1, got %d", count)
	}
	if shards > 0 {
		idxStr := strings.TrimSpace(os.Getenv("JOB_COMPLETION_INDEX"))
		if idxStr == "" {
			return 0, 0, fmt.Errorf("-shards=%d requires JOB_COMPLETION_INDEX environment variable", shards)
		}
		shardIdx, err := strconv.Atoi(idxStr)
		if err != nil || shardIdx < 0 || shardIdx >= shards {
			return 0, 0, fmt.Errorf("invalid JOB_COMPLETION_INDEX %q for shards=%d", idxStr, shards)
		}
		chunk := (count + shards - 1) / shards
		s := shardIdx*chunk + 1
		e := min(count, (shardIdx+1)*chunk)
		return s, e, nil
	}
	s := max(1, startFlag)
	e := count
	if endFlag > 0 && endFlag < e {
		e = endFlag
	}
	return s, e, nil
}

func main() {
	atespace := flag.String("atespace", "ate-demo-conf", "Target Atespace")
	template := flag.String("template", "conf-agent-v1", "ActorTemplate name")
	count := flag.Int("count", 50, "Total number of attendee actors to ensure")
	startFlag := flag.Int("start", 1, "1-based starting actor index (ignored when -shards > 0)")
	endFlag := flag.Int("end", 0, "1-based inclusive ending actor index (0 = -count; ignored when -shards > 0)")
	shards := flag.Int("shards", 0, "Total Indexed Job shards; reads JOB_COMPLETION_INDEX when > 0")
	skipList := flag.Bool("skip-list", false, "Skip upfront ListActors scan and rely on idempotent AlreadyExists")
	concurrency := flag.Int("concurrency", 64, "Number of concurrent gRPC CreateActor workers")
	endpoint := flag.String("endpoint", "", "Optional ateapi gRPC endpoint (starts kubectl port-forward if empty)")
	caFile := flag.String("ca-file", "", "Optional PEM CA bundle for ateapi TLS")
	kubeContext := flag.String("context", os.Getenv("KUBECTL_CONTEXT"), "Optional kubeconfig context")
	kubectlBin := flag.String("kubectl", "kubectl", "kubectl binary for port-forwarding")
	flag.Parse()

	rangeStart, rangeEnd, err := resolveRange(*count, *startFlag, *endFlag, *shards)
	if err != nil {
		log.Fatalf("resolving actor range: %v", err)
	}
	if rangeStart > rangeEnd {
		log.Printf("Shard has empty actor range [%d..%d]; nothing to do.", rangeStart, rangeEnd)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	k8sClient, err := buildK8sClient(*kubeContext)
	if err != nil {
		log.Fatalf("building Kubernetes client: %v", err)
	}

	targetEndpoint := *endpoint
	if targetEndpoint == "" {
		addr, cleanup, err := startPortForward(ctx, *kubectlBin, *kubeContext)
		if err != nil {
			log.Fatalf("port-forward to ate-system/svc/api: %v", err)
		}
		defer cleanup()
		targetEndpoint = addr
	}

	ctrl, err := actorstate.NewGRPCControl(k8sClient, targetEndpoint, *atespace, *caFile)
	if err != nil {
		log.Fatalf("creating gRPC control client: %v", err)
	}
	defer ctrl.Close()

	start := time.Now()
	totalInRange := rangeEnd - rangeStart + 1
	useSkipList := *skipList || *count > actorstate.HyperFleetThreshold || *shards > 0

	var seen map[string]bool
	if !useSkipList {
		existing, err := ctrl.List(ctx)
		if err != nil {
			log.Fatalf("listing existing actors in %s: %v", *atespace, err)
		}
		seen = make(map[string]bool, len(existing))
		for _, s := range existing {
			seen[s.Name] = true
		}
		missingCount := 0
		for i := rangeStart; i <= rangeEnd; i++ {
			if !seen[fmt.Sprintf("attendee-%02d", i)] {
				missingCount++
			}
		}
		if missingCount == 0 {
			log.Printf("All %d actors in range [%d..%d] already exist in %s (checked in %s)",
				totalInRange, rangeStart, rangeEnd, *atespace, time.Since(start).Round(time.Millisecond))
			return
		}
		totalInRange = missingCount
	}

	log.Printf("Provisioning %d actors in range [%d..%d] (of %d total) in %s with concurrency %d (skip-list=%v)...",
		totalInRange, rangeStart, rangeEnd, *count, *atespace, *concurrency, useSkipList)

	jobs := make(chan int, min(4096, totalInRange))
	go func() {
		defer close(jobs)
		for i := rangeStart; i <= rangeEnd; i++ {
			if seen != nil && seen[fmt.Sprintf("attendee-%02d", i)] {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- i:
			}
		}
	}()

	var ensured atomic.Int64
	var failed atomic.Int64
	var wg sync.WaitGroup
	workers := max(1, min(*concurrency, totalInRange))
	logEvery := int64(2000)
	if totalInRange > 100000 {
		logEvery = 25000
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					return
				}
				name := fmt.Sprintf("attendee-%02d", idx)
				var lastErr error
				for attempt := 0; attempt < 6; attempt++ {
					callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
					err := ctrl.CreateActor(callCtx, name, *template)
					cancel()
					if err == nil || status.Code(err) == codes.AlreadyExists {
						lastErr = nil
						break
					}
					lastErr = err
					time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
				}
				if lastErr != nil {
					failed.Add(1)
					log.Printf("failed to create %s: %v", name, lastErr)
					continue
				}
				n := ensured.Add(1)
				if n%logEvery == 0 || int(n) == totalInRange {
					elapsed := time.Since(start)
					rate := float64(n) / max(elapsed.Seconds(), 0.001)
					log.Printf("  ensured %d / %d actors in range [%d..%d] (%.0f actors/s, %s elapsed)",
						n, totalInRange, rangeStart, rangeEnd, rate, elapsed.Round(time.Millisecond))
				}
			}
		}()
	}
	wg.Wait()

	if f := failed.Load(); f > 0 {
		log.Fatalf("provisioning finished with %d failures out of %d actors in range [%d..%d]", f, totalInRange, rangeStart, rangeEnd)
	}
	log.Printf("Successfully ensured %d actors in range [%d..%d] in %s (%s)",
		ensured.Load(), rangeStart, rangeEnd, time.Since(start).Round(time.Millisecond), *atespace)
}

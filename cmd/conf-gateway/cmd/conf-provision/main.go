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
// over a single persistent gRPC connection to ate-api-server.
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

func main() {
	atespace := flag.String("atespace", "ate-demo-conf", "Target Atespace")
	template := flag.String("template", "conf-agent-v1", "ActorTemplate name")
	count := flag.Int("count", 50, "Total number of attendee actors to ensure")
	concurrency := flag.Int("concurrency", 64, "Number of concurrent gRPC CreateActor workers")
	endpoint := flag.String("endpoint", "", "Optional ateapi gRPC endpoint (starts kubectl port-forward if empty)")
	kubeContext := flag.String("context", os.Getenv("KUBECTL_CONTEXT"), "Optional kubeconfig context")
	kubectlBin := flag.String("kubectl", "kubectl", "kubectl binary for port-forwarding")
	flag.Parse()

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

	ctrl, err := actorstate.NewGRPCControl(k8sClient, targetEndpoint, *atespace, "")
	if err != nil {
		log.Fatalf("creating gRPC control client: %v", err)
	}
	defer ctrl.Close()

	start := time.Now()
	existing, err := ctrl.List(ctx)
	if err != nil {
		log.Fatalf("listing existing actors in %s: %v", *atespace, err)
	}
	seen := make(map[string]bool, len(existing))
	for _, s := range existing {
		seen[s.Name] = true
	}

	var missing []string
	for i := 1; i <= *count; i++ {
		name := fmt.Sprintf("attendee-%02d", i)
		if !seen[name] {
			missing = append(missing, name)
		}
	}

	if len(missing) == 0 {
		log.Printf("All %d actors already exist in %s (checked in %s)", *count, *atespace, time.Since(start).Round(time.Millisecond))
		return
	}

	log.Printf("Provisioning %d missing actors (of %d total) in %s with concurrency %d...", len(missing), *count, *atespace, *concurrency)

	jobs := make(chan string, len(missing))
	for _, name := range missing {
		jobs <- name
	}
	close(jobs)

	var created atomic.Int64
	var failed atomic.Int64
	var wg sync.WaitGroup
	workers := min(*concurrency, len(missing))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				if ctx.Err() != nil {
					return
				}
				var lastErr error
				for attempt := 0; attempt < 4; attempt++ {
					callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
					err := ctrl.CreateActor(callCtx, name, *template)
					cancel()
					if err == nil || status.Code(err) == codes.AlreadyExists {
						lastErr = nil
						break
					}
					lastErr = err
					time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
				}
				if lastErr != nil {
					failed.Add(1)
					log.Printf("failed to create %s: %v", name, lastErr)
					continue
				}
				n := created.Add(1)
				if n%2000 == 0 || int(n) == len(missing) {
					log.Printf("  created %d / %d missing actors (%s elapsed)", n, len(missing), time.Since(start).Round(time.Millisecond))
				}
			}
		}()
	}
	wg.Wait()

	if f := failed.Load(); f > 0 {
		log.Fatalf("provisioning finished with %d failures out of %d missing actors", f, len(missing))
	}
	log.Printf("Successfully provisioned %d actors in %s (%d total in %s)", created.Load(), time.Since(start).Round(time.Millisecond), *count, *atespace)
}

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

package routerstatus

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EndpointSliceTargets finds ready router pods from a Service's EndpointSlices.
type EndpointSliceTargets struct {
	client             kubernetes.Interface
	namespace, service string
	port               int
}

// NewEndpointSliceTargets returns a Source for the pods behind service.
func NewEndpointSliceTargets(client kubernetes.Interface, namespace, service string, port int) *EndpointSliceTargets {
	return &EndpointSliceTargets{client: client, namespace: namespace, service: service, port: port}
}

// Targets returns one base URL per ready pod address, deduplicated.
func (e *EndpointSliceTargets) Targets(ctx context.Context) ([]string, error) {
	list, err := e.client.DiscoveryV1().EndpointSlices(e.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + e.service,
	})
	if err != nil {
		return nil, fmt.Errorf("listing endpointslices for %s/%s: %w", e.namespace, e.service, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range list.Items {
		for _, ep := range s.Endpoints {
			if ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
				continue
			}
			for _, addr := range ep.Addresses {
				u := "http://" + net.JoinHostPort(addr, strconv.Itoa(e.port))
				if !seen[u] {
					seen[u] = true
					out = append(out, u)
				}
			}
		}
	}
	return out, nil
}

// EndpointSliceAddrs resolves ready TCP host:port endpoints for a Kubernetes
// Service from its EndpointSlices, caching results briefly and coalescing
// refreshes so high-QPS HTTP dialers can round-robin across pod IPs without
// stampeding or client-side throttling apiserver.
type EndpointSliceAddrs struct {
	client             kubernetes.Interface
	namespace, service string
	portName           string
	fallbackPort       int
	ttl                time.Duration

	mu         sync.Mutex
	cached     []string
	expiresAt  time.Time
	refreshing bool
	waitCh     chan struct{}
}

// NewEndpointSliceAddrs returns a cached TCP endpoint resolver for service.
func NewEndpointSliceAddrs(client kubernetes.Interface, namespace, service, portName string, fallbackPort int) *EndpointSliceAddrs {
	return &EndpointSliceAddrs{
		client:       client,
		namespace:    namespace,
		service:      service,
		portName:     portName,
		fallbackPort: fallbackPort,
		ttl:          10 * time.Second,
	}
}

// Addrs returns deduplicated host:port addresses of ready router pods.
func (e *EndpointSliceAddrs) Addrs(ctx context.Context) ([]string, error) {
	now := time.Now()
	e.mu.Lock()
	if len(e.cached) > 0 {
		cp := append([]string(nil), e.cached...)
		if !now.Before(e.expiresAt) && !e.refreshing {
			e.refreshing = true
			go func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, _ = e.fetchAndStore(bgCtx)
			}()
		}
		e.mu.Unlock()
		return cp, nil
	}
	if e.waitCh != nil {
		ch := e.waitCh
		e.mu.Unlock()
		select {
		case <-ch:
			e.mu.Lock()
			cp := append([]string(nil), e.cached...)
			e.mu.Unlock()
			if len(cp) > 0 {
				return cp, nil
			}
			return nil, fmt.Errorf("endpointslice cache empty for %s/%s", e.namespace, e.service)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ch := make(chan struct{})
	e.waitCh = ch
	e.refreshing = true
	e.mu.Unlock()

	out, err := e.fetchAndStore(ctx)
	e.mu.Lock()
	e.waitCh = nil
	close(ch)
	e.mu.Unlock()
	return out, err
}

func (e *EndpointSliceAddrs) fetchAndStore(ctx context.Context) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	list, err := e.client.DiscoveryV1().EndpointSlices(e.namespace).List(reqCtx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + e.service,
	})
	if err != nil {
		e.mu.Lock()
		e.refreshing = false
		stale := append([]string(nil), e.cached...)
		if len(stale) > 0 {
			e.expiresAt = time.Now().Add(2 * time.Second)
			e.mu.Unlock()
			return stale, nil
		}
		e.mu.Unlock()
		return nil, fmt.Errorf("listing endpointslices for %s/%s: %w", e.namespace, e.service, err)
	}

	seen := map[string]bool{}
	var out []string
	for _, s := range list.Items {
		port := e.fallbackPort
		for _, p := range s.Ports {
			if p.Port == nil {
				continue
			}
			if e.portName != "" && p.Name != nil && *p.Name == e.portName {
				port = int(*p.Port)
				break
			}
			if e.portName == "" {
				port = int(*p.Port)
				break
			}
		}
		for _, ep := range s.Endpoints {
			if ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
				continue
			}
			for _, addr := range ep.Addresses {
				hp := net.JoinHostPort(addr, strconv.Itoa(port))
				if !seen[hp] {
					seen[hp] = true
					out = append(out, hp)
				}
			}
		}
	}

	e.mu.Lock()
	e.cached = append([]string(nil), out...)
	e.expiresAt = time.Now().Add(e.ttl)
	e.refreshing = false
	e.mu.Unlock()
	return out, nil
}

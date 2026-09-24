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

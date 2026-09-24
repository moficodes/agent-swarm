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

package routerstatus_test

import (
	"context"
	"slices"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"github.com/agent-substrate/substrate/demos/conference/cmd/conf-gateway/internal/routerstatus"
)

func slice(name, service string, eps ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: name, Namespace: "ate-system", Labels: map[string]string{discoveryv1.LabelServiceName: service}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   eps,
	}
}

func ep(ip string, ready bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{Addresses: []string{ip}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(ready)}}
}

func TestEndpointSliceTargets_ReadyRouterPodsOnly(t *testing.T) {
	client := fake.NewClientset(
		slice("r-1", "atenet-router", ep("10.0.0.1", true), ep("10.0.0.2", false)),
		slice("r-2", "atenet-router", ep("10.0.0.3", true), ep("10.0.0.1", true)),
		slice("other", "ateapi", ep("10.0.0.9", true)),
	)
	src := routerstatus.NewEndpointSliceTargets(client, "ate-system", "atenet-router", 4040)
	got, err := src.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"http://10.0.0.1:4040", "http://10.0.0.3:4040"}
	if !slices.Equal(got, want) {
		t.Errorf("Targets = %v, want %v", got, want)
	}
}

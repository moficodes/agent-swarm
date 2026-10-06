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

// Package workerscale reads a worker pool's size from its
// HorizontalPodAutoscaler, falling back to the WorkerPool when there is no
// HPA.
package workerscale

import (
	"context"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

var workerPoolGVR = schema.GroupVersionResource{Group: "ate.dev", Version: "v1alpha1", Resource: "workerpools"}

// fetchTimeout bounds one Fetch, so a slow API server cannot stall the
// gateway's poll loop.
const fetchTimeout = 2 * time.Second

// Reader reads one worker pool's size. The HPA and the WorkerPool share the
// pool's name.
type Reader struct {
	client    kubernetes.Interface
	dyn       dynamic.Interface
	namespace string
	name      string
}

// New returns a Reader for the pool name in namespace.
func New(client kubernetes.Interface, dyn dynamic.Interface, namespace, name string) *Reader {
	return &Reader{client: client, dyn: dyn, namespace: namespace, name: name}
}

// Fetch returns the pool's size and non-terminating pod names. Without an HPA,
// Current, Desired and Max all equal the WorkerPool's spec.replicas. Any other
// failure returns a WorkerPods with Available false.
func (r *Reader) Fetch(ctx context.Context) types.WorkerPods {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	hpa, err := r.client.AutoscalingV2().HorizontalPodAutoscalers(r.namespace).Get(ctx, r.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		n, ok := r.poolReplicas(ctx)
		if !ok {
			return types.WorkerPods{}
		}
		return types.WorkerPods{Current: n, Desired: n, Max: n, Available: true, PodNames: r.poolPodNames(ctx)}
	}
	if err != nil {
		return types.WorkerPods{}
	}

	current := int(hpa.Status.CurrentReplicas)
	if current == 0 {
		// A new HPA has no status until its first sync; the pool's own
		// replicas are what is running meanwhile.
		if n, ok := r.poolReplicas(ctx); ok {
			current = n
		}
	}
	desired := int(hpa.Status.DesiredReplicas)
	if desired == 0 {
		desired = current
	}
	return types.WorkerPods{
		Current:   current,
		Desired:   desired,
		Max:       int(hpa.Spec.MaxReplicas),
		Available: true,
		PodNames:  r.poolPodNames(ctx),
	}
}

// poolPodNames lists non-terminating pods belonging to the worker pool.
func (r *Reader) poolPodNames(ctx context.Context) []string {
	list, err := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "ate.dev/worker-pool=" + r.name,
	})
	if err != nil || len(list.Items) == 0 {
		return nil
	}
	var names []string
	for i := range list.Items {
		p := &list.Items[i]
		if p.DeletionTimestamp != nil || p.Name == "" {
			continue
		}
		names = append(names, p.Name)
	}
	slices.Sort(names)
	return names
}

// poolReplicas returns the WorkerPool's spec.replicas.
func (r *Reader) poolReplicas(ctx context.Context) (int, bool) {
	pool, err := r.dyn.Resource(workerPoolGVR).Namespace(r.namespace).Get(ctx, r.name, metav1.GetOptions{})
	if err != nil {
		return 0, false
	}
	n, found, err := unstructured.NestedInt64(pool.Object, "spec", "replicas")
	if err != nil || !found {
		return 0, false
	}
	return int(n), true
}

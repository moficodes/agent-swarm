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

package workerscale_test

import (
	"context"
	"errors"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/agent-substrate/agent-swarm/cmd/conf-gateway/internal/workerscale"
	"github.com/agent-substrate/agent-swarm/pkg/types"
)

const (
	ns   = "ate-demo-conf"
	name = "conf-workers"
)

var poolGVR = schema.GroupVersionResource{Group: "ate.dev", Version: "v1alpha1", Resource: "workerpools"}

func hpa(current, desired, max int32) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: max},
		Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: current, DesiredReplicas: desired},
	}
}

// pool returns a WorkerPool; a negative replicas leaves spec.replicas unset.
func pool(replicas int64) *unstructured.Unstructured {
	spec := map[string]any{}
	if replicas >= 0 {
		spec["replicas"] = replicas
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ate.dev/v1alpha1",
		"kind":       "WorkerPool",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	}}
}

func TestFetch(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "autoscaling", Resource: "horizontalpodautoscalers"}, name, errors.New("denied"))
	tests := []struct {
		name     string
		hpa      *autoscalingv2.HorizontalPodAutoscaler
		pool     *unstructured.Unstructured
		hpaError error
		want     types.WorkerPods
	}{
		{name: "hpa reconciled", hpa: hpa(3, 4, 5), pool: pool(3), want: types.WorkerPods{Current: 3, Desired: 4, Max: 5, Available: true}},
		{name: "hpa steady", hpa: hpa(2, 2, 5), want: types.WorkerPods{Current: 2, Desired: 2, Max: 5, Available: true}},
		{name: "fresh hpa uses pool replicas", hpa: hpa(0, 0, 5), pool: pool(2), want: types.WorkerPods{Current: 2, Desired: 2, Max: 5, Available: true}},
		{name: "fresh hpa without pool", hpa: hpa(0, 0, 5), want: types.WorkerPods{Current: 0, Desired: 0, Max: 5, Available: true}},
		{name: "no hpa falls back to pool", pool: pool(2), want: types.WorkerPods{Current: 2, Desired: 2, Max: 2, Available: true}},
		{name: "no hpa, pool at zero", pool: pool(0), want: types.WorkerPods{Available: true}},
		{name: "no hpa, pool without replicas", pool: pool(-1), want: types.WorkerPods{}},
		{name: "neither exists", want: types.WorkerPods{}},
		{name: "hpa forbidden", pool: pool(2), hpaError: forbidden, want: types.WorkerPods{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			if tt.hpa != nil {
				objs = append(objs, tt.hpa)
			}
			client := fake.NewClientset(objs...)
			if tt.hpaError != nil {
				client.PrependReactor("get", "horizontalpodautoscalers", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.hpaError
				})
			}
			var dynObjs []runtime.Object
			if tt.pool != nil {
				dynObjs = append(dynObjs, tt.pool)
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{poolGVR: "WorkerPoolList"}, dynObjs...)

			got := workerscale.New(client, dyn, ns, name).Fetch(context.Background())
			if got != tt.want {
				t.Errorf("Fetch() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

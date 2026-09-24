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

// Command checkmanifests validates that a multi-document YAML stream on
// stdin parses as Kubernetes objects, without contacting any cluster: every
// non-empty document must decode and carry an apiVersion, a kind and a
// metadata.name. hack/verify-demo-conference-manifests.sh runs it in place
// of "kubectl apply --dry-run=client", which needs a reachable kube context
// for API discovery even in dry-run mode.
package main

import (
	"fmt"
	"io"
	"os"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func main() {
	if err := run(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run decodes every document in r and validates it. It returns an error
// naming the first invalid document, or one reporting an empty stream.
func run(r io.Reader) error {
	decoder := yaml.NewYAMLOrJSONDecoder(r, 4096)
	n := 0
	for {
		raw := map[string]any{}
		switch err := decoder.Decode(&raw); {
		case err == io.EOF:
			if n == 0 {
				return fmt.Errorf("no documents found")
			}
			return nil
		case err != nil:
			return fmt.Errorf("decoding document %d: %w", n+1, err)
		}
		if len(raw) == 0 {
			continue
		}
		n++
		obj := &unstructured.Unstructured{Object: raw}
		if obj.GetAPIVersion() == "" {
			return fmt.Errorf("document %d has no apiVersion", n)
		}
		if obj.GetKind() == "" {
			return fmt.Errorf("document %d has no kind", n)
		}
		if obj.GetName() == "" {
			return fmt.Errorf("document %d (%s) has no metadata.name", n, obj.GetKind())
		}
	}
}

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

package engine_test

import (
	"os"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestActorTemplateGrantsPrivilegeDrop guards the coupling between the
// executor and the actor template: substrate's default capability set lacks
// CHOWN, SETUID, SETGID and DAC_OVERRIDE, and without them the root executor
// can neither hand the workspace to nobody, manage files in it, nor run
// scripts as nobody.
func TestActorTemplateGrantsPrivilegeDrop(t *testing.T) {
	raw, err := os.ReadFile("../../../manifests/actortemplate.yaml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl struct {
		Containers []struct {
			Name            string `json:"name"`
			SecurityContext struct {
				Capabilities struct {
					Add []string `json:"add"`
				} `json:"capabilities"`
			} `json:"securityContext"`
		} `json:"containers"`
	}
	if err := yaml.Unmarshal(raw, &tmpl); err != nil {
		t.Fatal(err)
	}
	for _, c := range tmpl.Containers {
		if c.Name != "conf-workload" {
			continue
		}
		for _, want := range []string{"CHOWN", "SETUID", "SETGID", "DAC_OVERRIDE"} {
			if !slices.Contains(c.SecurityContext.Capabilities.Add, want) {
				t.Errorf("conf-workload securityContext.capabilities.add lacks %s", want)
			}
		}
		return
	}
	t.Fatal("actor template has no conf-workload container")
}

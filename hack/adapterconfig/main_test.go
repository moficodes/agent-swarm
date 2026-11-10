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
	"bytes"
	"strings"
	"testing"
)

func TestRunUpsertAndDelete(t *testing.T) {
	initial := `externalRules:
- seriesQuery: '{__name__="ate_workerpool_workers",ate_workerpool_namespace="ate-demo-conf",ate_workerpool_name="conf-workers"}'
  resources:
    namespaced: false
  name:
    matches: "^ate_workerpool_workers$"
    as: "conf_worker_demand"
  metricsQuery: 'q1'
`
	withLive, err := run(strings.NewReader(initial), []string{
		"upsert",
		"conf_worker_demand_live",
		`{__name__="ate_workerpool_workers",ate_workerpool_namespace="ate-demo-conf-live",ate_workerpool_name="conf-workers-live"}`,
		"q2",
	})
	if err != nil {
		t.Fatalf("upsert live failed: %v", err)
	}
	for _, want := range []string{"conf_worker_demand", "q1", "conf_worker_demand_live", "q2", "conf-workers-live"} {
		if !bytes.Contains(withLive, []byte(want)) {
			t.Errorf("upserted output missing %q:\n%s", want, withLive)
		}
	}

	// Updating an existing metric replaces it in place without duplicating it.
	updatedLive, err := run(bytes.NewReader(withLive), []string{
		"upsert",
		"conf_worker_demand_live",
		`{__name__="ate_workerpool_workers",ate_workerpool_namespace="ate-demo-conf-live",ate_workerpool_name="conf-workers-live"}`,
		"q2_v2",
	})
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}
	if strings.Count(string(updatedLive), "conf_worker_demand_live") != 1 {
		t.Errorf("expected exactly 1 conf_worker_demand_live entry:\n%s", updatedLive)
	}
	if !bytes.Contains(updatedLive, []byte("q2_v2")) || bytes.Contains(updatedLive, []byte("q2\n")) {
		t.Errorf("expected q2 replaced with q2_v2:\n%s", updatedLive)
	}

	// Deleting the live metric leaves the primary metric intact.
	afterDelete, err := run(bytes.NewReader(updatedLive), []string{"delete", "conf_worker_demand_live"})
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if bytes.Contains(afterDelete, []byte("conf_worker_demand_live")) {
		t.Errorf("deleted metric still present:\n%s", afterDelete)
	}
	if !bytes.Contains(afterDelete, []byte("conf_worker_demand")) || !bytes.Contains(afterDelete, []byte("q1")) {
		t.Errorf("primary metric lost on delete:\n%s", afterDelete)
	}
}

func TestRunEmptyInputAndErrors(t *testing.T) {
	out, err := run(strings.NewReader(""), []string{"upsert", "m", "s", "q"})
	if err != nil {
		t.Fatalf("upsert from empty stdin failed: %v", err)
	}
	if !bytes.Contains(out, []byte("as: m")) {
		t.Errorf("unexpected output from empty stdin:\n%s", out)
	}

	for _, bad := range [][]string{
		{},
		{"unknown"},
		{"upsert", "only-one"},
		{"upsert", "", "s", "q"},
		{"delete"},
		{"delete", ""},
	} {
		if _, err := run(strings.NewReader(""), bad); err == nil {
			t.Errorf("expected error for args %v", bad)
		}
	}
}

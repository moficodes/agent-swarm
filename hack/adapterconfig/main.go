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

// Command adapterconfig upserts or deletes an external metric rule in a
// prometheus-adapter config.yaml read from stdin, preserving rules registered
// by other demo namespaces sharing the cluster's external metrics APIService.
package main

import (
	"fmt"
	"io"
	"os"

	"sigs.k8s.io/yaml"
)

type configDoc struct {
	ExternalRules []map[string]any `json:"externalRules"`
}

func main() {
	out, err := run(os.Stdin, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(out)
}

func run(r io.Reader, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: adapterconfig upsert <metric> <seriesQuery> <metricsQuery> | delete <metric>")
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}
	var doc configDoc
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parsing config.yaml: %w", err)
		}
	}

	switch args[0] {
	case "upsert":
		if len(args) != 4 {
			return nil, fmt.Errorf("usage: adapterconfig upsert <metric> <seriesQuery> <metricsQuery>")
		}
		metric, seriesQuery, metricsQuery := args[1], args[2], args[3]
		if metric == "" || seriesQuery == "" || metricsQuery == "" {
			return nil, fmt.Errorf("upsert requires non-empty metric, seriesQuery, and metricsQuery")
		}
		rule := map[string]any{
			"seriesQuery": seriesQuery,
			"resources": map[string]any{
				"namespaced": false,
			},
			"name": map[string]any{
				"matches": "^ate_workerpool_workers$",
				"as":      metric,
			},
			"metricsQuery": metricsQuery,
		}
		replaced := false
		for i, existing := range doc.ExternalRules {
			if ruleMetricName(existing) == metric {
				doc.ExternalRules[i] = rule
				replaced = true
				break
			}
		}
		if !replaced {
			doc.ExternalRules = append(doc.ExternalRules, rule)
		}
	case "delete":
		if len(args) != 2 || args[1] == "" {
			return nil, fmt.Errorf("usage: adapterconfig delete <metric>")
		}
		metric := args[1]
		kept := make([]map[string]any, 0, len(doc.ExternalRules))
		for _, existing := range doc.ExternalRules {
			if ruleMetricName(existing) != metric {
				kept = append(kept, existing)
			}
		}
		doc.ExternalRules = kept
	default:
		return nil, fmt.Errorf("unknown subcommand %q (want upsert or delete)", args[0])
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling config.yaml: %w", err)
	}
	return out, nil
}

func ruleMetricName(rule map[string]any) string {
	nameMap, ok := rule["name"].(map[string]any)
	if !ok {
		return ""
	}
	as, _ := nameMap["as"].(string)
	return as
}

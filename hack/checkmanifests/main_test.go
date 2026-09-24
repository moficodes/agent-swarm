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
	"strings"
	"testing"
)

func TestRunValid(t *testing.T) {
	const stream = `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: conf-gateway
  namespace: ate-demo-conf
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: conf-gateway
  namespace: ate-demo-conf
spec: {}
`
	if err := run(strings.NewReader(stream)); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
}

func TestRunSkipsEmptyDocuments(t *testing.T) {
	const stream = `
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: conf-gateway
---
`
	if err := run(strings.NewReader(stream)); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
}

func TestRunEmptyStream(t *testing.T) {
	if err := run(strings.NewReader("")); err == nil {
		t.Fatal("run() = nil, want an error for an empty stream")
	}
}

func TestRunMissingAPIVersion(t *testing.T) {
	const stream = `
kind: ServiceAccount
metadata:
  name: conf-gateway
`
	err := run(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("run() = %v, want an apiVersion error", err)
	}
}

func TestRunMissingKind(t *testing.T) {
	const stream = `
apiVersion: v1
metadata:
  name: conf-gateway
`
	err := run(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("run() = %v, want a kind error", err)
	}
}

func TestRunMissingName(t *testing.T) {
	const stream = `
apiVersion: v1
kind: ServiceAccount
metadata:
  namespace: ate-demo-conf
`
	err := run(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "metadata.name") {
		t.Fatalf("run() = %v, want a metadata.name error", err)
	}
}

func TestRunMalformedYAML(t *testing.T) {
	const stream = `
apiVersion: v1
kind: ServiceAccount
metadata: [this is not a mapping
`
	if err := run(strings.NewReader(stream)); err == nil {
		t.Fatal("run() = nil, want an error for malformed YAML")
	}
}

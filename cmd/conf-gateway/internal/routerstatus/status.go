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

// Package routerstatus reads parked-request counts from every atenet router
// pod and sums them.
package routerstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Source lists router status base URLs.
type Source interface {
	Targets(ctx context.Context) ([]string, error)
}

// StaticTargets is a fixed list of base URLs.
type StaticTargets []string

// Targets returns the list.
func (s StaticTargets) Targets(context.Context) ([]string, error) { return s, nil }

// Snapshot is one aggregated reading.
type Snapshot struct {
	Parked    int
	Available bool
	Targets   int
	Failed    int
}

// Aggregator fetches statusz from every target in parallel.
type Aggregator struct {
	src    Source
	client *http.Client
}

// NewAggregator returns an Aggregator over src.
func NewAggregator(src Source, client *http.Client) *Aggregator {
	return &Aggregator{src: src, client: client}
}

// Fetch reads every target. It is available if at least one target answered.
func (a *Aggregator) Fetch(ctx context.Context) Snapshot {
	targets, err := a.src.Targets(ctx)
	if err != nil || len(targets) == 0 {
		return Snapshot{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var (
		mu   sync.Mutex
		snap = Snapshot{Targets: len(targets)}
		wg   sync.WaitGroup
	)
	for _, base := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := a.fetchOne(ctx, base)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				snap.Failed++
				return
			}
			snap.Parked += n
		}()
	}
	wg.Wait()
	snap.Available = snap.Failed < snap.Targets
	return snap
}

func (a *Aggregator) fetchOne(ctx context.Context, base string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/statusz?format=json", nil)
	if err != nil {
		return 0, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("statusz %s: %s", base, resp.Status)
	}
	var sz struct {
		Parking struct {
			Active int `json:"active"`
		} `json:"parking"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&sz); err != nil {
		return 0, err
	}
	return sz.Parking.Active, nil
}

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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	if n, ok, err := a.fetchMetrics(ctx, base); ok || err != nil {
		return n, err
	}
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

// fetchMetrics scrapes atenet_router_parking_active from base+"/metrics". It
// returns ok=false with nil error when the target returns 404 so callers can
// fall back to /statusz?format=json.
func (a *Aggregator) fetchMetrics(ctx context.Context, base string) (int, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("metrics %s: %s", base, resp.Status)
	}
	n, err := parseMetricsParked(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

const parkingMetricName = "atenet_router_parking_active"

func parseMetricsParked(r io.Reader) (int, error) {
	sc := bufio.NewScanner(r)
	var (
		total   int
		sawProm bool
	)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# TYPE ") {
				sawProm = true
			}
			continue
		}
		if !strings.HasPrefix(line, parkingMetricName) {
			continue
		}
		rest := line[len(parkingMetricName):]
		if len(rest) == 0 {
			continue
		}
		switch rest[0] {
		case '{':
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				return 0, fmt.Errorf("malformed metric line %q", line)
			}
			rest = rest[end+1:]
		case ' ', '\t':
		default:
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, fmt.Errorf("missing value in metric line %q", line)
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, fmt.Errorf("parsing %s value %q: %w", parkingMetricName, fields[0], err)
		}
		sawProm = true
		total += int(v)
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if !sawProm {
		return 0, fmt.Errorf("metrics response contained no Prometheus exposition data")
	}
	return total, nil
}

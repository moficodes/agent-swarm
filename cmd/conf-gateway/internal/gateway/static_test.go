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

package gateway_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const staticDir = "../../static"

// TestStatic_NoInventedStateLabels guards against invented lifecycle states
// and resource numbers the gateway cannot observe creeping back into the
// static pages.
func TestStatic_NoInventedStateLabels(t *testing.T) {
	banned := []string{
		"RUNNING_GVISOR", "THINKING_VERTEX_AI", "PARKED_ENVOY",
		"Suspended in GCS", "0 MB RAM", "0 vCPU", "Envoy L7 Buffer", "Process frozen",
		"snapshot_location", "snapshot_size_bytes", "current_task", "elapsed_ms",
		"state_change", "conf-workers-0",
	}
	err := filepath.WalkDir(staticDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, s := range banned {
			if strings.Contains(string(b), s) {
				t.Errorf("%s contains %q", path, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestStatic_PagesLoadActorViewFirst checks that every page loads the shared
// actorview.js mapping before its own script, since the page scripts call
// window.ActorView at load time.
func TestStatic_PagesLoadActorViewFirst(t *testing.T) {
	for _, page := range []string{"chat", "feed", "stage"} {
		b, err := os.ReadFile(filepath.Join(staticDir, page+".html"))
		if err != nil {
			t.Fatal(err)
		}
		html := string(b)
		shared := strings.Index(html, `src="/static/actorview.js"`)
		own := strings.Index(html, `src="/static/`+page+`.js"`)
		if shared < 0 || own < 0 || shared > own {
			t.Errorf("%s.html must load actorview.js before %s.js", page, page)
		}
	}
}

// TestStatic_ActorViewIsServed checks that the gateway serves actorview.js
// as static content.
func TestStatic_ActorViewIsServed(t *testing.T) {
	gw := newTestGateway(t, "")
	rec := getPath(gw, "/static/actorview.js")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "window.ActorView") {
		t.Errorf("GET /static/actorview.js = %d", rec.Code)
	}
}

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(staticDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestStatic_StageLegendListsOnlyReachableStates checks that the stage legend
// lists the states the demo produces, and none of the ones it never does.
func TestStatic_StageLegendListsOnlyReachableStates(t *testing.T) {
	html := readStatic(t, "stage.html")
	start := strings.Index(html, `class="fleet-legend"`)
	if start < 0 {
		t.Fatal("stage.html has no fleet legend")
	}
	legend := html[start:]
	legend = legend[:strings.Index(legend, "</div>")]
	for _, want := range []string{"Never started", "Suspended", "Resuming", "Running", "Suspending", "Thinking", "Awaiting approval", "Executing"} {
		if !strings.Contains(legend, want) {
			t.Errorf("legend is missing %q", want)
		}
	}
	for _, unwanted := range []string{"Paused", "Crashed", "Deleting", "Reverting", "State unavailable", "Unknown"} {
		if strings.Contains(legend, unwanted) {
			t.Errorf("legend lists %q, which the demo never produces", unwanted)
		}
	}
}

// TestStatic_StageHasNoFixedBadgeSlots checks that the fleet grid is built
// from telemetry instead of a fixed set of attendee slots.
func TestStatic_StageHasNoFixedBadgeSlots(t *testing.T) {
	if html := readStatic(t, "stage.html"); strings.Contains(html, `id="badge-attendee-`) {
		t.Error("stage.html hard-codes attendee badges")
	}
	if js := readStatic(t, "stage.js"); !strings.Contains(js, "ActorView.fleetIDs(") {
		t.Error("stage.js does not build the fleet grid from ActorView.fleetIDs")
	}
}

// TestStatic_StageStylesEveryBadgeClass checks that stage.css styles each
// class ActorView.badge can return.
func TestStatic_StageStylesEveryBadgeClass(t *testing.T) {
	css := readStatic(t, "stage.css")
	for _, cls := range []string{"never-started", "unknown", "suspended", "resuming", "suspending", "running", "thinking", "awaiting", "executing", "problem"} {
		if !strings.Contains(css, ".fleet-badge."+cls) {
			t.Errorf("stage.css has no .fleet-badge.%s rule", cls)
		}
	}
}

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

// TestStatic_FailedRunsGoThroughActorView checks that every page presents a
// failed run through the shared ActorView.run mapping, and that the phone
// renders the approve reply like a chat reply, so a fix's approval appears.
func TestStatic_FailedRunsGoThroughActorView(t *testing.T) {
	if js := readStatic(t, "actorview.js"); !strings.Contains(js, "run: run") {
		t.Error("actorview.js does not export run")
	}
	for _, page := range []string{"chat.js", "feed.js", "stage.js"} {
		if js := readStatic(t, page); !strings.Contains(js, "ActorView.run(") {
			t.Errorf("%s does not use ActorView.run", page)
		}
	}
	chat := readStatic(t, "chat.js")
	for _, want := range []string{"renderReply(chatMsg)", "renderReply(resultMsg)", "createElement('details')", "What went wrong"} {
		if !strings.Contains(chat, want) {
			t.Errorf("chat.js lacks %q", want)
		}
	}
}

// TestStatic_FailedRunsExplainTheSandboxTimeout checks that ActorView.run
// turns Go's context-deadline stderr into a plain word about the sandbox's
// own 5-second limit, rather than showing attendees the raw Go text.
func TestStatic_FailedRunsExplainTheSandboxTimeout(t *testing.T) {
	js := readStatic(t, "actorview.js")
	for _, want := range []string{"deadline exceeded", "Stopped after the 5 second limit"} {
		if !strings.Contains(js, want) {
			t.Errorf("actorview.js does not handle %q", want)
		}
	}
}

// TestStatic_FailedRunsAreStyled checks that each page styles the classes
// ActorView.run returns.
func TestStatic_FailedRunsAreStyled(t *testing.T) {
	for _, sheet := range []string{"feed.css", "stage.css"} {
		css := readStatic(t, sheet)
		for _, cls := range []string{".term-exit-status.run-corrected", ".term-exit-status.run-failed", ".term-detail"} {
			if !strings.Contains(css, cls) {
				t.Errorf("%s has no %s rule", sheet, cls)
			}
		}
	}
	if css := readStatic(t, "stage.css"); !strings.Contains(css, ".drawer-terminal-box.run-failed") {
		t.Error("stage.css does not style a failed run in the spotlight")
	}
	css := readStatic(t, "chat.css")
	for _, cls := range []string{".run-note", ".run-details"} {
		if !strings.Contains(css, cls) {
			t.Errorf("chat.css has no %s rule", cls)
		}
	}
}

// TestStatic_StageHeaderIsLive checks the header's pod count and ratio come
// from telemetry, not fixed copy.
func TestStatic_StageHeaderIsLive(t *testing.T) {
	html := readStatic(t, "stage.html")
	for _, fixed := range []string{"2 Physical Worker Pods", "50x Oversubscription"} {
		if strings.Contains(html, fixed) {
			t.Errorf("stage.html still has fixed copy %q", fixed)
		}
	}
	for _, id := range []string{"worker-pods-current", "worker-pods-max", "worker-pods-scaling", "oversub-ratio", "oversub-waiting", "worker-pods-seats", "workspace-mode", "workspace-bound", "spotlight-volume"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("stage.html lacks #%s", id)
		}
	}
	js := readStatic(t, "stage.js")
	for _, want := range []string{"data.worker_pods", "data.actor_count", "p.current > 0", "data.seats_per_worker", "p.current * seatsPerWorker", "w.actors", "ActorView.fleetVolumes(", "ActorView.volume("} {
		if !strings.Contains(js, want) {
			t.Errorf("stage.js lacks %q", want)
		}
	}
}

func TestStatic_StageWorkerSectionScales(t *testing.T) {
	html := readStatic(t, "stage.html")
	if strings.Contains(html, "2 Replicas") {
		t.Error(`stage.html still has fixed copy "2 Replicas"`)
	}
	if !strings.Contains(html, `id="worker-pool-replicas"`) {
		t.Error("stage.html lacks #worker-pool-replicas")
	}
	js := readStatic(t, "stage.js")
	for _, want := range []string{"workerPoolReplicas", "gridTemplateColumns", "'compact'"} {
		if !strings.Contains(js, want) {
			t.Errorf("stage.js lacks %q", want)
		}
	}
	css := readStatic(t, "stage.css")
	if !strings.Contains(css, ".workers-grid.compact") {
		t.Error("stage.css lacks .workers-grid.compact")
	}
}

// TestStatic_StageWorkerSectionAlwaysVisible checks that the worker pool
// section is always rendered with full worker cards (never collapsing to an
// empty-state line when no actors are running).
func TestStatic_StageWorkerSectionAlwaysVisible(t *testing.T) {
	html := readStatic(t, "stage.html")
	if strings.Contains(html, "No actors running") || strings.Contains(html, "workers-empty") {
		t.Error("stage.html still contains empty-state placeholder for worker pool")
	}
	if !strings.Contains(html, `class="card worker-card"`) || !strings.Contains(html, `class="worker-seat empty"`) {
		t.Error("stage.html does not pre-render initial idle worker cards")
	}
	js := readStatic(t, "stage.js")
	if strings.Contains(js, "No actors running") || strings.Contains(js, "workers-empty") {
		t.Error("stage.js still collapses worker pool when no actors are running")
	}
	if !strings.Contains(js, "updateWorkerCards(data.workers || [], seats, data.worker_pods)") {
		t.Error("stage.js does not pass data.worker_pods to updateWorkerCards")
	}
}

// TestStatic_LightModeTogglePresentAndStyled checks that every page includes
// the top-right icon-only Projector Light Mode segmented toggle (Moon for dark,
// Sun for light) and CSS token overrides for [data-theme="light"].
func TestStatic_LightModeTogglePresentAndStyled(t *testing.T) {
	for _, page := range []string{"stage.html", "feed.html", "chat.html"} {
		html := readStatic(t, page)
		for _, id := range []string{`id="btn-theme-toggle"`, `id="theme-seg-dark"`, `id="theme-seg-light"`} {
			if !strings.Contains(html, id) {
				t.Errorf("%s lacks %s", page, id)
			}
		}
	}
	stageHTML := readStatic(t, "stage.html")
	controlsIdx := strings.Index(stageHTML, `class="header-controls"`)
	toggleIdx := strings.Index(stageHTML, `id="btn-theme-toggle"`)
	specsIdx := strings.Index(stageHTML, `class="header-specs"`)
	if controlsIdx < 0 || toggleIdx < 0 || specsIdx < 0 || !(controlsIdx < toggleIdx && toggleIdx < specsIdx) {
		t.Errorf("stage.html must place #btn-theme-toggle inside .header-controls (top-right row) before .header-specs")
	}
	for _, sheet := range []string{"stage.css", "feed.css", "chat.css"} {
		css := readStatic(t, sheet)
		for _, want := range []string{`[data-theme="light"]`, `.theme-toggle`, `.theme-seg.active`} {
			if !strings.Contains(css, want) {
				t.Errorf("%s lacks %s", sheet, want)
			}
		}
	}
	js := readStatic(t, "actorview.js")
	for _, want := range []string{"conf_theme", "getTheme", "setTheme", "toggleTheme"} {
		if !strings.Contains(js, want) {
			t.Errorf("actorview.js lacks %q", want)
		}
	}
}

// TestStatic_SubstrateLogoPresentAndServed checks that every page displays the
// Agent Substrate logo at the top-left and that the gateway serves ate-logo.svg.
func TestStatic_SubstrateLogoPresentAndServed(t *testing.T) {
	for _, page := range []string{"stage.html", "feed.html", "chat.html"} {
		html := readStatic(t, page)
		if !strings.Contains(html, `src="/static/ate-logo.svg"`) {
			t.Errorf("%s does not reference /static/ate-logo.svg", page)
		}
	}
	gw := newTestGateway(t, "")
	rec := getPath(gw, "/static/ate-logo.svg")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("GET /static/ate-logo.svg = %d", rec.Code)
	}
}

// TestStatic_ChatFilestoreChipHiddenUnlessFilestoreEnabled checks that /chat
// starts the Filestore starter prompt chip hidden and reveals it only when the
// attendee's ActorView reports a Filestore external volume.
func TestStatic_ChatFilestoreChipHiddenUnlessFilestoreEnabled(t *testing.T) {
	html := readStatic(t, "chat.html")
	if !strings.Contains(html, `<button id="chip-filestore" type="button" class="chip" hidden `) {
		t.Error("chat.html must render #chip-filestore with the hidden attribute by default")
	}
	js := readStatic(t, "chat.js")
	for _, want := range []string{`getElementById('chip-filestore')`, `el.filestoreChip.hidden = !window.ActorView.volume(view).filestore`} {
		if !strings.Contains(js, want) {
			t.Errorf("chat.js lacks %q", want)
		}
	}
	css := readStatic(t, "chat.css")
	if !strings.Contains(css, "[hidden]") || !strings.Contains(css, "display: none !important") {
		t.Error("chat.css must keep [hidden] hidden over .chip's display: inline-flex")
	}
}

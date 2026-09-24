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

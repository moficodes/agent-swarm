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
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// runActorView calls window.ActorView[fn] on each arg under node and returns
// the JSON-decoded results. It skips the test when node is not installed.
func runActorView(t *testing.T, fn string, args []any, out any) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script, err := filepath.Abs(filepath.Join(staticDir, "actorview.js"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	const driver = `
globalThis.window = {};
require(process.argv[1]);
const fn = window.ActorView[process.argv[2]];
const args = JSON.parse(process.argv[3]);
process.stdout.write(JSON.stringify(args.map((a) => fn(a))));
`
	b, err := exec.Command(node, "-e", driver, script, fn, string(in)).Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decoding %s: %v", b, err)
	}
}

type badge struct {
	Label string `json:"label"`
	Sub   string `json:"sub"`
	Cls   string `json:"cls"`
}

func TestActorView_BadgeLeadsWithActivity(t *testing.T) {
	cases := []struct {
		name string
		view map[string]any
		want badge
	}{
		{
			name: "awaiting approval while suspended",
			view: map[string]any{"state": "SUSPENDED", "activity": "AWAITING_APPROVAL"},
			want: badge{Label: "Awaiting approval", Sub: "Suspended", Cls: "awaiting"},
		},
		{
			name: "thinking while running",
			view: map[string]any{"state": "RUNNING", "worker_pod": "w-1", "activity": "THINKING"},
			want: badge{Label: "Thinking", Sub: "Running", Cls: "thinking"},
		},
		{
			name: "executing",
			view: map[string]any{"state": "RUNNING", "activity": "EXECUTING"},
			want: badge{Label: "Executing", Sub: "Running", Cls: "executing"},
		},
		{
			name: "thinking before the first snapshot",
			view: map[string]any{"state": "SUSPENDED", "never_started": true, "activity": "THINKING"},
			want: badge{Label: "Thinking", Sub: "Never started", Cls: "thinking"},
		},
		{
			name: "idle shows the substrate state",
			view: map[string]any{"state": "SUSPENDED"},
			want: badge{Label: "Suspended", Sub: "Snapshot saved", Cls: "suspended"},
		},
		{
			name: "a crash outranks activity",
			view: map[string]any{"state": "CRASHED", "state_detail": "exit 2", "activity": "THINKING"},
			want: badge{Label: "Crashed", Sub: "exit 2", Cls: "problem"},
		},
		{
			name: "unreadable state is a problem",
			view: map[string]any{"state": "UNAVAILABLE", "state_detail": "ateapi unreachable"},
			want: badge{Label: "State unavailable", Sub: "ateapi unreachable", Cls: "problem"},
		},
	}
	args := make([]any, len(cases))
	for i, c := range cases {
		args[i] = c.view
	}
	var got []badge
	runActorView(t, "badge", args, &got)
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("%s: badge = %+v, want %+v", c.name, got[i], c.want)
		}
	}
}

func TestActorView_FleetIDsAreTheReportedActorsInOrder(t *testing.T) {
	actors := map[string]any{"attendee-10": map[string]any{}, "attendee-02": map[string]any{}, "attendee-01": map[string]any{}}
	var got [][]string
	runActorView(t, "fleetIDs", []any{actors, nil}, &got)
	if want := []string{"attendee-01", "attendee-02", "attendee-10"}; !reflect.DeepEqual(got[0], want) {
		t.Errorf("fleetIDs = %v, want %v", got[0], want)
	}
	if len(got[1]) != 0 {
		t.Errorf("fleetIDs(null) = %v, want none", got[1])
	}
}

type volumeBadge struct {
	Label      string `json:"label"`
	Sub        string `json:"sub"`
	URI        string `json:"uri"`
	Bound      bool   `json:"bound"`
	Configured bool   `json:"configured"`
}

type fleetVolumeSummary struct {
	Known      bool   `json:"known"`
	Configured bool   `json:"configured"`
	Label      string `json:"label"`
	Sub        string `json:"sub"`
	Bound      int    `json:"bound"`
	Total      int    `json:"total"`
}

func TestActorView_VolumeAndFleetVolumes(t *testing.T) {
	volCases := []struct {
		name string
		view map[string]any
		want volumeBadge
	}{
		{
			name: "bound filestore share",
			view: map[string]any{
				"state":        "RUNNING",
				"volume_type":  "filestore.csi.storage.gke.io",
				"volume_share": "192.0.2.10:/vol1",
				"volume_uri":   "volumepool://proj/us-west1/demo-pool/volumes/v1?ip=192.0.2.10",
			},
			want: volumeBadge{
				Label:      "192.0.2.10:/vol1",
				Sub:        "/workspace",
				URI:        "volumepool://proj/us-west1/demo-pool/volumes/v1?ip=192.0.2.10",
				Bound:      true,
				Configured: true,
			},
		},
		{
			name: "configured external volume before first wake",
			view: map[string]any{
				"state":         "SUSPENDED",
				"never_started": true,
				"volume_type":   "filestore.csi.storage.gke.io",
			},
			want: volumeBadge{
				Label:      "Provisioned on first wake",
				Sub:        "filestore.csi.storage.gke.io",
				URI:        "filestore.csi.storage.gke.io",
				Bound:      false,
				Configured: true,
			},
		},
		{
			name: "container rootfs default",
			view: map[string]any{"state": "SUSPENDED"},
			want: volumeBadge{
				Label:      "Container rootfs",
				Sub:        "/tmp/workspace",
				URI:        "",
				Bound:      false,
				Configured: false,
			},
		},
	}
	volArgs := make([]any, len(volCases))
	for i, c := range volCases {
		volArgs[i] = c.view
	}
	var gotVols []volumeBadge
	runActorView(t, "volume", volArgs, &gotVols)
	for i, c := range volCases {
		if gotVols[i] != c.want {
			t.Errorf("%s: volume = %+v, want %+v", c.name, gotVols[i], c.want)
		}
	}

	fleetCases := []struct {
		name   string
		actors map[string]any
		want   fleetVolumeSummary
	}{
		{
			name:   "empty fleet",
			actors: map[string]any{},
			want:   fleetVolumeSummary{Known: false, Configured: false, Label: "--", Sub: "", Bound: 0, Total: 0},
		},
		{
			name: "rootfs fleet",
			actors: map[string]any{
				"attendee-01": map[string]any{"state": "SUSPENDED"},
				"attendee-02": map[string]any{"state": "RUNNING"},
			},
			want: fleetVolumeSummary{
				Known:      true,
				Configured: false,
				Label:      "Container rootfs",
				Sub:        "/tmp/workspace",
				Bound:      0,
				Total:      2,
			},
		},
		{
			name: "filestore fleet with partial bindings",
			actors: map[string]any{
				"attendee-01": map[string]any{
					"state":        "RUNNING",
					"volume_type":  "filestore.csi.storage.gke.io",
					"volume_share": "192.0.2.10:/vol1",
				},
				"attendee-02": map[string]any{
					"state":       "SUSPENDED",
					"volume_type": "filestore.csi.storage.gke.io",
				},
			},
			want: fleetVolumeSummary{
				Known:      true,
				Configured: true,
				Label:      "Filestore Agent Volumes",
				Sub:        "1 of 2 /workspace shares bound",
				Bound:      1,
				Total:      2,
			},
		},
	}
	fleetArgs := make([]any, len(fleetCases))
	for i, c := range fleetCases {
		fleetArgs[i] = c.actors
	}
	var gotFleet []fleetVolumeSummary
	runActorView(t, "fleetVolumes", fleetArgs, &gotFleet)
	for i, c := range fleetCases {
		if gotFleet[i] != c.want {
			t.Errorf("%s: fleetVolumes = %+v, want %+v", c.name, gotFleet[i], c.want)
		}
	}
}

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

// Command conf-localrouter runs conference actors in-process behind a
// router that mimics atenet's routing header and statusz, for local runs.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-substrate/substrate/demos/conference/internal/localactors"
	"github.com/agent-substrate/substrate/demos/conference/internal/workload/server"
)

func main() {
	addr := flag.String("addr", ":18080", "listen address")
	atespace := flag.String("atespace", "ate-demo-conf", "atespace in the ate-target-actor header")
	slots := flag.Int("slots", 2, "requests served at once; the rest are parked (0 = unlimited)")
	root := flag.String("workspace-root", filepath.Join(os.TempDir(), "conf-localrouter"), "parent of each actor's sandbox workspace")
	flag.Parse()

	r := localactors.New(*atespace, func(name string) http.Handler {
		return server.NewWorkloadServer(server.WithWorkspaceDir(filepath.Join(*root, name))).Handler()
	}, localactors.WithSlots(*slots))

	srv := &http.Server{Addr: *addr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("conf-localrouter listening on %s (atespace %s, %d slots)", *addr, *atespace, *slots)
	log.Fatal(srv.ListenAndServe())
}

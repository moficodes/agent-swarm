<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Splitting the conference demo into agent-swarm

## Goal

Move `demos/conference` out of the substrate monorepo into its own
repository, `agent-swarm`. The new repository must build, test, run locally
and deploy to a cluster that already runs Substrate, without a substrate
checkout. It keeps the demo's git history.

The structure borrows from
[always-on-agent](https://github.com/agent-substrate/always-on-agent): its
own manifests and deploy script, Substrate as a prerequisite, and CI that
needs no cluster.

## Decisions

| Question | Decision |
|---|---|
| Where the repo lives | Local only, at `~/Documents/github.com/agent-substrate/agent-swarm`. The user creates the GitHub repo and pushes. |
| Name and module | `agent-swarm`, module `github.com/agent-substrate/agent-swarm` |
| History | Kept, rewritten with `git filter-repo` |
| Substrate dependency | Installing Substrate is a prerequisite. agent-swarm pins one substrate commit. |

## Non-goals

- Changing the substrate repository. Removing `demos/conference` and its
  `hack/*demo-conference*` scripts from substrate is a later, separate pull
  request.
- Creating the GitHub repository or pushing anything.
- Moving `demos/swarm` (the red/blue-team demo), `reports/`, or
  `vendor/`.
- The open step-4 items (the state poll measurement, fixture recapture and
  core bug write-ups). They continue in agent-swarm afterwards.

## Layout

```
agent-swarm/
  cmd/
    conf-gateway/        # from demos/conference/cmd/conf-gateway
    conf-workload/
    conf-localrouter/
  internal/              # from demos/conference/internal
  pkg/types/             # from demos/conference/pkg/types
  manifests/
    gateway.yaml.tmpl
    gateway-ingress.yaml.tmpl
    workerpool.yaml.tmpl        # from demos/conference/conference.yaml.tmpl
    actortemplate.yaml.tmpl     # from demos/conference/conference-template.yaml.tmpl
  build/
    gateway.Dockerfile          # from demos/conference/Dockerfile
    gateway.Dockerfile.dockerignore
  hack/
    lib.sh                      # helpers ported from substrate's install-ate.sh
    deploy.sh                   # from hack/install-demo-conference.sh
    test-deploy.sh              # from hack/test-install-demo-conference.sh
    verify.sh                   # from hack/verify-demo-conference.sh
    verify-manifests.sh         # from hack/verify-demo-conference-manifests.sh
    checkmanifests/             # from demos/conference/hack/checkmanifests
    boilerplate/
  docs/superpowers/{specs,plans}/   # the conference specs and plans
  .github/workflows/ci.yaml
  .golangci.yaml
  .ko.yaml
  go.mod, go.sum
  Makefile
  README.md                     # from demos/conference/README.md
  substrate-gaps.md             # Substrate limitations the demo works around
  CONTRIBUTING.md, LICENSE
```

The binary names stay the same, so image names, manifests and the running
deployment on substrate-poc do not need renaming.

## History

1. Install `git-filter-repo`. It is not installed yet.
2. Clone the `docs/demo-ideas` branch into a scratch directory. The
   substrate worktree is never rewritten.
3. Keep only these paths, renamed to the layout above:
   - `demos/conference/**`
   - `hack/install-demo-conference.sh`, `hack/test-install-demo-conference.sh`,
     `hack/verify-demo-conference.sh`, `hack/verify-demo-conference-manifests.sh`
   - the specs and plans dated 2026-09-20 through 2026-09-23 whose names
     contain `conference`, and this spec
4. Commits that touch none of those paths drop out. Commits that also
   touched other paths keep only their demo changes.
5. Move the result to the repo location with `main` as its branch and no
   remote. Commits made after the split use the prefix-free style of a
   standalone repo. The historical `demos/conference:` subjects stay as they
   are.

## Go module and the Substrate pin

- `go.mod` declares `module github.com/agent-substrate/agent-swarm`. Every
  import of `github.com/agent-substrate/substrate/demos/conference/...` is
  rewritten to the new module path.
- The `go` directive and the `replace k8s.io/apimachinery => ...` line match
  substrate's `go.mod` at the pinned commit. Without that replace, substrate
  does not build as a dependency.
- `tool` directives pin substrate at commit `c7a1be17`:
  - `github.com/agent-substrate/substrate/cmd/kubectl-ate`
  - `github.com/agent-substrate/substrate/cmd/ateom-gvisor`
- Developers and scripts run `go tool kubectl-ate`. The gateway image builds
  kubectl-ate from the same pin, so the CLI inside the gateway matches the
  cluster.
- The WorkerPool keeps `workerImage: ko://github.com/agent-substrate/substrate/cmd/ateom-gvisor`.
  ko resolves it from the module cache at the pinned version.
- The Go code imports no substrate packages. Only the two tools depend on
  substrate.
- `.ko.yaml` carries the `python:3.12-slim` base override for
  `conf-workload`. It also carries whatever settings ateom-gvisor needs from
  substrate's root `.ko.yaml`. The plan copies those values.

Bumping the pin means `go get -tool` for both tools, copying substrate's
`go` directive and apimachinery replace, and redeploying.

## Deploy: `hack/deploy.sh up|down [--patch-router]`

`hack/lib.sh` holds the helpers the demo borrowed from substrate's
`install-ate.sh`, trimmed to what it uses:
- `run_kubectl` and `run_kubectl_fatal`
- `log_step`
- waiting for the WorkerPool rollout
- `ensure_atespace`
- creating an actor template and waiting for its golden snapshot to be ready
- deleting the actors and the template

`substitute_version` is dropped because the demo has no version to
substitute.

`up`:
1. **Prerequisites.**
   - Require `kubectl`, `ko`, `docker buildx` and `jq`.
   - Require the `ate-system` namespace. If it is missing: with
     `SUBSTRATE_REPO` set, run `$SUBSTRATE_REPO/hack/install-ate.sh
     --deploy-ate-system`; otherwise fail with install instructions.
   - Require every node's `ate.dev/substrate-version` label to equal the
     pinned commit. On a mismatch, refuse and print how to bump the pin.
2. **WorkerPool.** Render it and apply it through `ko resolve`, then wait
   for the rollout.
3. **Atespace and template.** Create the atespace. Keep the check that
   refuses a stale `conf-agent` template (`DEMO_CONF_TEMPLATE_REVISION`).
   Create the template through `ko resolve` and wait for its golden snapshot.
4. **Router flags.** Check them as today. Patch them only with
   `--patch-router`.
5. **Vertex AI.** When `PROJECT_ID` is set, create the service account and
   the Workload Identity binding, as today.
6. **Gateway.** Build `build/gateway.Dockerfile` with `docker buildx`
   (linux/amd64) and push it to `KO_DOCKER_REPO`. Pin the image by digest,
   then apply the Secret, the gateway manifest and, when `DEMO_DOMAIN` is set,
   the ingress.
7. **Actors.** Create `DEMO_CONF_ACTORS` actors (default 50).

`down` deletes the actors, the template, the gateway and the WorkerPool. Like
today, it leaves the router flags, the service account and the IAM binding
in place.

The environment variables stay the same:
- required: `KO_DOCKER_REPO`, `BUCKET_NAME`
- optional: `PROJECT_ID`, `VERTEX_LOCATION`, `DEMO_DOMAIN`, `DEMO_CONF_ACTORS`, `SUBSTRATE_REPO`

An existing setup therefore carries over unchanged.

`hack/test-deploy.sh` keeps today's cluster-free tests of the router flag
checks, the router patch builder, the stale-template check, the duration
parser and the template render. It adds tests for the substrate version check.

## Local development

`make run-local` starts `conf-localrouter` and then the gateway with
`LOCAL_MODE=true DEMO_MOCK_LLM=true`, as the README describes today. It needs
no cluster or credentials.

## Makefile

| Target | Runs |
|---|---|
| `test` | `go test ./...` and `hack/test-deploy.sh` |
| `verify` | gofmt, `go vet`, golangci-lint, license headers, `go mod tidy` check, `hack/verify-manifests.sh`, shellcheck |
| `build` | every binary, plus the gateway image built locally without pushing |
| `run-local` | local mode, above |
| `deploy` / `undeploy` | `hack/deploy.sh up` / `down` |
| `e2e` | `hack/verify.sh` against the deployed demo |

## CI

`.github/workflows/ci.yaml` needs no cluster:
- `make verify` and `make test`
- build `build/gateway.Dockerfile` without pushing
- `go build` both pinned substrate tools, which catches a broken pin or a
  stale replace

## Proof that it works standalone

Run in this order. Each step must pass before the next:

1. **Clean clone.** A fresh clone, built with no substrate checkout on the
   Go or shell paths, passes `make verify test build`.
2. **Local mode.** `make run-local` serves `/stage` and `/chat`, checked
   with screenshots.
3. **Cluster.** On substrate-poc, run `make undeploy` using the monorepo
   install, then `make deploy` and `make e2e` from agent-swarm. All 13
   verify steps must pass on real Gemini.
   - This resets the 50 actors, takes about 20 minutes, and also ships
     the committed syntax-check change.
   - Get the user's confirmation before starting it, since substrate-poc
     is a shared cluster.

## Risks

- **The apimachinery replace.** If substrate's replace changes, a pin bump
  must copy it or the tools will not build. CI's tool build catches this.
- **ko and module-cache resolution of ateom-gvisor.** The worker image must
  come from the same commit as the control plane. The deploy script's
  version check guards the cluster side. The plan must confirm that ko
  resolves the `tool` dependency's version rather than the latest one.
- **History filtering.** Renames inside `demos/conference` over time (for
  example files that moved into `manifests/`) must still land at the new
  paths. After filtering, compare the tree against the worktree's
  `demos/conference`, file by file, before any new commit.

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

# agent-swarm Repo Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `demos/conference` into a standalone, history-preserving repository, `agent-swarm`. It must build, test, run locally and deploy to a Substrate cluster without a substrate checkout.

**Architecture:**
1. `git filter-repo` extracts the demo's history from a scratch clone and renames paths to a top-level layout.
2. A new `go.mod` pins substrate at one commit through `tool` directives for `kubectl-ate` and `ateom-gvisor`.
3. A standalone `hack/deploy.sh` and `hack/lib.sh` replace the `install-ate.sh` plugin. It checks that the cluster runs the pinned Substrate version.

**Tech Stack:** Go 1.27, bash, ko, docker buildx, git-filter-repo, golangci-lint v2.13.2, shellcheck, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-24-agent-swarm-repo-split-design.md`

## Global Constraints

- New repo path: `/Users/mofi/Documents/github.com/agent-substrate/agent-swarm`. Module: `github.com/agent-substrate/agent-swarm`. Branch: `main`. No remote.
- Never push, never create a GitHub repo, never modify the substrate worktree's history or files. The only exception is the plan and spec commits that already exist there.
- Substrate pin: commit `c7a1be177d0f1391c38a3b77f333d10ce8ec324a` (node label `c7a1be17`).
- Copy this line verbatim from substrate's `go.mod`: `replace k8s.io/apimachinery => k8s.io/apimachinery v0.36.0-alpha.2.0.20260904094813-f8600edbddc8`. The `go` directive is `go 1.27.0`.
- The Go code must not import any `github.com/agent-substrate/substrate/...` package. Only the two tool directives reference substrate.
- Binary names stay `conf-gateway`, `conf-workload` and `conf-localrouter`. Namespace and atespace `ate-demo-conf`, pool `conf-workers`, template `conf-agent`.
- The deploy environment is unchanged. Required: `KO_DOCKER_REPO` and `BUCKET_NAME`. Optional: `PROJECT_ID`, `VERTEX_LOCATION`, `DEMO_DOMAIN`, `DEMO_CONF_ACTORS` and `SUBSTRATE_REPO`.
- Every file carries the Apache 2.0 "Copyright 2026 Google LLC" header in the style of the file type. Templates are in `hack/boilerplate/`.
- American English. Keep comments short and describe the final state.
- Commit messages in agent-swarm have no `demos/conference:` prefix and no issue or PR refs. End each one with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- The provider API key never enters actors, snapshots or the repo.
- TDD applies to new shell logic, such as the version check. Moved code keeps its existing tests, which must pass.

## Path mapping (used by Task 1 and referenced by later tasks)

| Old path (substrate branch `docs/demo-ideas`) | New path |
|---|---|
| `demos/conference/cmd/` | `cmd/` |
| `demos/conference/internal/` | `internal/` |
| `demos/conference/pkg/` | `pkg/` |
| `demos/conference/manifests/` | `manifests/` |
| `demos/conference/conference.yaml.tmpl` | `manifests/workerpool.yaml.tmpl` |
| `demos/conference/conference-template.yaml.tmpl` | `manifests/actortemplate.yaml.tmpl` |
| `demos/conference/Dockerfile` | `build/gateway.Dockerfile` |
| `demos/conference/Dockerfile.dockerignore` | `build/gateway.Dockerfile.dockerignore` |
| `demos/conference/hack/checkmanifests/` | `hack/checkmanifests/` |
| `demos/conference/README.md` | `README.md` |
| `hack/install-demo-conference.sh` | `hack/deploy.sh` |
| `hack/test-install-demo-conference.sh` | `hack/test-deploy.sh` |
| `hack/verify-demo-conference.sh` | `hack/verify.sh` |
| `hack/verify-demo-conference-manifests.sh` | `hack/verify-manifests.sh` |
| `docs/superpowers/specs/2026-09-2{0,1,2,3}-*conference*` and `2026-09-24-agent-swarm-repo-split-design.md` | unchanged |
| `docs/superpowers/plans/2026-09-2{0,1,2,3}-*conference*` and `2026-09-24-agent-swarm-repo-split.md` | unchanged |

---

### Task 1: Extract the history into the new repo

**Files:**
- Create: `/Users/mofi/Documents/github.com/agent-substrate/agent-swarm` (whole repo, via filter-repo)
- Create (scratch, deleted at end): `/tmp/agent-swarm-split/`

**Interfaces:**
- Produces: a git repo at the new path whose tree at `main` equals the substrate worktree's demo files under the path mapping. Later tasks commit on top of it.

- [ ] **Step 1: Install git-filter-repo**

Run: `brew install git-filter-repo && git filter-repo --version`
Expected: a version string (2.47.0 or later).

- [ ] **Step 2: Make sure the plan and spec are committed in the worktree**

Run from `/Users/mofi/Documents/github.com/agent-substrate/substrate/.worktrees/demo-ideas`:
`git status --porcelain docs/superpowers && git log --oneline -3`
Expected: no output from status, and the plan commit is in the log. The clone only sees committed files.

- [ ] **Step 3: Clone the branch into scratch**

```bash
rm -rf /tmp/agent-swarm-split
git clone --no-local --single-branch --branch docs/demo-ideas \
  /Users/mofi/Documents/github.com/agent-substrate/substrate /tmp/agent-swarm-split
```
Expected: the clone succeeds. `git -C /tmp/agent-swarm-split log --oneline -1` shows the same HEAD as the worktree.

- [ ] **Step 4: Filter and rename**

```bash
cd /tmp/agent-swarm-split
git filter-repo --force \
  --path demos/conference/ \
  --path hack/install-demo-conference.sh \
  --path hack/test-install-demo-conference.sh \
  --path hack/verify-demo-conference.sh \
  --path hack/verify-demo-conference-manifests.sh \
  --path-glob 'docs/superpowers/specs/2026-09-2[0-3]-*conference*' \
  --path-glob 'docs/superpowers/plans/2026-09-2[0-3]-*conference*' \
  --path docs/superpowers/specs/2026-09-24-agent-swarm-repo-split-design.md \
  --path docs/superpowers/plans/2026-09-24-agent-swarm-repo-split.md \
  --path-rename demos/conference/conference.yaml.tmpl:manifests/workerpool.yaml.tmpl \
  --path-rename demos/conference/conference-template.yaml.tmpl:manifests/actortemplate.yaml.tmpl \
  --path-rename demos/conference/Dockerfile.dockerignore:build/gateway.Dockerfile.dockerignore \
  --path-rename demos/conference/Dockerfile:build/gateway.Dockerfile \
  --path-rename demos/conference/hack/:hack/ \
  --path-rename demos/conference/README.md:README.md \
  --path-rename demos/conference/:  \
  --path-rename hack/install-demo-conference.sh:hack/deploy.sh \
  --path-rename hack/test-install-demo-conference.sh:hack/test-deploy.sh \
  --path-rename hack/verify-demo-conference-manifests.sh:hack/verify-manifests.sh \
  --path-rename hack/verify-demo-conference.sh:hack/verify.sh
```
filter-repo applies `--path` and `--path-rename` rules in command-line order, and each rule is a prefix match that sees the result of the rules before it. So:
- All `--path` filters come first, so they match original paths.
- The specific renames come before the catch-all `demos/conference/:`, which moves what is left to the root (`cmd/`, `internal/`, `pkg/`, `manifests/`).
- `Dockerfile.dockerignore` is renamed before `Dockerfile`, because the `demos/conference/Dockerfile` prefix would otherwise also catch the dockerignore.

- [ ] **Step 5: Verify that the tree matches the worktree, file by file**

```bash
WT=/Users/mofi/Documents/github.com/agent-substrate/substrate/.worktrees/demo-ideas
cd /tmp/agent-swarm-split
fail=0
check() { cmp -s "$WT/$1" "$2" || { echo "DIFF: $1 -> $2"; fail=1; }; }
while IFS= read -r f; do
  rel="${f#demos/conference/}"
  case "$rel" in
    conference.yaml.tmpl) new=manifests/workerpool.yaml.tmpl ;;
    conference-template.yaml.tmpl) new=manifests/actortemplate.yaml.tmpl ;;
    Dockerfile) new=build/gateway.Dockerfile ;;
    Dockerfile.dockerignore) new=build/gateway.Dockerfile.dockerignore ;;
    *) new="$rel" ;;
  esac
  check "$f" "$new"
done < <(cd "$WT" && git ls-files demos/conference)
check hack/install-demo-conference.sh hack/deploy.sh
check hack/test-install-demo-conference.sh hack/test-deploy.sh
check hack/verify-demo-conference.sh hack/verify.sh
check hack/verify-demo-conference-manifests.sh hack/verify-manifests.sh
echo "tracked files: $(git ls-files | wc -l)"
git ls-files | grep -E '^(vendor|reports|demos)/' && fail=1
echo "fail=$fail"
```
Expected:
- no `DIFF:` lines and `fail=0`
- no `vendor/`, `reports/` or `demos/` paths

Every file under `git ls-files` must come from the mapping. Check with `git ls-files | sed 's|/.*||' | sort -u`, which should list only: `README.md`, `build`, `cmd`, `docs`, `hack`, `internal`, `manifests`, `pkg`.

- [ ] **Step 6: Check the history**

Run: `git log --oneline | wc -l; git log --oneline | tail -3; git log --oneline -3`
Expected:
- dozens of commits (the branch has 159 ahead of main, and only the demo ones survive)
- the newest commit is the plan commit
- no empty commits (filter-repo prunes them by default)

- [ ] **Step 7: Move it into place**

```bash
test ! -e /Users/mofi/Documents/github.com/agent-substrate/agent-swarm || { echo "target exists, stop"; exit 1; }
git -C /tmp/agent-swarm-split branch -m docs/demo-ideas main
git -C /tmp/agent-swarm-split remote -v   # filter-repo removes origin; expect no output
mv /tmp/agent-swarm-split /Users/mofi/Documents/github.com/agent-substrate/agent-swarm
git -C /Users/mofi/Documents/github.com/agent-substrate/agent-swarm status --short | head
```
Expected: the target did not exist before, there is no remote, and the status is clean. No commit is needed for this task.

---

### Task 2: Go module, import rewrite and the substrate pin

All paths from here on are relative to `/Users/mofi/Documents/github.com/agent-substrate/agent-swarm`.

**Files:**
- Create: `go.mod`, `go.sum`, `.ko.yaml`, `.gitignore`, `hack/tools/go.mod`, `hack/tools/go.sum`, `.golangci.yaml`
- Modify: every `*.go` file that imports `github.com/agent-substrate/substrate/demos/conference/...`
- Modify: `internal/workload/engine/template_test.go:31`
- Modify: `cmd/conf-gateway/internal/web/web.go:133-135`

**Interfaces:**
- Produces:
  - module `github.com/agent-substrate/agent-swarm`
  - `go tool kubectl-ate` and `go tool ateom-gvisor` resolve at the pin
  - `go tool -modfile=hack/tools/go.mod golangci-lint` runs the linter

- [ ] **Step 1: Confirm that the module build currently fails (the baseline)**

Run: `go build ./... 2>&1 | head -3`
Expected: an error about there being no `go.mod` ("go: cannot find main module" or similar).

- [ ] **Step 2: Rewrite the import paths**

```bash
grep -rl 'github.com/agent-substrate/substrate/demos/conference' --include='*.go' . \
  | xargs sed -i '' 's|github.com/agent-substrate/substrate/demos/conference|github.com/agent-substrate/agent-swarm|g'
grep -rn 'agent-substrate/substrate' --include='*.go' . ; echo "exit=$?"
```
Expected: the second grep prints nothing (`exit=1`).

- [ ] **Step 3: Fix the relative paths that the move changed**

In `internal/workload/engine/template_test.go`, replace
```go
	raw, err := os.ReadFile("../../../conference-template.yaml.tmpl")
```
with
```go
	raw, err := os.ReadFile("../../../manifests/actortemplate.yaml.tmpl")
```
In `cmd/conf-gateway/internal/web/web.go`, replace the candidate entry `"demos/conference/cmd/conf-gateway/static",` with `"cmd/conf-gateway/static",`. It is a dev-time lookup that is relative to the repo root.

Then check for any other old references in code and templates:
`grep -rn 'demos/conference\|conference-template\|conference\.yaml' --include='*.go' --include='*.tmpl' . ; echo "exit=$?"`
Expected: `exit=1`. If any hit remains, fix it with the path mapping table.

- [ ] **Step 4: Write go.mod with the replace and the tool pins**

```bash
cat > go.mod <<'EOF'
module github.com/agent-substrate/agent-swarm

go 1.27.0

replace k8s.io/apimachinery => k8s.io/apimachinery v0.36.0-alpha.2.0.20260904094813-f8600edbddc8
EOF
PIN=c7a1be177d0f1391c38a3b77f333d10ce8ec324a
go get -tool github.com/agent-substrate/substrate/cmd/kubectl-ate@${PIN}
go get -tool github.com/agent-substrate/substrate/cmd/ateom-gvisor@${PIN}
go mod tidy
grep -n '^tool\|agent-substrate/substrate \|^replace\|^go ' go.mod
```
Expected:
- two `tool` lines
- `github.com/agent-substrate/substrate v…-c7a1be177d0f`, a pseudo-version ending in the pin's first 12 characters
- the replace line
- `go 1.27.0`

If `go mod tidy` rewrites the `go` line to a higher version, set it back to `go 1.27.0` and re-run tidy. It must stay 1.27.0.

- [ ] **Step 5: Build and test**

Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -30`
Expected: every package prints `ok` or `no test files`, with no FAIL.

Run: `go tool kubectl-ate --help | head -3 && GOOS=linux go build -o /dev/null github.com/agent-substrate/substrate/cmd/ateom-gvisor && echo ateom-ok`
Expected: kubectl-ate help text and `ateom-ok`. ateom-gvisor builds only for linux, hence `GOOS=linux`.

- [ ] **Step 6: Write `.ko.yaml`**

```yaml
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Same default base as substrate's .ko.yaml, which ateom-gvisor is built with.
defaultBaseImage: gcr.io/distroless/static-debian13:latest@sha256:f2ea2709ac8db56323cbd7d014277f32cb572d9ea124b0076f7aafe5980678fe

defaultPlatforms:
  - linux/amd64
  - linux/arm64

baseImageOverrides:
  # The workload runs the model's Python scripts and syntax-checks them.
  github.com/agent-substrate/agent-swarm/cmd/conf-workload: python:3.12-slim
```

- [ ] **Step 7: Prove that ko builds ateom-gvisor at the pinned version (spec risk)**

```bash
ko build --local --platform=linux/amd64 --bare github.com/agent-substrate/substrate/cmd/ateom-gvisor 2>&1 | tail -3
go list -m -f '{{.Dir}}' github.com/agent-substrate/substrate
```
Expected: ko prints a `ko.local/...` image reference. The module directory ends in `-c7a1be177d0f`, which shows that `go list` (and so ko) resolves the pinned version. Clean up with `docker rmi` on the printed reference.

Run: `ko build --local --platform=linux/amd64 --bare ./cmd/conf-workload 2>&1 | tail -1`
Expected: an image reference. Its base is python:3.12-slim.

- [ ] **Step 8: Add the lint tool module and config**

```bash
mkdir -p hack/tools
cat > hack/tools/go.mod <<'EOF'
module github.com/agent-substrate/agent-swarm/hack/tools

go 1.27.0
EOF
(cd hack/tools && go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2 && go mod tidy)
cp /Users/mofi/Documents/github.com/agent-substrate/substrate/.worktrees/demo-ideas/.golangci.yaml .golangci.yaml
```
In `.golangci.yaml`, delete any exclusion `paths` entries for directories that do not exist in agent-swarm (`LICENSES/`, generated code and similar). Keep `hack/tools`.

Run: `go tool -modfile=hack/tools/go.mod golangci-lint run ./...`
Expected: `0 issues.` If it reports findings, fix them in the code. They are real lint findings, which the monorepo also runs (for example, misspell or errcheck).

- [ ] **Step 9: Write `.gitignore`**

```
/bin/
*.test
.superpowers/
*.env
```

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "Build as the standalone github.com/agent-substrate/agent-swarm module

Imports move from the substrate monorepo path to the new module. go.mod
pins substrate's kubectl-ate and ateom-gvisor as tool dependencies at the
commit the demo was developed against, with substrate's apimachinery
replace, so the demo builds without a substrate checkout.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: A gateway image that builds from this repo

**Files:**
- Modify: `build/gateway.Dockerfile`
- Modify: `build/gateway.Dockerfile.dockerignore`

**Interfaces:**
- Consumes: `go.mod` from Task 2
- Produces: `docker buildx build -f build/gateway.Dockerfile .` builds an image with `/usr/local/bin/kubectl-ate` and `/usr/local/bin/conf-gateway`. Task 4's deploy script builds it this way.

- [ ] **Step 1: Confirm that the current file fails**

Run: `docker buildx build --platform=linux/amd64 -f build/gateway.Dockerfile -t agent-swarm-gw:test . 2>&1 | tail -5`
Expected: FAIL, because `./cmd/kubectl-ate` and `./demos/conference/...` do not exist.

- [ ] **Step 2: Replace the build lines**

Keep the license header. Replace everything after it with:
```dockerfile
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
# kubectl-ate comes from the substrate version pinned in go.mod, so the CLI
# the gateway shells out to matches the cluster.
RUN go build -trimpath -o /out/kubectl-ate github.com/agent-substrate/substrate/cmd/kubectl-ate \
 && go build -trimpath -o /out/conf-gateway ./cmd/conf-gateway

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/kubectl-ate /usr/local/bin/kubectl-ate
COPY --from=build /out/conf-gateway /usr/local/bin/conf-gateway
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/conf-gateway"]
```

Replace the dockerignore body (after the header) with:
```
.git
.superpowers
**/*.test
bin
docs
*.env
```

- [ ] **Step 3: Build and inspect it**

```bash
docker buildx build --platform=linux/amd64 --load -f build/gateway.Dockerfile -t agent-swarm-gw:test .
docker run --rm --platform=linux/amd64 --entrypoint /usr/local/bin/kubectl-ate agent-swarm-gw:test --help | head -2
docker rmi agent-swarm-gw:test
```
Expected: the build succeeds and kubectl-ate prints its help.

- [ ] **Step 4: Commit**

```bash
git add build/
git commit -m "Build the gateway image from this repo and the pinned kubectl-ate

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Standalone deploy script with the Substrate version check

**Files:**
- Create: `hack/lib.sh`
- Modify: `hack/deploy.sh` (was `install-demo-conference.sh`)
- Modify: `hack/test-deploy.sh` (was `test-install-demo-conference.sh`)
- Create: `hack/boilerplate/go.txt`, `hack/boilerplate/sh.txt` (copied from substrate `hack/boilerplate/`)

**Interfaces:**
- Consumes: `go tool kubectl-ate` (Task 2) and `build/gateway.Dockerfile` (Task 3)
- Produces:
  - `hack/deploy.sh up [--patch-router]` and `hack/deploy.sh down`
  - `hack/test-deploy.sh` (exit 0 when all checks pass)
  - these functions in `hack/lib.sh`: `log_step`, `run_kubectl`, `run_kubectl_fatal`, `run_kubectl_ate`, `wait_for_pool_rollout_fatal`, `wait_actortemplate_ready`, `ensure_atespace`, `create_actor_template RENDER_FN MANIFEST ATESPACE TEMPLATE [TIMEOUT]`, `delete_template_actors ATESPACE TEMPLATE`, `delete_actor_template ATESPACE TEMPLATE`, `substrate_pin`, `substrate_labels_match PIN LABELS`
  - every `demo-conference_*` function is renamed to `conf_*`, with the same behavior

- [ ] **Step 1: Copy the boilerplate templates**

```bash
mkdir -p hack/boilerplate
cp /Users/mofi/Documents/github.com/agent-substrate/substrate/.worktrees/demo-ideas/hack/boilerplate/{go,sh}.txt hack/boilerplate/
```

- [ ] **Step 2: Rename the function prefix and point the test at the new files**

```bash
sed -i '' 's/demo-conference_/conf_/g' hack/deploy.sh hack/test-deploy.sh
```
In `hack/test-deploy.sh`:
- Delete the lines `ATE_DEMOS=()`, `ate_demo_flag_unhandled() { :; }`, the `substitute_version` comment and stub, and the `# shellcheck source=hack/install-demo-conference.sh` line.
- After the `ROOT=...` line, add `cd "${ROOT}"`. The render tests and `substrate_pin` use paths relative to the repo root.
- Change the source line to:
```bash
# shellcheck source=hack/deploy.sh
source "${ROOT}/hack/deploy.sh"
```
- Change `demos/conference/conference-template.yaml.tmpl` to `manifests/actortemplate.yaml.tmpl`.
- Change `demos/conference/manifests/gateway.yaml.tmpl` to `manifests/gateway.yaml.tmpl`.

- [ ] **Step 3: Write the failing tests for the version check**

Insert this block in `hack/test-deploy.sh` just before the final `exit "${fail}"`:
```bash
# expect_labels checks substrate_labels_match: every labeled node must run
# the pinned commit, and at least one node must be labeled.
expect_labels() {
  local want="$1" labels="$2" got=0
  substrate_labels_match c7a1be177d0f "${labels}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: labels ${want} for '${labels//$'\n'/,}'" >&2
    fail=1
  fi
}
expect_labels ok  $'c7a1be17\nc7a1be17'
expect_labels ok  $'c7a1be17\n'           # an unlabeled node is ignored
expect_labels bad $'c7a1be17\n0123abcd'   # one node on another version
expect_labels bad ''                      # no labeled nodes at all
expect_labels bad $'\n\n'
expect_labels bad 'c7a1be177d0f99'        # longer than the pin is not a match

# substrate_pin reads the pinned commit from go.mod.
if [[ "$(substrate_pin)" != "c7a1be177d0f" ]]; then
  echo "FAIL: substrate_pin = '$(substrate_pin)', want c7a1be177d0f" >&2
  fail=1
fi
```

- [ ] **Step 4: Run it and see it fail**

Run: `bash hack/test-deploy.sh`
Expected: FAIL. The sourced `deploy.sh` still ends by exec-ing `install-ate.sh`, or else `substrate_labels_match` and `substrate_pin` are "command not found". Either way it is non-zero.

- [ ] **Step 5: Write `hack/lib.sh`**

```bash
#!/usr/bin/env bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Helpers for hack/deploy.sh, ported from substrate's hack/install-ate.sh.
# Sourced; expects to run from the repo root.

SUBSTRATE_MODULE=github.com/agent-substrate/substrate

log_step() {
  echo -e "\033[1;36m[step]: $1\033[0m"
}

run_kubectl() {
  kubectl ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
}

# run_kubectl_fatal runs kubectl and aborts the deploy if it fails.
run_kubectl_fatal() {
  if ! run_kubectl "$@"; then
    echo "error: kubectl $* failed" >&2
    exit 1
  fi
}

# run_kubectl_ate runs the kubectl-ate pinned in go.mod.
run_kubectl_ate() {
  go tool kubectl-ate ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
}

# substrate_pin prints the 12-character commit of the substrate module
# pinned in go.mod, taken from its pseudo-version.
substrate_pin() {
  local v
  v="$(go list -m -f '{{.Version}}' "${SUBSTRATE_MODULE}")" || return 1
  echo "${v##*-}"
}

# substrate_labels_match succeeds if at least one line of LABELS is set and
# every set line is a prefix of PIN. LABELS holds one node's
# ate.dev/substrate-version per line; unlabeled nodes are empty lines.
substrate_labels_match() {
  local pin="$1" labels="$2" label seen=false
  while IFS= read -r label; do
    [[ -z "${label}" ]] && continue
    [[ "${pin}" == "${label}"* ]] || return 1
    seen=true
  done <<<"${labels}"
  [[ "${seen}" == true ]]
}

# wait_for_pool_rollout_fatal waits for a WorkerPool's Deployment, which
# ate-controller creates after the apply returns, to exist and roll out.
wait_for_pool_rollout_fatal() {
  local pool="$1" namespace="$2" timeout="${3:-300s}"
  if ! run_kubectl wait --for=create "deployment/${pool}" -n "${namespace}" --timeout="${timeout}" \
      || ! run_kubectl rollout status "deployment/${pool}" -n "${namespace}" --timeout="${timeout}"; then
    echo "error: worker pool ${pool} did not roll out" >&2
    exit 1
  fi
}

# wait_actortemplate_ready polls an ActorTemplate until its golden snapshot
# exists, failing fast when the template reconciler reports an error.
wait_actortemplate_ready() {
  local atespace="$1" template="$2" timeout_secs="${3:-300}"
  local deadline=$((SECONDS + timeout_secs)) json snapshot error_message
  while ((SECONDS < deadline)); do
    if json=$(run_kubectl_ate get actor-template "${template}" -a "${atespace}" -o json 2>/dev/null); then
      snapshot=$(jq -r '.status.goldenSnapshotStatus.goldenTag.name // empty' <<<"${json}")
      [[ -n "${snapshot}" ]] && return 0
      error_message=$(jq -r '.status.goldenSnapshotStatus.errorMessage // empty' <<<"${json}")
      if [[ -n "${error_message}" ]]; then
        echo "actor template ${atespace}/${template} failed: ${error_message}" >&2
        return 1
      fi
    fi
    sleep 5
  done
  echo "timed out waiting for actor template ${atespace}/${template} golden snapshot" >&2
  return 1
}

# ensure_atespace creates the atespace if it does not already exist.
ensure_atespace() {
  local atespace="$1"
  if ! run_kubectl_ate create atespace "${atespace}" >/dev/null 2>&1 \
      && ! run_kubectl_ate get atespace "${atespace}" >/dev/null 2>&1; then
    echo "error: failed to create atespace ${atespace}" >&2
    exit 1
  fi
}

# create_actor_template renders a protojson ActorTemplate, resolves its ko://
# images, creates it and waits for its golden snapshot. Templates are
# immutable, so an existing one is kept.
create_actor_template() {
  local render_fn="$1" manifest="$2" atespace="$3" template="$4" timeout_secs="${5:-300}"
  if ! "${render_fn}" "${manifest}" | ko resolve -f - | run_kubectl_ate create actor-template -f -; then
    if run_kubectl_ate get actor-template "${template}" -a "${atespace}" >/dev/null 2>&1; then
      log_step "actor template ${atespace}/${template} already exists; keeping it"
    else
      echo "error: failed to create actor template ${atespace}/${template}" >&2
      exit 1
    fi
  fi
  log_step "Waiting for the ${atespace}/${template} golden snapshot..."
  wait_actortemplate_ready "${atespace}" "${template}" "${timeout_secs}" || exit 1
}

# prepare_actor_for_delete suspends an actor (resuming it first if paused)
# until DeleteActor accepts it.
prepare_actor_for_delete() {
  local actor="$1" atespace="$2" deadline=$((SECONDS + 120)) json state
  while ((SECONDS < deadline)); do
    json=$(run_kubectl_ate get actor "${actor}" -a "${atespace}" -o json 2>/dev/null) || return 0
    state=$(jq -r '.status.state // empty' <<<"${json}")
    case "${state}" in
      ACTOR_STATE_SUSPENDED | ACTOR_STATE_CRASHED | ACTOR_STATE_DELETING) return 0 ;;
      ACTOR_STATE_PAUSED) run_kubectl_ate resume actor "${actor}" -a "${atespace}" -o json >/dev/null ;;
      ACTOR_STATE_RUNNING) run_kubectl_ate suspend actor "${actor}" -a "${atespace}" -o json >/dev/null ;;
      ACTOR_STATE_RESUMING | ACTOR_STATE_SUSPENDING | ACTOR_STATE_PAUSING) ;;
      *)
        echo "cannot delete actor ${actor}: unexpected state ${state}" >&2
        return 1
        ;;
    esac
    sleep 2
  done
  echo "timed out waiting for actor ${actor} to become deletable" >&2
  return 1
}

# delete_template_actors deletes every actor created from ATESPACE/TEMPLATE.
delete_template_actors() {
  local tmpl_atespace="$1" tmpl="$2" actors_json atespace actor
  if ! actors_json=$(run_kubectl_ate get actors -A -o json 2>/dev/null); then
    echo "warning: could not list actors; skipping actor cleanup" >&2
    return 0
  fi
  log_step "Deleting actors for ${tmpl_atespace}/${tmpl}"
  while IFS=$'\t' read -r atespace actor; do
    [[ -z "${actor}" ]] && continue
    prepare_actor_for_delete "${actor}" "${atespace}"
    run_kubectl_ate delete actor "${actor}" -a "${atespace}"
  done < <(jq -r --arg as "${tmpl_atespace}" --arg tmpl "${tmpl}" \
    '.actors[]? | select(.actorTemplate.atespace == $as and .actorTemplate.name == $tmpl) | "\(.metadata.atespace)\t\(.metadata.name)"' \
    <<<"${actors_json}")
}

# delete_actor_template deletes a template's actors, then the template,
# which server-side also removes its golden actor and snapshot.
delete_actor_template() {
  local atespace="$1" template="$2"
  delete_template_actors "${atespace}" "${template}"
  run_kubectl_ate delete actor-template "${template}" -a "${atespace}" 2>/dev/null \
    || log_step "actor template ${atespace}/${template} not deleted (may not exist)"
}
```

- [ ] **Step 6: Rework `hack/deploy.sh`**

Edit the file as follows. Everything not listed keeps its current text: the seconds and flag parsers, the router checks and patch, the GSA and IAM retry, the secret, and the template-current check.

1. Replace the header comment `# This is sourced as part of install-ate.sh, or executed directly.` and the `ATE_DEMOS+=(...)` line with:
```bash
# Deploys the demo onto a cluster that runs Substrate. Sourced by
# hack/test-deploy.sh for its cluster-free tests.

# shellcheck source=hack/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
```
2. Change `DEMO_CONF_ACTORS=50` to `DEMO_CONF_ACTORS="${DEMO_CONF_ACTORS:-50}"`.
3. Delete `conf_usage` and `conf_cmdline`.
4. In `conf_build_gateway`, change `-f demos/conference/Dockerfile` to `-f build/gateway.Dockerfile`.
5. In `conf_render`, change the last line from `"$1" | substitute_version` to `"$1"`.
6. In `conf_check_template`, change the hint to `echo "Run: hack/deploy.sh down, then hack/deploy.sh up." >&2`.
7. Add this function before `conf_deploy`:
```bash
# conf_check_substrate requires Substrate on the cluster at the version
# pinned in go.mod, installing it from SUBSTRATE_REPO if it is absent.
conf_check_substrate() {
  local pin labels
  pin="$(substrate_pin)" || { echo "error: cannot read the substrate pin from go.mod" >&2; exit 1; }
  if ! run_kubectl get namespace ate-system >/dev/null 2>&1; then
    if [[ -z "${SUBSTRATE_REPO:-}" ]]; then
      echo "error: Substrate is not installed (no ate-system namespace)." >&2
      echo "Install it from a substrate checkout at commit ${pin}:" >&2
      echo "  hack/install-ate.sh --deploy-ate-system" >&2
      echo "or set SUBSTRATE_REPO to that checkout and run this again." >&2
      exit 1
    fi
    local head
    head="$(git -C "${SUBSTRATE_REPO}" rev-parse HEAD)"
    if [[ "${head}" != "${pin}"* ]]; then
      echo "error: SUBSTRATE_REPO is at ${head:0:12}, not the pinned ${pin}. Check out ${pin} there first." >&2
      exit 1
    fi
    log_step "Installing Substrate from ${SUBSTRATE_REPO}..."
    (cd "${SUBSTRATE_REPO}" && hack/install-ate.sh --deploy-ate-system)
  fi
  labels="$(run_kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.labels.ate\.dev/substrate-version}{"\n"}{end}')"
  if ! substrate_labels_match "${pin}" "${labels}"; then
    echo "error: the cluster's nodes run Substrate $(sort -u <<<"${labels}" | tr '\n' ' ')but go.mod pins ${pin}." >&2
    echo "Bump the pin to the cluster's commit:" >&2
    echo "  go get -tool ${SUBSTRATE_MODULE}/cmd/kubectl-ate@<commit>" >&2
    echo "  go get -tool ${SUBSTRATE_MODULE}/cmd/ateom-gvisor@<commit>" >&2
    echo "  then copy substrate's go directive and k8s.io/apimachinery replace into go.mod and run go mod tidy." >&2
    exit 1
  fi
}
```
8. In `conf_deploy`:
   - Change the first `log_step` to `log_step "Deploying the conference demo"`.
   - Extend the prerequisites line to:
```bash
  local tool
  for tool in kubectl ko docker jq go; do
    command -v "${tool}" >/dev/null || { echo "error: ${tool} is required" >&2; exit 1; }
  done
  conf_check_substrate
```
   - Replace the `deploy_substrate_demo ...` call (both lines) with:
```bash
  conf_render manifests/workerpool.yaml.tmpl | ko resolve -f - | run_kubectl_fatal apply -f -
  log_step "Waiting for the conf-workers worker pool rollout..."
  wait_for_pool_rollout_fatal conf-workers ate-demo-conf
  ensure_atespace ate-demo-conf
  create_actor_template conf_render manifests/actortemplate.yaml.tmpl ate-demo-conf conf-agent 300
```
   - Change `demos/conference/manifests/` to `manifests/` in the two render calls.
9. In `conf_delete`:
   - Change the first `log_step` to `log_step "Deleting the conference demo"`.
   - Change `demos/conference/manifests/` to `manifests/`.
   - Replace the `delete_substrate_demo ...` call (both lines) with:
```bash
  delete_actor_template ate-demo-conf conf-agent
  run_kubectl_ate delete atespace ate-demo-conf 2>/dev/null \
    || log_step "atespace ate-demo-conf not deleted (may not exist or is not empty)"
  conf_render manifests/workerpool.yaml.tmpl | run_kubectl delete --ignore-not-found -f -
```
10. Replace the entire `if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then ... fi` block at the end with:
```bash
conf_main() {
  local action="" arg
  for arg in "$@"; do
    case "${arg}" in
      up | down) action="${arg}" ;;
      --patch-router) DEMO_CONF_PATCH_ROUTER=true ;;
      -h | --help) action=help ;;
      *) echo "error: unknown argument ${arg}" >&2; action=help ;;
    esac
  done
  case "${action}" in
    up) conf_deploy ;;
    down) conf_delete ;;
    *)
      echo "Usage: $0 up|down [--patch-router]"
      echo "  up              deploy the demo onto a cluster running Substrate at the go.mod pin"
      echo "  down            delete it (router flags and the gateway service account stay)"
      echo "  --patch-router  patch atenet-router's timeouts and grace period instead of failing"
      echo "  Required env: KO_DOCKER_REPO, BUCKET_NAME"
      echo "  Optional env: PROJECT_ID (enables Vertex AI; unset runs mock replies), VERTEX_LOCATION,"
      echo "                DEMO_DOMAIN, DEMO_CONF_ACTORS (default 50), SUBSTRATE_REPO"
      [[ "${action}" == help ]] && return 0 || return 1
      ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -o errexit -o nounset -o pipefail
  cd "$(dirname "${BASH_SOURCE[0]}")/.."
  conf_main "$@"
fi
```
With `-h` the usage exits 0. With no action or an unknown argument it exits 1.

- [ ] **Step 7: Run the tests and see them pass**

Run: `bash hack/test-deploy.sh; echo "exit=$?"`
Expected: `exit=0` with no FAIL lines.

Run: `bash hack/deploy.sh --help; echo "exit=$?"; bash hack/deploy.sh; echo "exit=$?"`
Expected: usage and `exit=0`, then usage and `exit=1`.

Run: `grep -n 'install-ate\|demos/conference\|substitute_version\|deploy_substrate_demo\|ATE_DEMOS' hack/deploy.sh hack/test-deploy.sh; echo "exit=$?"`
Expected: `exit=1`. The only `install-ate.sh` references allowed are the two in `conf_check_substrate`, so this grep may list exactly those lines.

Run: `shellcheck -x hack/lib.sh hack/deploy.sh hack/test-deploy.sh`
Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add hack/
git commit -m "Deploy with a standalone script that checks the cluster's Substrate version

hack/deploy.sh up|down replaces the install-ate.sh plugin. The helpers it
borrowed from install-ate.sh live in hack/lib.sh. Before deploying it
requires Substrate on the cluster, optionally installing it from
SUBSTRATE_REPO, and refuses when the nodes' ate.dev/substrate-version is
not the commit go.mod pins.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: The verify scripts and the Makefile

**Files:**
- Modify: `hack/verify-manifests.sh`, `hack/verify.sh`
- Create: `hack/verify-boilerplate.sh`, `Makefile`

**Interfaces:**
- Consumes: `hack/deploy.sh` and `hack/lib.sh` (Task 4) and the tool module (Task 2)
- Produces: the make targets `test`, `verify`, `build`, `run-local`, `deploy`, `undeploy`, `e2e`, `verify-local` and `fmt`

- [ ] **Step 1: Fix the paths in `hack/verify-manifests.sh`**

- Set `GATEWAY_MANIFEST="manifests/gateway.yaml.tmpl"`, `INGRESS_MANIFEST="manifests/gateway-ingress.yaml.tmpl"` and `ACTOR_TEMPLATE="manifests/actortemplate.yaml.tmpl"`.
- Change `go run ./demos/conference/hack/checkmanifests` to `go run ./hack/checkmanifests`.
- Change any `install-demo-conference.sh` mention in comments to `deploy.sh`.

Run: `bash hack/verify-manifests.sh; echo "exit=$?"`
Expected: `exit=0`.

- [ ] **Step 2: Fix the paths in `hack/verify.sh`**

- Change `./demos/conference/cmd/conf-gateway` to `./cmd/conf-gateway`, and `./demos/conference/cmd/conf-localrouter` to `./cmd/conf-localrouter`.
- Change `hack/install-ate.sh --deploy-demo-conference` to `make deploy`.
- Change the header comments to say "the conference demo" instead of `(demos/conference)`.

Run: `grep -n 'demos/conference\|install-ate' hack/verify.sh; echo "exit=$?"`
Expected: `exit=1`.

Run: `bash hack/verify.sh --mock-mode; echo "exit=$?"`
Expected: `exit=0`. This is the local mock run against conf-localrouter. It is the same check that CI and proof step 2 use.

- [ ] **Step 3: Write `hack/verify-boilerplate.sh`**

```bash
#!/usr/bin/env bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Fails if a tracked source file lacks the Apache 2.0 license header.
set -o errexit -o nounset -o pipefail
cd "$(git rev-parse --show-toplevel)"

fail=0
while IFS= read -r f; do
  if ! head -20 "${f}" | grep -q 'Licensed under the Apache License, Version 2.0'; then
    echo "missing license header: ${f}" >&2
    fail=1
  fi
done < <(git ls-files '*.go' '*.sh' '*.py' '*.js' '*.css' '*.html' '*.tmpl' '*.yaml' '*.yml' \
  'Makefile' '*Dockerfile' '*.dockerignore' ':!:go.sum')
exit "${fail}"
```

Run: `bash hack/verify-boilerplate.sh; echo "exit=$?"`
Expected: `exit=0`. For each file it lists, add the header from `hack/boilerplate/` in the comment style the file type uses. The script fails on its own if a header is missing, so write it before adding the Makefile and workflow, and give those headers.

- [ ] **Step 4: Write the `Makefile`**

```make
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

GO ?= go
BINDIR := bin
LINT := $(GO) tool -modfile=hack/tools/go.mod golangci-lint

.PHONY: all
all: verify test build

.PHONY: test
test:
	$(GO) test ./...
	bash hack/test-deploy.sh

.PHONY: fmt
fmt:
	gofmt -w $$(git ls-files '*.go')

.PHONY: verify
verify:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(LINT) run ./...
	$(GO) mod tidy -diff
	cd hack/tools && $(GO) mod tidy -diff
	bash hack/verify-boilerplate.sh
	bash hack/verify-manifests.sh
	shellcheck -x $$(git ls-files 'hack/*.sh')

.PHONY: build
build:
	$(GO) build -o $(BINDIR)/ ./cmd/...
	$(GO) build -o $(BINDIR)/kubectl-ate github.com/agent-substrate/substrate/cmd/kubectl-ate
	docker buildx build --platform=linux/amd64 -f build/gateway.Dockerfile -t agent-swarm/conf-gateway:dev .

# Local mode: two actor slots on this machine and mock model replies.
.PHONY: run-local
run-local:
	@trap 'kill 0' EXIT; \
	$(GO) run ./cmd/conf-localrouter --addr=127.0.0.1:18080 --slots=2 & \
	sleep 2; \
	LOCAL_MODE=true DEMO_MOCK_LLM=true PORT=18090 \
	ROUTER_URL=http://127.0.0.1:18080 STATUS_URL=http://127.0.0.1:18080 ROUTER_STATUS_DISCOVERY=static \
	$(GO) run ./cmd/conf-gateway

.PHONY: verify-local
verify-local:
	bash hack/verify.sh --mock-mode

.PHONY: deploy
deploy:
	bash hack/deploy.sh up $(DEPLOY_FLAGS)

.PHONY: undeploy
undeploy:
	bash hack/deploy.sh down

.PHONY: e2e
e2e:
	bash hack/verify.sh

.PHONY: clean
clean:
	rm -rf $(BINDIR)
```
Before finalizing `run-local`, check the gateway env names against the README's local-mode section. It is around line 244 of the current README: `go run ./demos/conference/cmd/conf-localrouter --addr=127.0.0.1:18080 --slots=2 &` then `LOCAL_MODE=true DEMO_MOCK_LLM=true … go run ./demos/conference/cmd/conf-gateway`. Use exactly the variables it lists. If `PRESENTER_TOKEN` is unset, the gateway logs the presenter URL at startup.

- [ ] **Step 5: Run the whole verify, test and local-mode sequence**

Run: `make verify test && make verify-local; echo "exit=$?"`
Expected: `exit=0`.

Run: `make build && ls bin/`
Expected: `conf-gateway conf-localrouter conf-workload kubectl-ate`, and the image builds.

- [ ] **Step 6: Commit**

```bash
git add Makefile hack/
git commit -m "Add the Makefile and point the verify scripts at the new layout

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Repo docs and CI

**Files:**
- Modify: `README.md`
- Create: `CONTRIBUTING.md`, `LICENSE`, `substrate-gaps.md`, `.github/workflows/ci.yaml`

**Interfaces:**
- Consumes: the make targets from Task 5

- [ ] **Step 1: Write `LICENSE`**

`cp /Users/mofi/Documents/github.com/agent-substrate/substrate/.worktrees/demo-ideas/LICENSE LICENSE` (Apache 2.0).

- [ ] **Step 2: Write `.github/workflows/ci.yaml`**

```yaml
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

name: ci
on:
  push:
    branches: [main]
  pull_request:
permissions:
  contents: read
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - name: Verify
        run: make verify
      - name: Test
        run: make test
      - name: Local end-to-end (mock replies)
        run: make verify-local
      - name: Pinned substrate tools build
        run: |
          go build -o /dev/null github.com/agent-substrate/substrate/cmd/kubectl-ate
          GOOS=linux go build -o /dev/null github.com/agent-substrate/substrate/cmd/ateom-gvisor
      - uses: docker/setup-buildx-action@v3
      - name: Gateway image builds
        run: docker buildx build --platform=linux/amd64 -f build/gateway.Dockerfile .
```
shellcheck is preinstalled on `ubuntu-latest`.

Run: `python3 -c 'import yaml,sys; yaml.safe_load(open(".github/workflows/ci.yaml"))' && echo ok`
Expected: `ok`.

- [ ] **Step 3: Rewrite the README's setup sections**

Keep the demo description, how it works, the presenter and attendee flow, and the troubleshooting sections. Change:
- Every `demos/conference/` path, using the mapping table. `go run ./demos/conference/cmd/X` becomes `go run ./cmd/X`.
- Every `hack/install-ate.sh --deploy-demo-conference` / `--delete-demo-conference` / `hack/install-demo-conference.sh` to `make deploy` / `make undeploy` / `hack/deploy.sh`. `--patch-router` becomes `make deploy DEPLOY_FLAGS=--patch-router`.
- Add a "Prerequisites" section at the top:
  - a GKE cluster running Substrate at the commit pinned in `go.mod` (link `https://github.com/agent-substrate/substrate`, and say that `hack/deploy.sh` refuses a mismatch)
  - `go` 1.27, `kubectl`, `ko`, `docker buildx`, `jq`, `gcloud` (for Vertex AI)
  - the env vars from the Global Constraints
- Add a "Development" section with `make test`, `make verify`, `make run-local` (open `http://127.0.0.1:18090/chat`; the presenter URL is in the gateway log), `make verify-local` and `make e2e`.
- Add a "Bumping Substrate" section with the three steps from `conf_check_substrate`'s error message.

Run: `grep -n 'demos/conference\|install-ate.sh --deploy-demo\|install-demo-conference' README.md; echo "exit=$?"`
Expected: `exit=1`.

- [ ] **Step 4: Write `substrate-gaps.md`**

It lists the Substrate limitations this demo works around. Each item gets one paragraph covering the symptom, the workaround in this repo, and where the fix belongs in substrate:
1. The actor rootfs overlay is created 0700, so a non-root workload user cannot traverse it. The fix belongs in atelet `cmd/atelet/oci.go` and ateom `internal/imagecache/bundle_linux.go`. The workload runs as root inside gVisor.
2. `kubectl-ate` has no verb to manage egress policies, so the demo cannot restrict actor egress from its deploy script.
3. atenet-router's default `--parked-request-budget`, `--route-timeout` and termination grace period are too short for LLM turns. `hack/deploy.sh` checks them, and `--patch-router` raises them.

Before writing items 1 and 3, read the README's troubleshooting section and `hack/deploy.sh` (`conf_router_flags_ok` and `conf_grace_ok`) so that the thresholds quoted match the code: 60s budget, 5m route timeout and 120s grace. Where the README already explains one of these, link to that section rather than repeating it.

- [ ] **Step 5: Write `CONTRIBUTING.md`**

Keep it short, under 40 lines:
- the header (HTML comment form)
- run `make verify test` before sending changes
- write tests for new code
- American English
- commit messages describe the change and why, with no issue or PR references
- `make verify-local` exercises the full flow without a cluster
- bump `DEMO_CONF_TEMPLATE_REVISION` in `hack/deploy.sh` with any change to `manifests/actortemplate.yaml.tmpl`, because templates are immutable

- [ ] **Step 6: Verify and commit**

Run: `make verify test; echo "exit=$?"`
Expected: `exit=0`. The boilerplate check covers the new yaml.

```bash
git add README.md CONTRIBUTING.md LICENSE substrate-gaps.md .github/
git commit -m "Document the standalone setup and add cluster-free CI

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Prove that it works standalone without a cluster

**Files:** none (verification only)

- [ ] **Step 1: Make a clean clone with no path back to substrate**

```bash
rm -rf /tmp/agent-swarm-clean
git clone /Users/mofi/Documents/github.com/agent-substrate/agent-swarm /tmp/agent-swarm-clean
cd /tmp/agent-swarm-clean
env | grep -i 'GOFLAGS\|GOWORK' ; go env GOWORK
```
Expected: `GOWORK` is empty or `off`, and no `GOFLAGS` contains `-mod=vendor`. If a go.work is found, re-run with `GOWORK=off`.

- [ ] **Step 2: Run the full cluster-free suite from the clone**

Run: `make verify test build verify-local; echo "exit=$?"`
Expected: `exit=0`.

- [ ] **Step 3: Check local mode by eye**

Run `make run-local` in the background. Then:
- `curl -sI http://127.0.0.1:18090/ | head -3`: expect a 302 to `/chat`.
- `curl -s http://127.0.0.1:18090/chat | grep -c '<html'`: expect `1`.
- Take the presenter URL from the gateway log and load `/stage?key=…`.

Screenshot `/chat` and `/stage` with the project's headless browser flow, as done for the earlier fleet grid, and show them to the user. Stop the background process afterwards.

- [ ] **Step 4: Clean up**

`rm -rf /tmp/agent-swarm-clean`. No commit.

---

### Task 8: Cluster proof on substrate-poc (GATED — ask the user first)

This resets the 50 live actors on a shared cluster and takes about 20 minutes. **Stop and get the user's explicit go-ahead before Step 2.** Under the standing rules, a production deploy needs an explicit "redeploy" or equivalent from the user.

**Files:** none (verification only)

- [ ] **Step 1: Dry pre-checks (read-only)**

```bash
cd /Users/mofi/Documents/github.com/agent-substrate/agent-swarm
kubectl config current-context        # expect gke_mofilabs_us-west1-c_substrate-poc
kubectl get nodes -L ate.dev/substrate-version
bash -c 'source hack/lib.sh; substrate_labels_match "$(substrate_pin)" "$(kubectl get nodes -o jsonpath="{range .items[*]}{.metadata.labels.ate\.dev/substrate-version}{\"\n\"}{end}")" && echo pin-ok'
```
Expected: the right context, both nodes on `c7a1be17`, and `pin-ok`. Report this to the user with the confirmation request.

- [ ] **Step 2: Tear down the monorepo install from agent-swarm**

With `PROJECT_ID`, `BUCKET_NAME`, `KO_DOCKER_REPO` and `DEMO_DOMAIN` exported as for the monorepo install, run `make undeploy`.
Expected:
- the actors, conf-agent template, gateway and pool are removed
- the router flags and service account are left in place
- `go tool kubectl-ate get actors -a ate-demo-conf` lists none

- [ ] **Step 3: Deploy from agent-swarm**

Run: `make deploy`
Expected:
- the template is created with revision 2 and its golden snapshot is ready
- the gateway rolls out
- 50 actors are created

- [ ] **Step 4: End to end**

Export `PRESENTER_TOKEN` from the Secret, start a port-forward to `svc/conf-gateway` on 8090, and run `make e2e`.
Expected: all 13 steps pass on real Gemini.

Also check the LB by hand: `curl -sI http://<LB>/` returns a 302 to `/chat`. A chat that asks for a Python script with a deliberate typo should come back with a working script. This exercises the syntax-check retry that ships with this image.

- [ ] **Step 5: Report**

Tell the user the results:
- the image digests
- the e2e output summary
- the new repo path and commit count
- the reminder that they create the GitHub repo and push, and that removing `demos/conference` from substrate is a separate pull request

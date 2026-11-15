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
#
# Builds the temporary Agent Substrate PR #3150 objectstore-plugin image from a
# local checkout with PR #3150 cherry-picked onto v1.0.0, publishing to
# KO_DOCKER_REPO. Once Agent Substrate merges PR #3150 into an official release
# (v1.0.1+), agent-swarm bumps go.mod to that release and returns to 100% stock
# upstream release images.

set -o errexit -o nounset -o pipefail

# shellcheck source=hack/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

DEFAULT_SUBSTRATE_WORKTREE="/usr/local/google/home/mofi/Documents/github.com/agent-substrate/substrate/.worktrees/v1.0.0-pr3150"
SUBSTRATE_REPO="${1:-${SUBSTRATE_REPO:-${DEFAULT_SUBSTRATE_WORKTREE}}}"

if [[ -z "${KO_DOCKER_REPO:-}" ]]; then
  echo "error: KO_DOCKER_REPO must be set" >&2
  exit 1
fi

if ! command -v ko >/dev/null 2>&1; then
  echo "error: ko is required to build the objectstore-plugin image" >&2
  exit 1
fi

if [[ ! -d "${SUBSTRATE_REPO}" || ! -f "${SUBSTRATE_REPO}/cmd/objectstore-plugin/main.go" ]]; then
  echo "error: Substrate checkout with PR #3150 not found at '${SUBSTRATE_REPO}'." >&2
  echo "Expected a checkout of go.ate.dev/substrate with cmd/objectstore-plugin/main.go." >&2
  exit 1
fi

export KO_DEFAULTPLATFORMS="${KO_DEFAULTPLATFORMS:-linux/amd64}"

log_step "Building objectstore-plugin (PR #3150) from ${SUBSTRATE_REPO} via ko..."
img="$(cd "${SUBSTRATE_REPO}" && ko build --bare ./cmd/objectstore-plugin)"

if [[ -z "${img}" ]]; then
  echo "error: ko build failed to produce an image" >&2
  exit 1
fi

log_step "Built objectstore-plugin image: ${img}"
echo "${img}"

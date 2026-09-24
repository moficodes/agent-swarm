#!/usr/bin/env bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Tests the router flag check in install-demo-conference.sh without a cluster.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
ATE_DEMOS=()
ate_demo_flag_unhandled() { :; }
# shellcheck source=hack/install-demo-conference.sh
source "${ROOT}/hack/install-demo-conference.sh"

fail=0
expect() {
  local want="$1" args="$2" got=0
  demo-conference_router_flags_ok "${args}" >/dev/null || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: ${want} for ${args}" >&2
    fail=1
  fi
}

expect bad '["--mode=ingress"]'
expect bad '["--parked-request-budget=5s"]'
expect bad '["--parked-request-budget=59s"]'
expect ok  '["--parked-request-budget=60s"]'
expect ok  '["--parked-request-budget=2m"]'
expect bad '["--parked-request-budget=60s","--route-timeout=30s"]'
expect ok  '["--parked-request-budget=90s","--route-timeout=5m"]'
expect ok  '["--parked-request-budget","60s"]'
exit "${fail}"

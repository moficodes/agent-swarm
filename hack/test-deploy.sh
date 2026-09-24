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

# Tests the router flag/grace-period checks and the duration parser in
# install-demo-conference.sh without a cluster.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
ATE_DEMOS=()
ate_demo_flag_unhandled() { :; }
# shellcheck source=hack/install-demo-conference.sh
source "${ROOT}/hack/install-demo-conference.sh"

fail=0

# expect checks demo-conference_router_flags_ok against ARGS_JSON and an
# optional GRACE_SECONDS (default 120, i.e. already acceptable, so callers
# that only care about the duration flags don't have to think about grace).
expect() {
  # ${3-120}, not ${3:-120}: an explicitly empty third argument (used to
  # test an unreadable grace period) must stay empty, not fall back to the
  # default the way a merely-omitted argument does.
  local want="$1" args="$2" grace="${3-120}" got=0
  demo-conference_router_flags_ok "${args}" "${grace}" >/dev/null || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: ${want} for ${args} (grace=${grace})" >&2
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

# --route-timeout in its space-separated form.
expect ok  '["--parked-request-budget=90s","--route-timeout","5m"]'

# A present but unparseable budget is treated the same as a missing one.
expect bad '["--parked-request-budget=notaduration"]'

# A repeated flag: the last value wins, not the first. The first value here
# would pass on its own, so this only stays "bad" if the last one is used.
expect bad '["--parked-request-budget=90s","--parked-request-budget=5s"]'

# No args at all.
expect bad '[]'

# The grace-period requirement only applies once the duration flags are
# already acceptable (see demo-conference_router_flags_ok's comment).
expect ok  '["--parked-request-budget=90s","--route-timeout=5m"]' 120
expect bad '["--parked-request-budget=90s","--route-timeout=5m"]' 119
expect bad '["--parked-request-budget=90s","--route-timeout=5m"]' ""

# expect_grace checks demo-conference_grace_ok directly.
expect_grace() {
  local want="$1" input="$2" got=0
  demo-conference_grace_ok "${input}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: grace ${want} for '${input}'" >&2
    fail=1
  fi
}

expect_grace ok  120
expect_grace ok  180
expect_grace bad 119
expect_grace bad ""
expect_grace bad abc

# expect_seconds checks demo-conference_seconds directly.
expect_seconds() {
  local want="$1" input="$2" got
  # An unparseable input makes demo-conference_seconds return 1 with no
  # output; the `|| true` keeps that from tripping errexit here.
  got="$(demo-conference_seconds "${input}")" || true
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: demo-conference_seconds(${input}) = '${got}', want '${want}'" >&2
    fail=1
  fi
}

expect_seconds 90 1m30s
expect_seconds 3600 1h
expect_seconds "" 500ms
# A leading zero must not be read as an invalid octal literal.
expect_seconds 8 08s

exit "${fail}"

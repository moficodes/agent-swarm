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

# Tests the router flag/grace-period checks, the router patch builder, the
# stale actor template check, the template render and the duration parser in
# hack/deploy.sh without a cluster.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "${ROOT}"
# shellcheck source=hack/deploy.sh
source "${ROOT}/hack/deploy.sh"

fail=0

# expect checks conf_router_flags_ok against ARGS_JSON and an
# optional GRACE_SECONDS (default 120, i.e. already acceptable, so callers
# that only care about the duration flags don't have to think about grace).
expect() {
  # ${3-120}, not ${3:-120}: an explicitly empty third argument (used to
  # test an unreadable grace period) must stay empty, not fall back to the
  # default the way a merely-omitted argument does.
  local want="$1" args="$2" grace="${3-120}" got=0
  conf_router_flags_ok "${args}" "${grace}" >/dev/null || got=$?
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
# already acceptable (see conf_router_flags_ok's comment).
expect ok  '["--parked-request-budget=90s","--route-timeout=5m"]' 120
expect bad '["--parked-request-budget=90s","--route-timeout=5m"]' 119
expect bad '["--parked-request-budget=90s","--route-timeout=5m"]' ""

# expect_grace checks conf_grace_ok directly.
expect_grace() {
  local want="$1" input="$2" got=0
  conf_grace_ok "${input}" || got=$?
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

# expect_seconds checks conf_seconds directly.
expect_seconds() {
  local want="$1" input="$2" got
  # An unparseable input makes conf_seconds return 1 with no
  # output; the `|| true` keeps that from tripping errexit here.
  got="$(conf_seconds "${input}")" || true
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: conf_seconds(${input}) = '${got}', want '${want}'" >&2
    fail=1
  fi
}

expect_seconds 90 1m30s
expect_seconds 3600 1h
expect_seconds "" 500ms
# A leading zero must not be read as an invalid octal literal.
expect_seconds 8 08s

# expect_patch checks the JSON patch conf_router_patch builds for
# a Deployment, where WANT is "error" if it must refuse.
expect_patch() {
  local want="$1" deploy="$2" got
  shift 2
  if ! got="$(conf_router_patch "${deploy}" "$@" 2>/dev/null)"; then
    got=error
  fi
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: conf_router_patch(${deploy}) = '${got}', want '${want}'" >&2
    fail=1
  fi
}

# The router container is found by name, wherever it sits.
expect_patch '[{"op":"add","path":"/spec/template/spec/containers/1/args/-","value":"--parked-request-budget=60s"},{"op":"add","path":"/spec/template/spec/containers/1/args/-","value":"--route-timeout=5m"}]' \
  '{"spec":{"template":{"spec":{"containers":[{"name":"envoy","args":["-c"]},{"name":"atenet-router","args":["--mode=ingress"]}]}}}}' \
  --parked-request-budget=60s --route-timeout=5m
expect_patch '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--route-timeout=5m"}]' \
  '{"spec":{"template":{"spec":{"containers":[{"name":"atenet-router","args":[]},{"name":"envoy"}]}}}}' \
  --route-timeout=5m
# A container without args gets a new args array rather than an append.
expect_patch '[{"op":"add","path":"/spec/template/spec/containers/1/args","value":["--parked-request-budget=60s","--route-timeout=5m"]}]' \
  '{"spec":{"template":{"spec":{"containers":[{"name":"envoy","args":["-c"]},{"name":"atenet-router"}]}}}}' \
  --parked-request-budget=60s --route-timeout=5m
# No atenet-router container: refuse rather than patch another container.
expect_patch error \
  '{"spec":{"template":{"spec":{"containers":[{"name":"envoy","args":["-c"]}]}}}}' \
  --parked-request-budget=60s

# expect_template checks conf_template_current against the JSON
# kubectl-ate prints for an existing conf-agent template.
expect_template() {
  local want="$1" json="$2" got=0
  conf_template_current "${json}" >/dev/null 2>&1 || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == stale && "${got}" == 0 ]]; then
    echo "FAIL: template ${want} for ${json}" >&2
    fail=1
  fi
}
rev="${DEMO_CONF_TEMPLATE_REVISION}"
expect_template ok    '{"containers":[{"name":"conf-workload","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"${rev}"'"}]}]}'
# Templates from before the revision marker, with the old model proxy URL.
expect_template stale '{"containers":[{"name":"conf-workload","env":[{"name":"MODEL_PROXY_URL","value":"x"}],"securityContext":{"capabilities":{"add":["CHOWN","SETUID","SETGID","DAC_OVERRIDE"]}}}]}'
expect_template stale '{"containers":[{"name":"conf-workload"}]}'
# An older or newer revision.
expect_template stale '{"containers":[{"name":"conf-workload","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"$((rev - 1))"'"}]}]}'
expect_template stale '{"containers":[{"name":"conf-workload","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"$((rev + 1))"'"}]}]}'
# The marker on another container does not count.
expect_template stale '{"containers":[{"name":"sidecar","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"${rev}"'"}]}]}'
# The rendered template carries the current revision.
if ! conf_render manifests/actortemplate.yaml.tmpl | grep -A1 'name: CONF_TEMPLATE_REVISION' | grep -qF "value: \"${rev}\""; then
  echo "FAIL: rendered conf-agent template lacks CONF_TEMPLATE_REVISION ${rev}" >&2
  fail=1
fi
# The gateway learns the actor count from the install.
if ! DEMO_CONF_ACTORS=7 conf_render manifests/gateway.yaml.tmpl | grep -A1 'name: ACTOR_COUNT' | grep -qF 'value: "7"'; then
  echo "FAIL: rendered gateway manifest lacks ACTOR_COUNT from DEMO_CONF_ACTORS" >&2
  fail=1
fi

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

exit "${fail}"

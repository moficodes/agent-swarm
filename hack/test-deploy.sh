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
# stale actor template check, the template and dashboard renders, the GKE
# cluster name parser and the duration parser in
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

# The dashboard renders to valid JSON for one cluster, and every query and
# the logs panel filter on that cluster.
dash="$(DEMO_CONF_CLUSTER=poc-1 conf_render manifests/dashboard.json)"
if ! jq -e '.displayName == "Conference demo (poc-1)"' <<<"${dash}" >/dev/null; then
  echo "FAIL: rendered dashboard is not JSON named for cluster poc-1" >&2
  fail=1
fi
if [[ "${dash}" == *"\${CLUSTER}"* ]]; then
  echo "FAIL: rendered dashboard still contains \${CLUSTER}" >&2
  fail=1
fi
if ! jq -e '[.. | .prometheusQuery? // empty, .logsPanel?.filter // empty] | length > 0 and all(test("cluster=\\\"poc-1\\\"|cluster_name=\\\"poc-1\\\""))' \
    <<<"${dash}" >/dev/null; then
  echo "FAIL: a dashboard query does not filter on cluster poc-1" >&2
  fail=1
fi

# expect_range checks conf_workers_range_ok.
expect_range() {
  local want="$1" min="$2" max="$3" got=0
  conf_workers_range_ok "${min}" "${max}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: conf_workers_range_ok ${min} ${max}: want ${want}" >&2
    fail=1
  fi
}
expect_range ok  2 5
expect_range ok  1 1
expect_range ok  08 09   # leading zeros are decimal, not octal
expect_range bad 0 5
expect_range bad 3 2
expect_range bad "" 5
expect_range bad 2 ""
expect_range bad abc 5
expect_range bad -1 5

# expect_replicas checks conf_worker_replicas: a re-deploy keeps the pool's
# current size, clamped to the range; a new pool starts at the minimum.
expect_replicas() {
  local want="$1" got
  got="$(conf_worker_replicas "$2" "$3" "$4")"
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: conf_worker_replicas '$2' $3 $4 = '${got}', want ${want}" >&2
    fail=1
  fi
}
expect_replicas 2 ""  2 5
expect_replicas 4 4   2 5
expect_replicas 2 1   2 5
expect_replicas 3 7   2 3
expect_replicas 2 abc 2 5

# expect_apiservice checks conf_apiservice_ok against an APIService JSON.
expect_apiservice() {
  local want="$1" json="$2" got=0
  conf_apiservice_ok "${json}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: conf_apiservice_ok ${json}: want ${want}" >&2
    fail=1
  fi
}
expect_apiservice ok  ''
expect_apiservice ok  '{"spec":{"service":{"namespace":"ate-demo-conf","name":"conf-prometheus-adapter"}}}'
expect_apiservice bad '{"spec":{"service":{"namespace":"custom-metrics","name":"custom-metrics-stackdriver-adapter"}}}'
expect_apiservice bad '{"spec":{}}'   # served locally by kube-apiserver

# expect_apiservice_delete checks conf_apiservice_deletable: delete only
# when the read succeeded and the APIService is absent or ours.
expect_apiservice_delete() {
  local want="$1" rc="$2" json="$3" got=0
  conf_apiservice_deletable "${rc}" "${json}" || got=$?
  if [[ "${want}" == delete && "${got}" != 0 ]] || [[ "${want}" == keep && "${got}" == 0 ]]; then
    echo "FAIL: conf_apiservice_deletable ${rc} ${json}: want ${want}" >&2
    fail=1
  fi
}
expect_apiservice_delete delete 0 '{"spec":{"service":{"namespace":"ate-demo-conf","name":"conf-prometheus-adapter"}}}'
expect_apiservice_delete delete 0 ''
expect_apiservice_delete keep   0 '{"spec":{"service":{"namespace":"custom-metrics","name":"custom-metrics-stackdriver-adapter"}}}'
expect_apiservice_delete keep   1 ''   # the read failed: owner unknown

# The APIService is registered only after the adapter is ready, so a broken
# adapter never leaves an unavailable aggregated API on the cluster.
adapter_ready="$(grep -n 'rollout status deployment/conf-prometheus-adapter' hack/deploy.sh | head -1 | cut -d: -f1)"
apiservice_apply="$(grep -n 'autoscaling-apiservice.yaml.tmpl | run_kubectl_fatal apply' hack/deploy.sh | head -1 | cut -d: -f1)"
if [[ -z "${adapter_ready}" || -z "${apiservice_apply}" || "${apiservice_apply}" -le "${adapter_ready}" ]]; then
  echo "FAIL: deploy.sh applies the APIService (line ${apiservice_apply}) before the adapter is ready (line ${adapter_ready})" >&2
  fail=1
fi

# The autoscaling manifest carries the configured range; the WorkerPool
# carries the replicas deploy.sh chose and the pod requests.
auto="$(DEMO_CONF_MIN_WORKERS=3 DEMO_CONF_MAX_WORKERS=4 conf_render manifests/autoscaling.yaml.tmpl)"
for want in "minReplicas: 3" "maxReplicas: 4" "name: conf-workers" "kind: WorkerPool" 'averageValue: "1"' \
            'ate_worker_state="assigned"' "atenet_router_parking_active" "conf_worker_demand"; do
  if ! grep -qF -- "${want}" <<<"${auto}"; then
    echo "FAIL: rendered autoscaling manifest lacks ${want}" >&2
    fail=1
  fi
done
pool_yaml="$(DEMO_CONF_WORKER_REPLICAS=4 conf_render manifests/workerpool.yaml.tmpl)"
for want in "replicas: 4" "cpu: 500m" "memory: 768Mi"; do
  if ! grep -qF -- "${want}" <<<"${pool_yaml}"; then
    echo "FAIL: rendered workerpool manifest lacks ${want}" >&2
    fail=1
  fi
done
if grep -qE '^[[:space:]]+limits:' <<<"${pool_yaml}"; then
  echo "FAIL: workerpool manifest sets limits; ateom would report them as capacity" >&2
  fail=1
fi
# Without a chosen replica count (the delete path), the pool renders at the minimum.
if ! DEMO_CONF_MIN_WORKERS=3 conf_render manifests/workerpool.yaml.tmpl | grep -qF "replicas: 3"; then
  echo "FAIL: workerpool manifest does not default to DEMO_CONF_MIN_WORKERS" >&2
  fail=1
fi

# expect_cluster checks conf_gke_cluster_name.
expect_cluster() {
  local want="$1" entry="$2" got
  got="$(conf_gke_cluster_name "${entry}")"
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: conf_gke_cluster_name '${entry}' = '${got}', want '${want}'" >&2
    fail=1
  fi
}

expect_cluster substrate-poc gke_mofilabs_us-west1-c_substrate-poc
# Only the first three underscores separate fields; the name keeps the rest.
expect_cluster my_cluster gke_proj_us-central1_my_cluster
expect_cluster "" kind-kind
expect_cluster "" ""

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

# expect_dirty checks substrate_labels_match's third, dirty-accepting
# argument: a "-dirty" label whose prefix (before "-dirty") is a prefix of
# PIN is accepted only when ALLOW is true.
expect_dirty() {
  local want="$1" labels="$2" allow="$3" got=0
  substrate_labels_match c7a1be177d0f "${labels}" "${allow}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: dirty labels ${want} for '${labels//$'\n'/,}' allow=${allow}" >&2
    fail=1
  fi
}
expect_dirty bad 'c7a1be17-dirty' false
expect_dirty bad 'c7a1be17-dirty' ''                          # omitted defaults to strict
expect_dirty ok  'c7a1be17-dirty' true
expect_dirty bad '0123abcd-dirty' true                        # dirty prefix must still match the pin
expect_dirty ok  $'c7a1be17\nc7a1be17-dirty' true              # mix of clean pin and dirty pin
expect_dirty bad $'c7a1be17\nc7a1be17-dirty' false             # ...only with the override

# expect_kind checks substrate_label_mismatch_kind, which classifies a
# substrate_labels_match failure so conf_check_substrate can tell an
# uninstalled Substrate from a dirty install from an actual version bump.
expect_kind() {
  local want="$1" labels="$2" got
  got="$(substrate_label_mismatch_kind c7a1be177d0f "${labels}")"
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: mismatch kind for '${labels//$'\n'/,}' = '${got}', want '${want}'" >&2
    fail=1
  fi
}
expect_kind unlabeled ''
expect_kind unlabeled $'\n\n'
expect_kind dirty 'c7a1be17-dirty'
expect_kind dirty $'c7a1be17-dirty\nc7a1be17-dirty'
expect_kind mismatch $'c7a1be17\n0123abcd'
expect_kind mismatch '0123abcd-dirty'
expect_kind mismatch $'c7a1be17-dirty\n0123abcd'

# substrate_repo_clean flags an uncommitted change in SUBSTRATE_REPO before
# conf_check_substrate installs from it: install-ate.sh would stamp nodes
# "-dirty" from a modified checkout, which substrate_labels_match then
# refuses by default.
tmp_repo="$(mktemp -d)"
(
  cd "${tmp_repo}"
  git init -q
  git config user.email test@example.com
  git config user.name test
  echo one >file.txt
  git add file.txt
  git commit -q -m init
)
if ! substrate_repo_clean "${tmp_repo}"; then
  echo "FAIL: substrate_repo_clean should accept a clean repo" >&2
  fail=1
fi
echo two >>"${tmp_repo}/file.txt"
if substrate_repo_clean "${tmp_repo}"; then
  echo "FAIL: substrate_repo_clean should reject a dirty repo" >&2
  fail=1
fi
rm -rf "${tmp_repo}"

# log_target_context must report an explicit KUBECTL_CONTEXT itself, since
# `kubectl --context=X config current-context` prints the kubeconfig file's
# current-context, not X.
out="$(KUBECTL_CONTEXT=my-ctx log_target_context 2>&1)"
if [[ "${out}" != *"Target cluster context: my-ctx"* ]]; then
  echo "FAIL: log_target_context with KUBECTL_CONTEXT set: ${out}" >&2
  fail=1
fi

# substrate_pin reads the pinned commit from go.mod.
if [[ "$(substrate_pin)" != "c7a1be177d0f" ]]; then
  echo "FAIL: substrate_pin = '$(substrate_pin)', want c7a1be177d0f" >&2
  fail=1
fi

exit "${fail}"

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
# hack/deploy.sh, and hack/create-cluster.sh's version pick and gcloud
# arguments, without a cluster.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "${ROOT}"
# shellcheck source=hack/deploy.sh
source "${ROOT}/hack/deploy.sh"
# shellcheck source=hack/create-cluster.sh
source "${ROOT}/hack/create-cluster.sh"

fail=0

# expect checks conf_router_flags_ok against ARGS_JSON, an optional
# GRACE_SECONDS (default 120, i.e. already acceptable, so callers that only
# care about the duration flags don't have to think about grace), and an
# optional SPIFFE_ID (default empty).
expect() {
  # ${3-120}, not ${3:-120}: an explicitly empty third argument (used to
  # test an unreadable grace period) must stay empty, not fall back to the
  # default the way a merely-omitted argument does.
  local want="$1" args="$2" grace="${3-120}" spiffe="${4-}" got=0
  conf_router_flags_ok "${args}" "${grace}" "${spiffe}" >/dev/null || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: ${want} for ${args} (grace=${grace} spiffe=${spiffe})" >&2
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

# --ingress-allowed-spiffe-ids must include DEMO_CONF_ROUTER_SPIFFE_ID when
# checked (both comma-separated and repeated forms are accepted).
expect bad '["--parked-request-budget=90s","--route-timeout=5m"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}"
expect ok  '["--parked-request-budget=90s","--route-timeout=5m","--ingress-allowed-spiffe-ids='"${DEMO_CONF_ROUTER_SPIFFE_ID}"'"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}"
expect ok  '["--parked-request-budget=90s","--ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/other/sa/x,'"${DEMO_CONF_ROUTER_SPIFFE_ID}"'"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}"
expect ok  '["--parked-request-budget=90s","--ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/other/sa/x","--ingress-allowed-spiffe-ids","'"${DEMO_CONF_ROUTER_SPIFFE_ID}"'"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}"
expect bad '["--parked-request-budget=90s","--ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/other/sa/x"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}"

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
# The rendered template carries the current revision and defaults to /tmp/workspace.
tmpl_default="$(DEMO_CONF_STORAGE_CLASS="" conf_render manifests/actortemplate.yaml.tmpl)"
if ! grep -A1 'name: CONF_TEMPLATE_REVISION' <<<"${tmpl_default}" | grep -qF "value: \"${rev}\""; then
  echo "FAIL: rendered conf-agent template lacks CONF_TEMPLATE_REVISION ${rev}" >&2
  fail=1
fi
if ! grep -A1 'name: SANDBOX_WORKSPACE' <<<"${tmpl_default}" | grep -qF 'value: "/tmp/workspace"'; then
  echo "FAIL: default conf-agent template lacks SANDBOX_WORKSPACE=/tmp/workspace" >&2
  fail=1
fi
if grep -qE 'volumeMounts:|volumes:' <<<"${tmpl_default}"; then
  echo "FAIL: default conf-agent template should omit external volume fields" >&2
  fail=1
fi
# DEMO_CONF_STORAGE_CLASS=none also keeps /tmp/workspace without external volumes.
tmpl_none="$(DEMO_CONF_STORAGE_CLASS=none conf_render manifests/actortemplate.yaml.tmpl)"
if grep -qE 'volumeMounts:|volumes:' <<<"${tmpl_none}"; then
  echo "FAIL: DEMO_CONF_STORAGE_CLASS=none should omit external volume fields" >&2
  fail=1
fi
# With DEMO_CONF_STORAGE_CLASS set, conf_render mounts a 1 GiB external volume at /workspace.
tmpl_vol="$(DEMO_CONF_STORAGE_CLASS=substrate-volumepool-sc conf_render manifests/actortemplate.yaml.tmpl)"
for want in 'value: "/workspace"' 'volumeMounts:' 'mountPath: /workspace' 'volumes:' \
            'storageClassName: substrate-volumepool-sc' 'capacityBytes: 1073741824'; do
  if ! grep -qF -- "${want}" <<<"${tmpl_vol}"; then
    echo "FAIL: volume-enabled conf-agent template lacks ${want}" >&2
    fail=1
  fi
done
# conf_template_current enforces matching external volume StorageClass as well as revision.
if ! DEMO_CONF_STORAGE_CLASS=substrate-volumepool-sc conf_template_current \
    '{"containers":[{"name":"conf-workload","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"${rev}"'"}]}],"volumes":[{"name":"workspace","external":{"template":{"storageClassName":"substrate-volumepool-sc","capacityBytes":1073741824}}}]}'; then
  echo "FAIL: conf_template_current rejected matching DEMO_CONF_STORAGE_CLASS" >&2
  fail=1
fi
if DEMO_CONF_STORAGE_CLASS=substrate-volumepool-sc conf_template_current \
    '{"containers":[{"name":"conf-workload","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"${rev}"'"}]}]}'; then
  echo "FAIL: conf_template_current accepted missing external volume when DEMO_CONF_STORAGE_CLASS is set" >&2
  fail=1
fi
if DEMO_CONF_STORAGE_CLASS="" conf_template_current \
    '{"containers":[{"name":"conf-workload","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"${rev}"'"}]}],"volumes":[{"name":"workspace","external":{"template":{"storageClassName":"substrate-volumepool-sc"}}}]}'; then
  echo "FAIL: conf_template_current accepted external volume when DEMO_CONF_STORAGE_CLASS is unset" >&2
  fail=1
fi
# conf_check_template ignores stderr warnings (e.g. kubectl-ate's --token-file warning)
# when "-o json" succeeds on stdout, and still treats NotFound on stderr as absent.
if ! (
  DEMO_CONF_STORAGE_CLASS=""
  run_kubectl_ate() {
    echo "Warning: no --token-file given; acting as ate-client." >&2
    echo '{"containers":[{"name":"conf-workload","env":[{"name":"CONF_TEMPLATE_REVISION","value":"'"${rev}"'"}]}]}'
  }
  conf_check_template
); then
  echo "FAIL: conf_check_template failed when kubectl-ate emitted a stderr warning" >&2
  fail=1
fi
if ! (
  DEMO_CONF_STORAGE_CLASS=""
  run_kubectl_ate() {
    echo "Error from server (NotFound): actor template conf-agent not found" >&2
    return 1
  }
  conf_check_template
); then
  echo "FAIL: conf_check_template did not ignore NotFound on stderr" >&2
  fail=1
fi

# expect_preview checks conf_ateapi_preview_enabled.
expect_preview() {
  local want="$1" args="$2" got=0
  conf_ateapi_preview_enabled "${args}" "ExternalVolumes" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: conf_ateapi_preview_enabled ${want} for ${args}" >&2
    fail=1
  fi
}
expect_preview bad '[]'
expect_preview bad '["--preview=OtherFeature"]'
expect_preview ok  '["--preview=ExternalVolumes"]'
expect_preview ok  '["--preview=OtherFeature,ExternalVolumes"]'
expect_preview ok  '["--preview","ExternalVolumes"]'
expect_preview ok  '["--preview=ALL"]'
# The gateway learns the actor count from the install.
if ! DEMO_CONF_ACTORS=7 conf_render manifests/gateway.yaml.tmpl | grep -A1 'name: ACTOR_COUNT' | grep -qF 'value: "7"'; then
  echo "FAIL: rendered gateway manifest lacks ACTOR_COUNT from DEMO_CONF_ACTORS" >&2
  fail=1
fi
# The gateway manifest passes optional Google presenter auth and 10 RPM rate limits.
gw_rendered="$(ALLOWED_GOOGLE_DOMAINS=google.com,event.goog GOOGLE_CLIENT_ID=cid FIREBASE_API_KEY=fkey conf_render manifests/gateway.yaml.tmpl)"
for pair in 'ALLOWED_GOOGLE_DOMAINS:google.com,event.goog' 'GOOGLE_CLIENT_ID:cid' 'FIREBASE_API_KEY:fkey' 'CHAT_RATE_LIMIT_RPM:10' 'SESSION_RATE_LIMIT_RPM:10'; do
  k="${pair%%:*}"
  v="${pair#*:}"
  if ! grep -A1 "name: ${k}" <<<"${gw_rendered}" | grep -qF "value: \"${v}\""; then
    echo "FAIL: rendered gateway manifest lacks ${k}=${v}" >&2
    fail=1
  fi
done

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
# A second demo namespace (e.g. ate-demo-conf-live) shares ate-demo-conf's
# adapter on deploy and must not delete ate-demo-conf's APIService on teardown.
if ! DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_apiservice_ok '{"spec":{"service":{"namespace":"ate-demo-conf","name":"conf-prometheus-adapter"}}}'; then
  echo "FAIL: conf_apiservice_ok rejected sharing ate-demo-conf's conf-prometheus-adapter" >&2
  fail=1
fi
if DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_apiservice_deletable 0 '{"spec":{"service":{"namespace":"ate-demo-conf","name":"conf-prometheus-adapter"}}}'; then
  echo "FAIL: conf_apiservice_deletable allowed ate-demo-conf-live to delete ate-demo-conf's APIService" >&2
  fail=1
fi

# DEMO_CONF_NAMESPACE=ate-demo-conf-live isolates the worker pool, workload
# selector, actor template, RBAC names, external metric, SPIFFE ID, and static IP.
live_pool="$(DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_render manifests/workerpool.yaml.tmpl)"
for want in "name: ate-demo-conf-live" "name: conf-workers-live" "workload: conference-live"; do
  if ! grep -qF -- "${want}" <<<"${live_pool}"; then
    echo "FAIL: live workerpool manifest lacks ${want}" >&2
    fail=1
  fi
done
live_tmpl="$(DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_render manifests/actortemplate.yaml.tmpl)"
for want in "atespace: ate-demo-conf-live" "name: conf-agent-live" "workload: conference-live"; do
  if ! grep -qF -- "${want}" <<<"${live_tmpl}"; then
    echo "FAIL: live actortemplate manifest lacks ${want}" >&2
    fail=1
  fi
done
live_gw="$(DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_render manifests/gateway.yaml.tmpl)"
for want in "name: ate-demo-conf-live-conf-gateway-ate-client" "name: ate-demo-conf-live-conf-gateway-trust-bundles" \
            'value: "ate-demo-conf-live"' 'value: "conf-workers-live"' "- conf-workers-live" \
            "name: METRICS_PORT" 'value: "9090"' "containerPort: 9090"; do
  if ! grep -qF -- "${want}" <<<"${live_gw}"; then
    echo "FAIL: live gateway manifest lacks ${want}" >&2
    fail=1
  fi
done
if [[ "$(DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_adapter_metrics_query)" != *'conf_gateway_parked_requests{namespace="ate-demo-conf-live"}'* ]]; then
  echo "FAIL: conf_adapter_metrics_query does not scope parked requests to DEMO_CONF_NAMESPACE" >&2
  fail=1
fi
shared_prom="$(DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_render_shared_prometheus ate-demo-conf)"
for want in "namespace: ate-demo-conf" "name: conf-prometheus-pod-reader" "job_name: conf-gateway"; do
  if ! grep -qF -- "${want}" <<<"${shared_prom}"; then
    echo "FAIL: conf_render_shared_prometheus lacks ${want}" >&2
    fail=1
  fi
done
if grep -qF -- "name: conf-prometheus-adapter" <<<"${shared_prom}"; then
  echo "FAIL: conf_render_shared_prometheus leaked conf-prometheus-adapter objects" >&2
  fail=1
fi
live_ing="$(DEMO_CONF_NAMESPACE=ate-demo-conf-live DEMO_DOMAIN=live.swarm.example.com DEMO_CONF_STATIC_IP_NAME=live-ip conf_render manifests/gateway-ingress.yaml.tmpl)"
if ! grep -qF 'kubernetes.io/ingress.global-static-ip-name: "live-ip"' <<<"${live_ing}"; then
  echo "FAIL: live ingress manifest lacks global-static-ip-name annotation" >&2
  fail=1
fi
live_hpa="$(DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_render_hpa)"
for want in "kind: HorizontalPodAutoscaler" "name: conf-workers-live" "namespace: ate-demo-conf-live" "name: conf_worker_demand_live"; do
  if ! grep -qF -- "${want}" <<<"${live_hpa}"; then
    echo "FAIL: live HPA manifest lacks ${want}" >&2
    fail=1
  fi
done
if [[ "$(DEMO_CONF_NAMESPACE=ate-demo-conf-live conf_router_spiffe_id)" != "spiffe://cluster.local/ns/ate-demo-conf-live/sa/conf-gateway" ]]; then
  echo "FAIL: conf_router_spiffe_id did not follow DEMO_CONF_NAMESPACE" >&2
  fail=1
fi

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
for want in "minReplicas: 3" "maxReplicas: 4" "name: conf-workers" "apiVersion: ate.dev/v1" "kind: WorkerPool" 'averageValue: "1"' \
            'ate_worker_state="partial"' 'ate_worker_state="at_capacity"' \
            "job_name: conf-gateway" "name: conf-prometheus-pod-reader" \
            'ceil((max(conf_gateway_parked_requests{namespace="ate-demo-conf"}) or sum(atenet_router_parking_active) or vector(0)) / 4)' "conf_worker_demand"; do
  if ! grep -qF -- "${want}" <<<"${auto}"; then
    echo "FAIL: rendered autoscaling manifest lacks ${want}" >&2
    fail=1
  fi
done
# Substrate v0.2.0 removed the "assigned" worker state; a query on it reads 0.
if grep -qF 'ate_worker_state="assigned"' <<<"${auto}"; then
  echo "FAIL: rendered autoscaling manifest queries the removed assigned worker state" >&2
  fail=1
fi
if ! grep -qF "or vector(0)) / 2)" <<<"$(DEMO_CONF_ACTORS_PER_WORKER=2 conf_render manifests/autoscaling.yaml.tmpl)"; then
  echo "FAIL: autoscaling manifest does not divide parked turns by DEMO_CONF_ACTORS_PER_WORKER" >&2
  fail=1
fi
if ! conf_render manifests/gateway.yaml.tmpl | grep -A1 -F "name: ACTORS_PER_WORKER" | grep -qF 'value: "4"'; then
  echo "FAIL: gateway manifest does not pass ACTORS_PER_WORKER=4" >&2
  fail=1
fi
pool_yaml="$(DEMO_CONF_WORKER_REPLICAS=4 conf_render manifests/workerpool.yaml.tmpl)"
for want in 'ate.dev/api-access: "true"' "apiVersion: ate.dev/v1" "replicas: 4" "sandboxClasses:" "- name: gvisor" "cpu: 500m" "memory: 2304Mi"; do
  if ! grep -qF -- "${want}" <<<"${pool_yaml}"; then
    echo "FAIL: rendered workerpool manifest lacks ${want}" >&2
    fail=1
  fi
done
# ateom reports its limits as capacity and hosts actors until it runs out:
# the CPU limit must fit four 1-CPU conf-agent actors but not five, and
# there must be no memory limit for a sandbox to OOM-kill the worker against.
pool_limits="$(sed -n '/^[[:space:]]*limits:/,$p' <<<"${pool_yaml}")"
if ! grep -qF "cpu: 4500m" <<<"${pool_limits}"; then
  echo "FAIL: workerpool manifest lacks the 4500m CPU limit that caps it at four actors" >&2
  fail=1
fi
if ! DEMO_CONF_ACTORS_PER_WORKER=1 conf_render manifests/workerpool.yaml.tmpl | grep -qF "cpu: 1500m"; then
  echo "FAIL: workerpool CPU limit does not follow DEMO_CONF_ACTORS_PER_WORKER" >&2
  fail=1
fi
if grep -qF "memory:" <<<"${pool_limits}"; then
  echo "FAIL: workerpool manifest sets a memory limit" >&2
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
expect_labels ok  'v0.1.0-12-gc7a1be17'   # git describe form after a tag
expect_labels bad 'v0.1.0-12-g0123abcd'
expect_labels bad 'v0.1.0'                # a tag is not a commit pin

# expect_tag_labels checks substrate_labels_match against a release tag pin,
# which a node label must equal exactly.
expect_tag_labels() {
  local want="$1" labels="$2" allow="${3:-false}" got=0
  substrate_labels_match v0.2.0 "${labels}" "${allow}" || got=$?
  if [[ "${want}" == ok && "${got}" != 0 ]] || [[ "${want}" == bad && "${got}" == 0 ]]; then
    echo "FAIL: tag labels ${want} for '${labels//$'\n'/,}' allow=${allow}" >&2
    fail=1
  fi
}
expect_tag_labels ok  $'v0.2.0\nv0.2.0'
expect_tag_labels ok  $'v0.2.0\n'
expect_tag_labels bad 'v0.2'              # a prefix of the tag is not a match
expect_tag_labels bad 'v0.2.0-rc.1'
expect_tag_labels bad 'v0.2.0-3-g0123abcd' # commits after the tag
expect_tag_labels bad 'v0.2.0-dirty'
expect_tag_labels ok  'v0.2.0-dirty' true
expect_tag_labels bad $'v0.2.0\nv0.1.0'

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
expect_kind dirty 'v0.1.0-12-gc7a1be17-dirty'
if [[ "$(substrate_label_mismatch_kind v0.2.0 'v0.2.0-dirty')" != dirty ]]; then
  echo "FAIL: mismatch kind for tag pin 'v0.2.0-dirty' should be dirty" >&2
  fail=1
fi

# substrate_repo_clean flags an uncommitted change in SUBSTRATE_REPO before
# conf_check_substrate installs from it: ate-setup would stamp binaries
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

# substrate_pin reads the pinned tag (or commit) from go.mod.
if [[ "$(substrate_pin)" != "v1.0.0" ]]; then
  echo "FAIL: substrate_pin = '$(substrate_pin)', want v1.0.0" >&2
  fail=1
fi

# expect_ver_out checks substrate_version_from_output against ateapi --version strings.
expect_ver_out() {
  local want="$1" input="$2" got
  got="$(substrate_version_from_output "${input}")"
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: substrate_version_from_output '${input}' = '${got}', want '${want}'" >&2
    fail=1
  fi
}
expect_ver_out v1.0.0 "v1.0.0 commit=b34fbee742906494b5eb4f1f36987176bf8770a4 built=2026-11-05T23:00:00Z linux/amd64"
expect_ver_out v1.0.0-dirty "v1.0.0-dirty commit=b34fbee742906494b5eb4f1f36987176bf8770a4 built=2026-11-05T23:00:00Z linux/amd64"
expect_ver_out v1.0.0 "go.ate.dev/substrate.Version=v1.0.0 commit=b34fbee742906494b5eb4f1f36987176bf8770a4 built=2026-11-05T23:00:00Z linux/amd64"
expect_ver_out v1.0.0-dirty "go.ate.dev/substrate.Version=v1.0.0+dirty commit=b34fbee742906494b5eb4f1f36987176bf8770a4-dirty built=2026-11-05T23:00:00Z linux/amd64"
expect_ver_out v1.0.0-rc.2 "v1.0.0-rc.2 commit=e59281c766d547689482a1fce666ca3f0f6bd008 built=2026-11-04T00:50:01Z linux/amd64"
expect_ver_out v1.0.0-rc.2-dirty "v1.0.0-rc.2-dirty commit=e59281c766d547689482a1fce666ca3f0f6bd008 built=2026-11-04T00:50:01Z linux/amd64"
expect_ver_out v1.0.0-rc.2 "go.ate.dev/substrate.Version=v1.0.0-rc.2 commit=e59281c766d547689482a1fce666ca3f0f6bd008 built=2026-11-04T00:50:01Z linux/amd64"
expect_ver_out v1.0.0-rc.2-dirty "go.ate.dev/substrate.Version=v1.0.0-rc.2+dirty commit=e59281c766d547689482a1fce666ca3f0f6bd008-dirty built=2026-11-04T00:50:01Z linux/amd64"
expect_ver_out v1.0.0-rc.1 "v1.0.0-rc.1 commit=c27cb01590a11b2f50489971985ba033b4c9eff7 built=2026-10-29T20:00:00Z linux/amd64"
expect_ver_out v1.0.0-rc.1-dirty "v1.0.0-rc.1-dirty commit=c27cb01590a11b2f50489971985ba033b4c9eff7 built=2026-10-29T20:00:00Z linux/amd64"
expect_ver_out v1.0.0-rc.1 "go.ate.dev/substrate.Version=v1.0.0-rc.1 commit=c27cb01590a11b2f50489971985ba033b4c9eff7 built=2026-10-29T20:00:00Z linux/amd64"
expect_ver_out v1.0.0-rc.1-dirty "go.ate.dev/substrate.Version=v1.0.0-rc.1+dirty commit=c27cb01590a11b2f50489971985ba033b4c9eff7-dirty built=2026-10-29T20:00:00Z linux/amd64"
expect_ver_out v0.6.0 "go.ate.dev/substrate.Version=v0.6.0 commit=d3f2363590a11b2f50489971985ba033b4c9eff7 built=2026-10-21T20:00:00Z linux/amd64"
expect_ver_out v0.6.0-dirty "go.ate.dev/substrate.Version=v0.6.0+dirty commit=d3f2363590a11b2f50489971985ba033b4c9eff7-dirty built=2026-10-21T20:00:00Z linux/amd64"
expect_ver_out v0.5.0 "github.com/agent-substrate/substrate.Version=v0.5.0 commit=e8d40c33f96f4b7a4984bfea7334a36ab5b45b7b built=2026-10-14T21:12:11Z linux/amd64"
expect_ver_out v0.5.0-dirty "github.com/agent-substrate/substrate.Version=v0.5.0+dirty commit=e8d40c33f96f4b7a4984bfea7334a36ab5b45b7b-dirty built=2026-10-14T21:12:11Z linux/amd64"
expect_ver_out e8d40c33f96f "github.com/agent-substrate/substrate.Version=v0.5.1-0.20261015000000-e8d40c33f96f commit=e8d40c33f96f4b7a4984bfea7334a36ab5b45b7b built=2026-10-15T00:00:00Z linux/amd64"
expect_ver_out e8d40c33f96f-dirty "github.com/agent-substrate/substrate.Version=v0.5.1-0.20261015000000-e8d40c33f96f+dirty commit=e8d40c33f96f4b7a4984bfea7334a36ab5b45b7b-dirty built=2026-10-15T00:00:00Z linux/amd64"
expect_ver_out v0.4.0 "v0.4.0 commit=756c2a53741121e728f4cc3066c8a19e575b4919 built=2026-10-07T22:53:25Z linux/amd64"
expect_ver_out "" ""

# expect_pin checks substrate_pin's parsing of go.mod versions, with go
# stubbed to report VERSION: a pseudo-version yields its commit, whether it
# follows no tag, a release tag, or a pre-release tag.
expect_pin() {
  local version="$1" want="$2" got
  got="$(go() { echo "${version}"; }; substrate_pin)"
  if [[ "${got}" != "${want}" ]]; then
    echo "FAIL: substrate_pin for ${version} = '${got}', want '${want}'" >&2
    fail=1
  fi
}
expect_pin v0.0.0-20260918213005-c7a1be177d0f c7a1be177d0f
expect_pin v0.2.1-0.20260926230214-ed6d2a1fc8ae ed6d2a1fc8ae
expect_pin v0.3.0-rc.1.0.20260926230214-ed6d2a1fc8ae ed6d2a1fc8ae
expect_pin v0.2.0 v0.2.0
expect_pin v0.3.0-rc.1 v0.3.0-rc.1
expect_pin v0.3.0 v0.3.0
expect_pin v0.4.0 v0.4.0
expect_pin v0.5.0 v0.5.0
expect_pin v0.6.0 v0.6.0
expect_pin v1.0.0-rc.1 v1.0.0-rc.1
expect_pin v1.0.0-rc.2 v1.0.0-rc.2
expect_pin v1.0.0 v1.0.0

# cc_pick_version takes the newest release of the minor version from gcloud's
# newest-first list, and fails when the channel has none.
versions="1.37.1-gke.100;1.37.0-gke.3503000;1.36.4-gke.1495000"
if [[ "$(cc_pick_version 1.37 "${versions}")" != 1.37.1-gke.100 ]]; then
  echo "FAIL: cc_pick_version 1.37 did not pick 1.37.1-gke.100" >&2
  fail=1
fi
if [[ "$(cc_pick_version 1.36 "${versions}")" != 1.36.4-gke.1495000 ]]; then
  echo "FAIL: cc_pick_version 1.36 did not pick 1.36.4-gke.1495000" >&2
  fail=1
fi
if cc_pick_version 1.3 "${versions}" >/dev/null || cc_pick_version 1.38 "${versions}" >/dev/null; then
  echo "FAIL: cc_pick_version matched a minor version the list lacks" >&2
  fail=1
fi
# The cluster gets the exclusion that keeps GKE from upgrading worker nodes.
create_args="$(PROJECT_ID=p CLUSTER_NAME=c cc_create_args 1.37.0-gke.1)"
for want in c "--project p" "--release-channel rapid" "--cluster-version 1.37.0-gke.1" \
            "--num-nodes 2" "--machine-type c3-standard-4" \
            "--workload-pool p.svc.id.goog" "--enable-nested-virtualization"; do
  if ! tr '\n' ' ' <<<"${create_args}" | grep -qF -- "${want}"; then
    echo "FAIL: cc_create_args lacks ${want}" >&2
    fail=1
  fi
done
mega_create_args="$(DEMO_CONF_PROFILE=megaswarm PROJECT_ID=p CLUSTER_NAME=c cc_create_args 1.37.0-gke.1)"
for want in "--num-nodes 16" "--machine-type c3-standard-8"; do
  if ! tr '\n' ' ' <<<"${mega_create_args}" | grep -qF -- "${want}"; then
    echo "FAIL: megaswarm cc_create_args lacks ${want}" >&2
    fail=1
  fi
done
if ! conf_validate_profile || ! DEMO_CONF_PROFILE=swarm conf_validate_profile || ! DEMO_CONF_PROFILE=megaswarm conf_validate_profile || ! DEMO_CONF_PROFILE=hyperswarm conf_validate_profile; then
  echo "FAIL: conf_validate_profile rejected a valid profile" >&2
  fail=1
fi
hyper_create_args="$(DEMO_CONF_PROFILE=hyperswarm PROJECT_ID=p CLUSTER_NAME=c cc_create_args 1.37.0-gke.1)"
for want in "--num-nodes 64" "--machine-type n4-highmem-64" "--disk-type hyperdisk-balanced" "--disk-size 300"; do
  if ! tr '\n' ' ' <<<"${hyper_create_args}" | grep -qF -- "${want}"; then
    echo "FAIL: hyperswarm cc_create_args lacks ${want}" >&2
    fail=1
  fi
done
hyper_multi_args="$(DEMO_CONF_PROFILE=hyperswarm NODE_LOCATIONS=us-west1-b,us-west1-c PROJECT_ID=p CLUSTER_NAME=c cc_create_args 1.37.0-gke.1)"
if ! tr '\n' ' ' <<<"${hyper_multi_args}" | grep -qF -- "--num-nodes 32"; then
  echo "FAIL: hyperswarm 2-zone cc_create_args did not divide 64 nodes into 32 per zone" >&2
  fail=1
fi
hyper_gw="$(DEMO_CONF_PROFILE=hyperswarm conf_render manifests/gateway.yaml.tmpl)"
for pair in 'ACTOR_COUNT:16777216' 'ACTORS_PER_WORKER:64' 'HUMAN_SLOT_STRIDE:4096' 'SUSPEND_AFTER:3s'; do
  k="${pair%%:*}"
  v="${pair#*:}"
  if ! grep -A1 "name: ${k}" <<<"${hyper_gw}" | grep -qF "value: \"${v}\""; then
    echo "FAIL: hyperswarm gateway manifest lacks ${k}=${v}" >&2
    fail=1
  fi
done
hyper_pool="$(DEMO_CONF_PROFILE=hyperswarm conf_render manifests/workerpool.yaml.tmpl)"
for want in "replicas: 4096" "cpu: 750m" "memory: 4096Mi" "cpu: 1290m"; do
  if ! grep -qF -- "${want}" <<<"${hyper_pool}"; then
    echo "FAIL: hyperswarm workerpool manifest lacks ${want}" >&2
    fail=1
  fi
done
hyper_tmpl="$(DEMO_CONF_PROFILE=hyperswarm conf_render manifests/actortemplate.yaml.tmpl)"
for want in "cpuMillicores: 20" "memoryBytes: 134217728"; do
  if ! grep -qF -- "${want}" <<<"${hyper_tmpl}"; then
    echo "FAIL: hyperswarm actortemplate manifest lacks ${want}" >&2
    fail=1
  fi
done
hyper_human_tmpl="$(DEMO_CONF_PROFILE=hyperswarm conf_render_human_template manifests/actortemplate.yaml.tmpl)"
for want in "name: conf-agent-human" "cpuMillicores: 500" "memoryBytes: 536870912"; do
  if ! grep -qF -- "${want}" <<<"${hyper_human_tmpl}"; then
    echo "FAIL: hyperswarm human actortemplate manifest lacks ${want}" >&2
    fail=1
  fi
done
if DEMO_CONF_PROFILE=hyperswarm conf_router_flags_ok '["--parked-request-budget=90s","--route-timeout=5m","--parked-request-max=4096","--ingress-allowed-spiffe-ids='"${DEMO_CONF_ROUTER_SPIFFE_ID}"'"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}" >/dev/null; then
  echo "FAIL: hyperswarm conf_router_flags_ok did not require --parked-request-max=65536" >&2
  fail=1
fi
if ! DEMO_CONF_PROFILE=hyperswarm conf_router_flags_ok '["--parked-request-budget=90s","--route-timeout=5m","--parked-request-max=65536","--ingress-allowed-spiffe-ids='"${DEMO_CONF_ROUTER_SPIFFE_ID}"'"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}" >/dev/null; then
  echo "FAIL: hyperswarm conf_router_flags_ok rejected --parked-request-max=65536" >&2
  fail=1
fi
if DEMO_CONF_PROFILE=invalid conf_validate_profile 2>/dev/null || DEMO_CONF_PROFILE=invalid cc_validate_profile 2>/dev/null; then
  echo "FAIL: validate_profile accepted an invalid profile" >&2
  fail=1
fi
mega_gw="$(DEMO_CONF_PROFILE=megaswarm conf_render manifests/gateway.yaml.tmpl)"
for pair in 'ACTOR_COUNT:65536' 'ACTORS_PER_WORKER:16' 'SUSPEND_AFTER:3s' 'MOCK_THINK_MIN_MS:9000' 'MOCK_EXEC_MIN_MS:8000'; do
  k="${pair%%:*}"
  v="${pair#*:}"
  if ! grep -A1 "name: ${k}" <<<"${mega_gw}" | grep -qF "value: \"${v}\""; then
    echo "FAIL: megaswarm gateway manifest lacks ${k}=${v}" >&2
    fail=1
  fi
done
mega_override_gw="$(DEMO_CONF_PROFILE=megaswarm DEMO_CONF_ACTORS=4096 conf_render manifests/gateway.yaml.tmpl)"
if ! grep -A1 'name: ACTOR_COUNT' <<<"${mega_override_gw}" | grep -qF 'value: "4096"'; then
  echo "FAIL: DEMO_CONF_ACTORS did not override DEMO_CONF_PROFILE=megaswarm" >&2
  fail=1
fi
mega_pool="$(DEMO_CONF_PROFILE=megaswarm conf_render manifests/workerpool.yaml.tmpl)"
for want in "replicas: 256" "cpu: 300m" "memory: 1024Mi" "cpu: 1650m"; do
  if ! grep -qF -- "${want}" <<<"${mega_pool}"; then
    echo "FAIL: megaswarm workerpool manifest lacks ${want}" >&2
    fail=1
  fi
done
mega_tmpl="$(DEMO_CONF_PROFILE=megaswarm conf_render manifests/actortemplate.yaml.tmpl)"
for want in "cpuMillicores: 100" "memoryBytes: 134217728"; do
  if ! grep -qF -- "${want}" <<<"${mega_tmpl}"; then
    echo "FAIL: megaswarm actortemplate manifest lacks ${want}" >&2
    fail=1
  fi
done
if DEMO_CONF_PROFILE=megaswarm conf_router_flags_ok '["--parked-request-budget=90s","--route-timeout=5m","--ingress-allowed-spiffe-ids='"${DEMO_CONF_ROUTER_SPIFFE_ID}"'"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}" >/dev/null; then
  echo "FAIL: megaswarm conf_router_flags_ok did not require --parked-request-max=4096" >&2
  fail=1
fi
if ! DEMO_CONF_PROFILE=megaswarm conf_router_flags_ok '["--parked-request-budget=90s","--route-timeout=5m","--parked-request-max=4096","--ingress-allowed-spiffe-ids='"${DEMO_CONF_ROUTER_SPIFFE_ID}"'"]' 120 "${DEMO_CONF_ROUTER_SPIFFE_ID}" >/dev/null; then
  echo "FAIL: megaswarm conf_router_flags_ok rejected --parked-request-max=4096" >&2
  fail=1
fi
exclusion_args="$(PROJECT_ID=p CLUSTER_NAME=c cc_exclusion_args)"
for want in --add-maintenance-exclusion-scope=no_minor_or_node_upgrades --add-maintenance-exclusion-until-end-of-support; do
  if ! grep -qxF -- "${want}" <<<"${exclusion_args}"; then
    echo "FAIL: cc_exclusion_args lacks ${want}" >&2
    fail=1
  fi
done

exit "${fail}"

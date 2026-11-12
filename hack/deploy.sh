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
# Deploys the demo onto a cluster that runs Substrate. Sourced by
# hack/test-deploy.sh for its cluster-free tests.

# shellcheck source=hack/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

DEMO_CONF_PATCH_ROUTER="${DEMO_CONF_PATCH_ROUTER:-false}"
# DEMO_CONF_PROFILE selects a deployment preset ("swarm" for the 50-actor
# booth demo on 2-5 workers with 4 seats/pod, or "megaswarm" for the 65,536-actor
# 16^4 scale demo on 256 workers with 16 seats/pod). Individual DEMO_CONF_*
# variables always override the selected profile's defaults.
DEMO_CONF_PROFILE="${DEMO_CONF_PROFILE:-swarm}"
# DEMO_CONF_TEMPLATE_REVISION marks the conf-agent template. Actor templates
# are immutable, so bump it with every change to manifests/actortemplate.yaml.tmpl
# and the install refuses a stale template instead of running old actors.
DEMO_CONF_TEMPLATE_REVISION=11
# DEMO_CONF_DEFAULT_STORAGE_CLASS is auto-selected for actor /workspace
# volumes when both this StorageClass and the Filestore CSIDriverConfig exist
# on the cluster (set DEMO_CONF_STORAGE_CLASS=none to keep /tmp/workspace).
DEMO_CONF_DEFAULT_STORAGE_CLASS="substrate-volumepool-sc"
DEMO_CONF_VOLUME_BYTES="${DEMO_CONF_VOLUME_BYTES:-1073741824}"
DEMO_CONF_NAMESPACE="${DEMO_CONF_NAMESPACE:-ate-demo-conf}"
# DEMO_CONF_ROUTER_SPIFFE_ID is the default pod identity conf-gateway presents
# to atenet-router's mTLS ingress (:443). conf_router_spiffe_id derives it from
# DEMO_CONF_NAMESPACE on each call so inline overrides work in tests.
DEMO_CONF_ROUTER_SPIFFE_ID="spiffe://cluster.local/ns/${DEMO_CONF_NAMESPACE}/sa/conf-gateway"

conf_profile() {
  echo "${DEMO_CONF_PROFILE:-swarm}"
}

conf_validate_profile() {
  local p
  p="$(conf_profile)"
  case "${p}" in
    swarm | megaswarm | hyperswarm) return 0 ;;
    *)
      echo "error: unknown DEMO_CONF_PROFILE '${p}' (expected 'swarm', 'megaswarm', or 'hyperswarm')" >&2
      return 1
      ;;
  esac
}

conf_actors() {
  if [[ -n "${DEMO_CONF_ACTORS:-}" ]]; then
    echo "${DEMO_CONF_ACTORS}"
  elif [[ "$(conf_profile)" == "hyperswarm" ]]; then
    echo "16777216"
  elif [[ "$(conf_profile)" == "megaswarm" ]]; then
    echo "65536"
  else
    echo "50"
  fi
}

conf_min_workers() {
  if [[ -n "${DEMO_CONF_MIN_WORKERS:-}" ]]; then
    echo "${DEMO_CONF_MIN_WORKERS}"
  elif [[ "$(conf_profile)" == "hyperswarm" ]]; then
    echo "4096"
  elif [[ "$(conf_profile)" == "megaswarm" ]]; then
    echo "256"
  else
    echo "2"
  fi
}

conf_max_workers() {
  if [[ -n "${DEMO_CONF_MAX_WORKERS:-}" ]]; then
    echo "${DEMO_CONF_MAX_WORKERS}"
  elif [[ "$(conf_profile)" == "hyperswarm" ]]; then
    echo "4096"
  elif [[ "$(conf_profile)" == "megaswarm" ]]; then
    echo "256"
  else
    echo "5"
  fi
}

conf_actors_per_worker() {
  if [[ -n "${DEMO_CONF_ACTORS_PER_WORKER:-}" ]]; then
    echo "${DEMO_CONF_ACTORS_PER_WORKER}"
  elif [[ "$(conf_profile)" == "hyperswarm" ]]; then
    echo "64"
  elif [[ "$(conf_profile)" == "megaswarm" ]]; then
    echo "16"
  else
    echo "4"
  fi
}

conf_human_slot_stride() {
  if [[ -n "${HUMAN_SLOT_STRIDE:-}" ]]; then
    echo "${HUMAN_SLOT_STRIDE}"
    return
  fi
  local actors
  actors="$(conf_actors)"
  if ((10#${actors:-50} > 65536)) || { [[ "$(conf_profile)" == "hyperswarm" ]] && ((10#${actors:-50} >= 4096)); }; then
    echo "4096"
  elif ((10#${actors:-50} > 256)); then
    echo "16"
  else
    echo "0"
  fi
}

conf_ns() {
  echo "${DEMO_CONF_NAMESPACE:-ate-demo-conf}"
}

conf_suffix() {
  local ns
  ns="$(conf_ns)"
  if [[ "${ns}" == "ate-demo-conf" ]]; then
    echo ""
  elif [[ "${ns}" == ate-demo-conf-* ]]; then
    echo "${ns#ate-demo-conf-}"
  else
    echo "${ns}"
  fi
}

conf_worker_pool() {
  if [[ -n "${DEMO_CONF_WORKER_POOL:-}" ]]; then
    echo "${DEMO_CONF_WORKER_POOL}"
    return
  fi
  local s
  s="$(conf_suffix)"
  [[ -z "${s}" ]] && echo "conf-workers" || echo "conf-workers-${s}"
}

conf_workload_label() {
  if [[ -n "${DEMO_CONF_WORKLOAD_LABEL:-}" ]]; then
    echo "${DEMO_CONF_WORKLOAD_LABEL}"
    return
  fi
  local s
  s="$(conf_suffix)"
  [[ -z "${s}" ]] && echo "conference" || echo "conference-${s}"
}

conf_template_name() {
  if [[ -n "${DEMO_CONF_TEMPLATE_NAME:-}" ]]; then
    echo "${DEMO_CONF_TEMPLATE_NAME}"
    return
  fi
  local s
  s="$(conf_suffix)"
  [[ -z "${s}" ]] && echo "conf-agent" || echo "conf-agent-${s}"
}

conf_rbac_prefix() {
  local ns
  ns="$(conf_ns)"
  [[ "${ns}" == "ate-demo-conf" ]] && echo "" || echo "${ns}-"
}

conf_external_metric() {
  if [[ -n "${DEMO_CONF_EXTERNAL_METRIC:-}" ]]; then
    echo "${DEMO_CONF_EXTERNAL_METRIC}"
    return
  fi
  local s
  s="$(conf_suffix)"
  [[ -z "${s}" ]] && echo "conf_worker_demand" || echo "conf_worker_demand_${s//-/_}"
}

conf_dashboard_suffix() {
  local ns
  ns="$(conf_ns)"
  [[ "${ns}" == "ate-demo-conf" ]] && echo "" || echo " / ${ns}"
}

conf_router_spiffe_id() {
  echo "spiffe://cluster.local/ns/$(conf_ns)/sa/conf-gateway"
}

# conf_seconds converts a Go duration made of h/m/s parts to
# whole seconds, or prints nothing if it cannot parse it.
conf_seconds() {
  local d="$1" total=0 n unit
  while [[ "${d}" =~ ^([0-9]+)(h|m|s)(.*)$ ]]; do
    # 10# forces base 10: a leading zero (e.g. "08s") would otherwise read
    # as an invalid octal literal and abort the arithmetic.
    n=$((10#${BASH_REMATCH[1]}))
    unit="${BASH_REMATCH[2]}"
    d="${BASH_REMATCH[3]}"
    case "${unit}" in
      h) total=$((total + n * 3600)) ;;
      m) total=$((total + n * 60)) ;;
      s) total=$((total + n)) ;;
    esac
  done
  [[ -z "${d}" ]] && echo "${total}"
}

# conf_flag_value prints the value of a --flag from a JSON array
# of args, accepting both "--flag=v" and "--flag" "v".
conf_flag_value() {
  local args_json="$1" flag="$2"
  jq -r --arg f "${flag}" '
    . as $a | [range(0; length)] | map(
      if ($a[.] | startswith($f + "=")) then ($a[.] | ltrimstr($f + "="))
      elif $a[.] == $f then $a[. + 1]
      else empty end) | last // empty' <<<"${args_json}"
}

# conf_grace_ok returns 0 if GRACE_SECONDS (the Deployment's
# spec.template.spec.terminationGracePeriodSeconds, or empty/unparseable if
# it could not be read) is at least 120: the minimum that fits
# atenet-router's shutdown sequence once --parked-request-budget is raised
# to 60s. At that budget the derived --drain-timeout alone (budget +
# drainRouteBudget + margin, see substrate's cmd/atenet/internal/router/xds.go
# and config.go) already reaches about 75s, on top of --drain-delay and the
# Envoy drain window (substrate's manifests/ate-install/atenet-router.yaml);
# a shorter grace period lets the kubelet SIGKILL the router mid-drain.
conf_grace_ok() {
  local grace="${1:-}"
  [[ "${grace}" =~ ^[0-9]+$ ]] && ((10#${grace} >= 120))
}

# conf_router_allows_spiffe returns 0 if ARGS_JSON includes SPIFFE_ID in
# any --ingress-allowed-spiffe-ids flag (which pflag parses as a
# comma-separated StringSlice that may be repeated).
conf_router_allows_spiffe() {
  local args_json="$1" spiffe_id="$2"
  jq -e --arg f "--ingress-allowed-spiffe-ids" --arg id "${spiffe_id}" '
    . as $a | [range(0; length)] | map(
      if ($a[.] | startswith($f + "=")) then ($a[.] | ltrimstr($f + "="))
      elif $a[.] == $f then ($a[. + 1] // "")
      else empty end
    ) | map(split(",")[]) | index($id) != null' <<<"${args_json}" >/dev/null
}

# conf_ateapi_preview_enabled returns 0 if ARGS_JSON (ate-api-server's
# container args) enables GATE or ALL via --preview.
conf_ateapi_preview_enabled() {
  local args_json="$1" gate="$2"
  jq -e --arg f "--preview" --arg g "${gate}" '
    . as $a | [range(0; length)] | map(
      if ($a[.] | startswith($f + "=")) then ($a[.] | ltrimstr($f + "="))
      elif $a[.] == $f then ($a[. + 1] // "")
      else empty end
    ) | map(split(",")[]) | (index($g) != null or index("ALL") != null)' <<<"${args_json}" >/dev/null
}

# conf_resolve_storage_class chooses DEMO_CONF_STORAGE_CLASS when unset and
# verifies the cluster's StorageClass and ExternalVolumes preview gate when
# active. Setting DEMO_CONF_STORAGE_CLASS=none skips external volumes.
conf_resolve_storage_class() {
  if [[ -z "${DEMO_CONF_STORAGE_CLASS:-}" ]]; then
    if [[ "$(conf_profile)" == "megaswarm" || "$(conf_profile)" == "hyperswarm" ]]; then
      DEMO_CONF_STORAGE_CLASS="none"
      log_step "Profile $(conf_profile) defaults to DEMO_CONF_STORAGE_CLASS=none (GCS snapshots + /tmp/workspace)"
    elif run_kubectl get storageclass "${DEMO_CONF_DEFAULT_STORAGE_CLASS}" >/dev/null 2>&1 \
        && run_kubectl get csidriverconfigs.ate.dev filestore.csi.storage.gke.io >/dev/null 2>&1; then
      DEMO_CONF_STORAGE_CLASS="${DEMO_CONF_DEFAULT_STORAGE_CLASS}"
      log_step "Auto-detected external volume StorageClass ${DEMO_CONF_STORAGE_CLASS}"
    fi
  fi
  if ! conf_storage_class_enabled; then
    return 0
  fi
  if ! run_kubectl get storageclass "${DEMO_CONF_STORAGE_CLASS}" >/dev/null 2>&1; then
    echo "error: StorageClass ${DEMO_CONF_STORAGE_CLASS} not found on the cluster." >&2
    echo "Create it first, or set DEMO_CONF_STORAGE_CLASS=none to use /tmp/workspace." >&2
    exit 1
  fi
  local args_json
  args_json="$(run_kubectl get deployment ate-api-server -n ate-system \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="ate-api-server")].args}')"
  if ! conf_ateapi_preview_enabled "${args_json:-[]}" "ExternalVolumes"; then
    echo "error: DEMO_CONF_STORAGE_CLASS=${DEMO_CONF_STORAGE_CLASS} requires --preview=ExternalVolumes on deployment/ate-api-server in ate-system." >&2
    echo "Enable it on ate-api-server, or set DEMO_CONF_STORAGE_CLASS=none to use /tmp/workspace." >&2
    exit 1
  fi
  log_step "Actor workspaces will mount external volumes from StorageClass ${DEMO_CONF_STORAGE_CLASS} at /workspace"
}

# conf_router_flags_ok prints the changes atenet-router needs for
# long LLM turns and conf-gateway mTLS ingress, and returns 1 if any are
# missing or too low. It also requires GRACE_SECONDS (see conf_grace_ok) to
# be at least 120, but only once the duration flags are themselves
# acceptable: a low grace period is only unsafe once --parked-request-budget
# is raised to 60s, so there is nothing to say about it before that.
conf_router_flags_ok() {
  local args_json="$1" grace="${2:-}" spiffe="${3-$(conf_router_spiffe_id)}" missing=() v s actors
  actors="$(conf_actors)"
  v="$(conf_flag_value "${args_json}" --parked-request-budget)"
  s="$(conf_seconds "${v:-5s}")"
  if [[ -z "${s}" || "${s}" -lt 60 ]]; then
    missing+=("--parked-request-budget=60s")
  fi
  v="$(conf_flag_value "${args_json}" --route-timeout)"
  if [[ -n "${v}" ]]; then
    s="$(conf_seconds "${v}")"
    if [[ -z "${s}" || "${s}" -lt 60 ]]; then
      missing+=("--route-timeout=5m")
    fi
  fi
  if ((${#missing[@]} == 0)) && ! conf_grace_ok "${grace}"; then
    missing+=("terminationGracePeriodSeconds=120")
  fi
  if [[ -n "${spiffe}" ]] && ! conf_router_allows_spiffe "${args_json}" "${spiffe}"; then
    missing+=("--ingress-allowed-spiffe-ids=${spiffe}")
  fi
  if [[ "$(conf_profile)" == "hyperswarm" ]] || ((10#${actors:-50} > 65536)); then
    v="$(conf_flag_value "${args_json}" --parked-request-max)"
    if [[ ! "${v}" =~ ^[0-9]+$ ]] || ((10#${v} < 65536)); then
      missing+=("--parked-request-max=65536")
    fi
  elif ((10#${actors:-50} > 1024)); then
    v="$(conf_flag_value "${args_json}" --parked-request-max)"
    if [[ ! "${v}" =~ ^[0-9]+$ ]] || ((10#${v} < 4096)); then
      missing+=("--parked-request-max=4096")
    fi
  fi
  if ((${#missing[@]} > 0)); then
    printf '%s\n' "${missing[@]}"
    return 1
  fi
}

# conf_router_patch prints a JSON patch that adds FLAGS... to the
# atenet-router container of the Deployment JSON in $1. The container is
# found by name, since the Deployment also runs Envoy; a container without
# args gets a new args array. Fails if there is no atenet-router container.
conf_router_patch() {
  local deploy_json="$1"
  shift
  # Flags go in one newline-joined --arg: jq would parse a positional
  # "--flag" as its own option.
  jq -c --arg flags "$(printf '%s\n' "$@")" '
    ($flags | split("\n") | map(select(. != ""))) as $flags
    | (.spec.template.spec.containers | map(.name) | index("atenet-router")) as $i
    | if $i == null then error("atenet-router container not found") else . end
    | "/spec/template/spec/containers/\($i)/args" as $path
    | if .spec.template.spec.containers[$i].args == null
      then [{op: "add", path: $path, value: $flags}]
      else [$flags[] | {op: "add", path: ($path + "/-"), value: .}]
      end' <<<"${deploy_json}"
}

conf_check_router() {
  local args_json grace missing flag flags patch
  args_json="$(run_kubectl get deployment atenet-router -n ate-system \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="atenet-router")].args}')"
  grace="$(run_kubectl get deployment atenet-router -n ate-system \
    -o jsonpath='{.spec.template.spec.terminationGracePeriodSeconds}')"
  if missing="$(conf_router_flags_ok "${args_json}" "${grace}")"; then
    return 0
  fi
  if [[ "${DEMO_CONF_PATCH_ROUTER}" != true ]]; then
    echo "error: atenet-router needs these changes for LLM turns that outlast its defaults:" >&2
    while IFS= read -r line; do echo "  ${line}"; done <<<"${missing}" >&2
    echo "Apply them to the atenet-router Deployment, or rerun with --patch-router." >&2
    exit 1
  fi
  log_step "Patching atenet-router flags (not restored on delete)..."
  flags=()
  while IFS= read -r flag; do
    [[ "${flag}" == --* ]] && flags+=("${flag}")
  done <<<"${missing}"
  if ((${#flags[@]} > 0)); then
    if ! patch="$(conf_router_patch \
      "$(run_kubectl get deployment atenet-router -n ate-system -o json)" "${flags[@]}")"; then
      echo "error: cannot find the atenet-router container in the atenet-router Deployment" >&2
      exit 1
    fi
    run_kubectl_fatal patch deployment atenet-router -n ate-system --type=json -p="${patch}"
  fi
  # Checked unconditionally, not just when "missing" already listed it:
  # raising --parked-request-budget to 60s above always makes the grace
  # period requirement apply, even if it wasn't part of "missing" because
  # the flags were not yet acceptable before this patch.
  if ! conf_grace_ok "${grace}"; then
    run_kubectl_fatal patch deployment atenet-router -n ate-system --type=json \
      -p='[{"op":"add","path":"/spec/template/spec/terminationGracePeriodSeconds","value":120}]'
  fi
  run_kubectl_fatal rollout status deployment/atenet-router -n ate-system --timeout=120s
}

# conf_workers_range_ok returns 0 if MIN and MAX are whole numbers with
# 1 <= MIN <= MAX.
conf_workers_range_ok() {
  local min="$1" max="$2"
  [[ "${min}" =~ ^[0-9]+$ && "${max}" =~ ^[0-9]+$ ]] || return 1
  ((10#${min} >= 1 && 10#${min} <= 10#${max}))
}

# conf_worker_replicas prints the replicas to apply to conf-workers: CURRENT
# (the pool's spec.replicas, empty for a new pool) clamped to [MIN, MAX], so
# a re-deploy keeps the size the HPA chose.
conf_worker_replicas() {
  local current="$1" min="$2" max="$3"
  if [[ ! "${current}" =~ ^[0-9]+$ ]] || ((10#${current} < 10#${min})); then
    echo "$((10#${min}))"
  elif ((10#${current} > 10#${max})); then
    echo "$((10#${max}))"
  else
    echo "$((10#${current}))"
  fi
}

# conf_apiservice_owner prints the namespace owning v1beta1.external.metrics.k8s.io
# when served by a conf-prometheus-adapter instance, or nothing otherwise.
conf_apiservice_owner() {
  local json="$1"
  [[ -z "${json}" ]] && return 0
  jq -r '(.spec.service // {}) | if .name == "conf-prometheus-adapter" and (.namespace // "") != "" then .namespace else empty end' \
    <<<"${json}"
}

# conf_apiservice_ok returns 0 if APISERVICE_JSON (the
# v1beta1.external.metrics.k8s.io APIService, empty when it does not exist)
# is absent or already served by a conf-prometheus-adapter instance on the cluster.
conf_apiservice_ok() {
  local json="$1"
  [[ -z "${json}" ]] && return 0
  [[ -n "$(conf_apiservice_owner "${json}")" ]]
}

# conf_apiservice_deletable returns 0 if teardown may delete the APIService:
# GET_RC (the exit status of reading it) is 0 and APISERVICE_JSON is absent
# or owned by DEMO_CONF_NAMESPACE. A failed read or an adapter in another
# demo namespace leaves the APIService intact.
conf_apiservice_deletable() {
  local rc="$1" json="$2" owner
  [[ "${rc}" == 0 ]] || return 1
  [[ -z "${json}" ]] && return 0
  owner="$(conf_apiservice_owner "${json}")"
  [[ "${owner}" == "$(conf_ns)" ]]
}

# conf_check_apiservice refuses to take over the cluster's external metrics
# API from another adapter.
conf_check_apiservice() {
  local json
  if ! json="$(run_kubectl get apiservice v1beta1.external.metrics.k8s.io -o json --ignore-not-found)"; then
    echo "error: cannot read APIService v1beta1.external.metrics.k8s.io" >&2
    exit 1
  fi
  if ! conf_apiservice_ok "${json}"; then
    echo "error: v1beta1.external.metrics.k8s.io is served by $(jq -r '(.spec.service // {}) | "\(.namespace // "kube-apiserver")/\(.name // "local")"' <<<"${json}")." >&2
    echo "  The conference demo installs its own prometheus-adapter there; remove the other adapter first." >&2
    exit 1
  fi
}

# conf_iam_retry runs an IAM-binding gcloud command, retrying it
# a few times with a short sleep when CREATED is true: IAM's view of a
# service account lags its creation by a few seconds, so the first binding
# attempt on a brand-new GSA can 404. A pre-existing GSA gets one attempt,
# so a real permission or config error still fails immediately.
conf_iam_retry() {
  local created="$1" out attempt=1 max=5
  shift
  while true; do
    if out="$("$@" 2>&1)"; then
      return 0
    fi
    if { [[ "${created}" != true ]] && [[ "${out}" != *"gcloud crashed (FileNotFoundError)"* ]]; } || ((attempt >= max)); then
      echo "error: $* failed:" >&2
      echo "${out}" >&2
      exit 1
    fi
    log_step "conf-gateway service account not yet visible to IAM; retrying (${attempt}/${max})..."
    sleep 2
    ((attempt++))
  done
}

conf_ensure_gsa() {
  local gsa="conf-gateway@${PROJECT_ID}.iam.gserviceaccount.com" created=false
  if [[ "${DEMO_CONF_SKIP_IAM:-false}" == "true" ]]; then
    DEMO_CONF_GATEWAY_GSA="${gsa}"
    return 0
  fi
  if ! gcloud iam service-accounts describe "${gsa}" --project "${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud iam service-accounts create conf-gateway --project "${PROJECT_ID}" \
      --display-name "Conference demo gateway (Agent Platform only)"
    created=true
  fi
  conf_iam_retry "${created}" gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --condition=None --member "serviceAccount:${gsa}" --role roles/aiplatform.user
  conf_iam_retry "${created}" gcloud iam service-accounts add-iam-policy-binding "${gsa}" \
    --project "${PROJECT_ID}" \
    --member "serviceAccount:${PROJECT_ID}.svc.id.goog[$(conf_ns)/conf-gateway]" \
    --role roles/iam.workloadIdentityUser
  DEMO_CONF_GATEWAY_GSA="${gsa}"
}

# conf_build_gateway pushes the gateway image and echoes its
# digest-pinned reference.
conf_build_gateway() {
  local repo="${KO_DOCKER_REPO}/conf-gateway"
  # shellcheck disable=SC2155 # safe initialization
  local stage_tag="${repo}:build-$(date +%s)"
  docker buildx build --platform=linux/amd64 --push \
    -f build/gateway.Dockerfile -t "${stage_tag}" . >&2
  local digest
  digest="$(docker buildx imagetools inspect "${stage_tag}" --format '{{json .}}' | jq -r '.manifest.digest')"
  if [[ -z "${digest}" || "${digest}" == "null" ]]; then
    echo "error: failed to resolve digest for ${stage_tag}" >&2
    return 1
  fi
  echo "${repo}@${digest}"
}

conf_render() {
  local actors min_workers max_workers seats
  actors="$(conf_actors)"
  min_workers="$(conf_min_workers)"
  max_workers="$(conf_max_workers)"
  seats="$(conf_actors_per_worker)"
  local default_actor_cpu=1000
  local default_actor_mem=536870912
  local default_think_min=1200
  local default_think_max=2200
  local default_exec_min=800
  local default_exec_max=1600
  local default_suspend_after="2s"
  local default_datagen_replicas=8
  local default_gw_mem_limit="4Gi"
  local default_provision_shards=32
  local default_provision_conc=64
  if [[ "$(conf_profile)" == "hyperswarm" ]] || ((10#${actors:-50} > 65536)); then
    default_actor_cpu=500
    default_actor_mem=536870912
    default_think_min=9000
    default_think_max=13000
    default_exec_min=8000
    default_exec_max=12000
    default_suspend_after="3s"
    default_datagen_replicas=32
    default_gw_mem_limit="16Gi"
    default_provision_shards=64
  elif ((10#${actors:-50} > 256)); then
    default_actor_cpu=100
    default_actor_mem=134217728
    default_think_min=9000
    default_think_max=13000
    default_exec_min=8000
    default_exec_max=12000
    default_suspend_after="3s"
  fi
  local actor_cpu="${DEMO_CONF_ACTOR_CPU_MILLICORES:-${default_actor_cpu}}"
  local actor_mem="${DEMO_CONF_ACTOR_MEMORY_BYTES:-${default_actor_mem}}"
  local cpu_headroom=$((10#${actor_cpu} / 2))
  local worker_cpu_req="${DEMO_CONF_WORKER_CPU_REQUEST:-500m}"
  local worker_mem_req="${DEMO_CONF_WORKER_MEMORY_REQUEST:-$((10#${seats} * 512 + 256))Mi}"
  if { [[ "$(conf_profile)" == "hyperswarm" ]] || ((10#${actors:-50} > 65536)); } && ((10#${actor_cpu} < 1000)); then
    worker_cpu_req="${DEMO_CONF_WORKER_CPU_REQUEST:-750m}"
    worker_mem_req="${DEMO_CONF_WORKER_MEMORY_REQUEST:-4096Mi}"
  elif ((10#${actor_cpu} < 1000)); then
    worker_cpu_req="${DEMO_CONF_WORKER_CPU_REQUEST:-300m}"
    worker_mem_req="${DEMO_CONF_WORKER_MEMORY_REQUEST:-1024Mi}"
  fi
  local sandbox_workspace="/tmp/workspace"
  if conf_storage_class_enabled; then
    sandbox_workspace="/workspace"
  fi
  local out
  out="$(sed -e "s|\${NAMESPACE}|$(conf_ns)|g" \
      -e "s|\${WORKER_POOL}|$(conf_worker_pool)|g" \
      -e "s|\${WORKLOAD_LABEL}|$(conf_workload_label)|g" \
      -e "s|\${TEMPLATE_NAME}|$(conf_template_name)|g" \
      -e "s|\${RBAC_PREFIX}|$(conf_rbac_prefix)|g" \
      -e "s|\${EXTERNAL_METRIC}|$(conf_external_metric)|g" \
      -e "s|\${DASHBOARD_SUFFIX}|$(conf_dashboard_suffix)|g" \
      -e "s|\${GATEWAY_IMAGE}|${DEMO_CONF_GATEWAY_IMAGE:-placeholder}|g" \
      -e "s|\${GATEWAY_GSA}|${DEMO_CONF_GATEWAY_GSA:-}|g" \
      -e "s|\${VERTEX_PROJECT}|${DEMO_CONF_VERTEX_PROJECT:-}|g" \
      -e "s|\${VERTEX_LOCATION}|${DEMO_CONF_VERTEX_LOCATION:-global}|g" \
      -e "s|\${DEMO_MOCK_LLM}|${DEMO_CONF_MOCK_LLM:-true}|g" \
      -e "s|\${SANDBOX_WORKSPACE}|${sandbox_workspace}|g" \
      -e "s|\${MOCK_THINK_MIN_MS}|${DEMO_CONF_MOCK_THINK_MIN_MS:-${default_think_min}}|g" \
      -e "s|\${MOCK_THINK_MAX_MS}|${DEMO_CONF_MOCK_THINK_MAX_MS:-${default_think_max}}|g" \
      -e "s|\${MOCK_EXEC_MIN_MS}|${DEMO_CONF_MOCK_EXEC_MIN_MS:-${default_exec_min}}|g" \
      -e "s|\${MOCK_EXEC_MAX_MS}|${DEMO_CONF_MOCK_EXEC_MAX_MS:-${default_exec_max}}|g" \
      -e "s|\${ACTOR_COUNT}|${actors}|g" \
      -e "s|\${DEMO_DOMAIN}|${DEMO_DOMAIN:-}|g" \
      -e "s|\${TEMPLATE_REVISION}|${DEMO_CONF_TEMPLATE_REVISION}|g" \
      -e "s|\${CLUSTER}|${DEMO_CONF_CLUSTER:-}|g" \
      -e "s|\${WORKER_REPLICAS}|${DEMO_CONF_WORKER_REPLICAS:-${min_workers}}|g" \
      -e "s|\${MIN_WORKERS}|${min_workers}|g" \
      -e "s|\${MAX_WORKERS}|${max_workers}|g" \
      -e "s|\${ACTORS_PER_WORKER}|${seats}|g" \
      -e "s|\${HUMAN_SLOT_STRIDE}|$(conf_human_slot_stride)|g" \
      -e "s|\${SIMULATION_MAX_MPS}|${SIMULATION_MAX_MPS:-0}|g" \
      -e "s|\${GATEWAY_MEMORY_LIMIT}|${DEMO_CONF_GATEWAY_MEMORY_LIMIT:-${default_gw_mem_limit}}|g" \
      -e "s|\${ACTOR_CPU_MILLICORES}|${actor_cpu}|g" \
      -e "s|\${ACTOR_MEMORY_BYTES}|${actor_mem}|g" \
      -e "s|\${ALLOWED_GOOGLE_DOMAINS}|${ALLOWED_GOOGLE_DOMAINS:-}|g" \
      -e "s|\${GOOGLE_CLIENT_ID}|${GOOGLE_CLIENT_ID:-}|g" \
      -e "s|\${FIREBASE_API_KEY}|${FIREBASE_API_KEY:-}|g" \
      -e "s|\${FIREBASE_PROJECT_ID}|${FIREBASE_PROJECT_ID:-${PROJECT_ID:-}}|g" \
      -e "s|\${CHAT_RATE_LIMIT_RPM}|${CHAT_RATE_LIMIT_RPM:-10}|g" \
      -e "s|\${SESSION_RATE_LIMIT_RPM}|${SESSION_RATE_LIMIT_RPM:-10}|g" \
      -e "s|\${SUSPEND_AFTER}|${DEMO_CONF_SUSPEND_AFTER:-${default_suspend_after}}|g" \
      -e "s|\${DATA_GENERATOR_URL}|${DEMO_CONF_DATA_GENERATOR_URL:-}|g" \
      -e "s|\${SIMULATION_TURN_URL}|${DEMO_CONF_SIMULATION_TURN_URL:-http://conf-gateway/api/simulation/turn}|g" \
      -e "s|\${DATAGEN_ORCHESTRATOR_IMAGE}|${DEMO_CONF_DATAGEN_ORCHESTRATOR_IMAGE:-placeholder}|g" \
      -e "s|\${DATAGEN_WORKER_IMAGE}|${DEMO_CONF_DATAGEN_WORKER_IMAGE:-placeholder}|g" \
      -e "s|\${DATAGEN_WORKER_REPLICAS}|${DEMO_CONF_DATAGEN_WORKER_REPLICAS:-${default_datagen_replicas}}|g" \
      -e "s|\${PROVISION_SHARDS}|${DEMO_CONF_PROVISION_SHARDS:-${default_provision_shards}}|g" \
      -e "s|\${PROVISION_CONCURRENCY}|${DEMO_CONF_PROVISION_CONCURRENCY:-${default_provision_conc}}|g" \
      -e "s|\${PROVISION_DELETE_ALL}|${DEMO_CONF_PROVISION_DELETE_ALL:-false}|g" \
      -e "s|\${WORKER_CPU_REQUEST}|${worker_cpu_req}|g" \
      -e "s|\${WORKER_CPU_LIMIT}|$((10#${seats} * 10#${actor_cpu} + cpu_headroom))m|g" \
      -e "s|\${WORKER_MEMORY_REQUEST}|${worker_mem_req}|g" \
      "$1")"
  if [[ "$1" == *actortemplate.yaml.tmpl ]] && conf_storage_class_enabled; then
    awk -v sc="${DEMO_CONF_STORAGE_CLASS}" -v cap="${DEMO_CONF_VOLUME_BYTES}" '
      /^resources:/ {
        print "  volumeMounts:"
        print "  - name: workspace"
        print "    mountPath: /workspace"
      }
      { print }
      END {
        print "volumes:"
        print "- name: workspace"
        print "  external:"
        print "    template:"
        print "      storageClassName: " sc
        print "      capacityBytes: " cap
      }
    ' <<<"${out}"
    return 0
  fi
  if [[ "$1" == *gateway-ingress.yaml.tmpl && -n "${DEMO_CONF_STATIC_IP_NAME:-}" ]]; then
    awk -v ip="${DEMO_CONF_STATIC_IP_NAME}" '
      /kubernetes\.io\/ingress\.class:/ {
        print
        print "    kubernetes.io/ingress.global-static-ip-name: \"" ip "\""
        next
      }
      { print }
    ' <<<"${out}"
    return 0
  fi
  printf '%s\n' "${out}"
}

conf_render_hpa() {
  conf_render manifests/autoscaling.yaml.tmpl | sed -n '/^apiVersion: autoscaling\/v2$/,$p'
}

conf_render_shared_prometheus() {
  local owner_ns="$1"
  DEMO_CONF_NAMESPACE="${owner_ns}" conf_render manifests/autoscaling.yaml.tmpl \
    | awk '/^# --- conf-prometheus-adapter ---$/ { exit } { print }'
}

conf_adapter_series_query() {
  printf '{__name__="ate_workerpool_workers",ate_workerpool_namespace="%s",ate_workerpool_name="%s"}' \
    "$(conf_ns)" "$(conf_worker_pool)"
}

conf_adapter_metrics_query() {
  local ns pool seats
  ns="$(conf_ns)"
  pool="$(conf_worker_pool)"
  seats="$(conf_actors_per_worker)"
  printf '(max(ate_workerpool_workers{ate_workerpool_namespace="%s",ate_workerpool_name="%s",ate_worker_state="partial"}) or vector(0)) + (max(ate_workerpool_workers{ate_workerpool_namespace="%s",ate_workerpool_name="%s",ate_worker_state="at_capacity"}) or vector(0)) + ceil((max(conf_gateway_parked_requests{namespace="%s"}) or sum(atenet_router_parking_active) or vector(0)) / %s)' \
    "${ns}" "${pool}" "${ns}" "${pool}" "${ns}" "${seats}"
}

conf_update_adapter_configmap() {
  local adapter_ns="$1" mode="$2" existing updated tmp_cfg
  existing="$(run_kubectl get configmap conf-prometheus-adapter-config -n "${adapter_ns}" \
    -o jsonpath='{.data.config\.yaml}' --ignore-not-found)"
  if [[ "${mode}" == "upsert" ]]; then
    updated="$(go run ./hack/adapterconfig upsert \
      "$(conf_external_metric)" "$(conf_adapter_series_query)" "$(conf_adapter_metrics_query)" <<<"${existing}")"
  else
    updated="$(go run ./hack/adapterconfig delete "$(conf_external_metric)" <<<"${existing}")"
  fi
  tmp_cfg="$(mktemp)"
  printf '%s' "${updated}" >"${tmp_cfg}"
  run_kubectl create configmap conf-prometheus-adapter-config -n "${adapter_ns}" \
    --from-file="config.yaml=${tmp_cfg}" --dry-run=client -o yaml | run_kubectl_fatal apply -f -
  rm -f "${tmp_cfg}"
}

# conf_discover_google_auth populates GOOGLE_CLIENT_ID and FIREBASE_API_KEY from
# the GCP project's Identity Platform configuration when ALLOWED_GOOGLE_DOMAINS
# is set, and adds DEMO_DOMAIN to Identity Platform's authorizedDomains.
conf_discover_google_auth() {
  [[ -n "${ALLOWED_GOOGLE_DOMAINS:-}" && -n "${PROJECT_ID:-}" ]] || return 0
  command -v curl >/dev/null 2>&1 || return 0
  local tok idp_json key_name cfg_json domains_json
  tok="$(gcloud auth application-default print-access-token 2>/dev/null || gcloud auth print-access-token 2>/dev/null || true)"
  [[ -n "${tok}" ]] || return 0

  if [[ -z "${GOOGLE_CLIENT_ID:-}" ]]; then
    idp_json="$(curl -fsS -H "Authorization: Bearer ${tok}" \
      -H "x-goog-user-project: ${PROJECT_ID}" \
      "https://identitytoolkit.googleapis.com/admin/v2/projects/${PROJECT_ID}/defaultSupportedIdpConfigs/google.com" 2>/dev/null || true)"
    GOOGLE_CLIENT_ID="$(jq -r '.clientId // empty' <<<"${idp_json}" 2>/dev/null || true)"
  fi

  if [[ -z "${FIREBASE_API_KEY:-}" ]]; then
    key_name="$(gcloud services api-keys list --project "${PROJECT_ID}" \
      --filter='displayName:"Browser key"' --format='value(name)' 2>/dev/null | head -n1 || true)"
    if [[ -n "${key_name}" ]]; then
      FIREBASE_API_KEY="$(gcloud services api-keys get-key-string "${key_name}" \
        --project "${PROJECT_ID}" --format='value(keyString)' 2>/dev/null || true)"
    fi
  fi

  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    cfg_json="$(curl -fsS -H "Authorization: Bearer ${tok}" \
      -H "x-goog-user-project: ${PROJECT_ID}" \
      "https://identitytoolkit.googleapis.com/admin/v2/projects/${PROJECT_ID}/config" 2>/dev/null || true)"
    if [[ -n "${cfg_json}" ]] && ! jq -e --arg d "${DEMO_DOMAIN}" '(.authorizedDomains // []) | index($d) != null' <<<"${cfg_json}" >/dev/null 2>&1; then
      domains_json="$(jq -c --arg d "${DEMO_DOMAIN}" '{authorizedDomains: (((.authorizedDomains // []) + [$d]) | unique)}' <<<"${cfg_json}")"
      curl -fsS -X PATCH -H "Authorization: Bearer ${tok}" \
        -H "x-goog-user-project: ${PROJECT_ID}" \
        -H "Content-Type: application/json" \
        "https://identitytoolkit.googleapis.com/admin/v2/projects/${PROJECT_ID}/config?updateMask=authorizedDomains" \
        -d "${domains_json}" >/dev/null 2>&1 || true
    fi
  fi
}

conf_ensure_secret() {
  local ns
  ns="$(conf_ns)"
  if run_kubectl get secret conf-gateway -n "${ns}" >/dev/null 2>&1; then
    return 0
  fi
  # Generated into locals first: a command substitution embedded directly in
  # a --from-literal=... argument doesn't trip errexit on failure, so a
  # failed openssl would otherwise create a permanently empty Secret.
  local hmac token
  hmac="$(openssl rand -hex 32)"
  token="$(openssl rand -hex 24)"
  if [[ -z "${hmac}" || -z "${token}" ]]; then
    echo "error: failed to generate conf-gateway secret values" >&2
    exit 1
  fi
  # --from-env-file reads the values from a file (here, a process
  # substitution backed by the printf builtin), so neither value ever
  # appears in this or any other process's argv.
  run_kubectl_fatal create secret generic conf-gateway -n "${ns}" \
    --from-env-file=<(printf 'HMAC_SECRET=%s\nPRESENTER_TOKEN=%s\n' "${hmac}" "${token}")
}

# conf_template_current succeeds if the conf-agent template JSON in
# $1 carries the current DEMO_CONF_TEMPLATE_REVISION and matches the
# configured external volume StorageClass.
conf_template_current() {
  local want_sc=""
  if conf_storage_class_enabled; then
    want_sc="${DEMO_CONF_STORAGE_CLASS}"
  fi
  jq -e --arg rev "${DEMO_CONF_TEMPLATE_REVISION}" --arg sc "${want_sc}" '
    (([.containers[]? | select(.name == "conf-workload")] | first // {})
      | [.env[]? | select(.name == "CONF_TEMPLATE_REVISION" and .value == $rev)] | length > 0)
    and ((([.volumes[]? | select(.name == "workspace")] | first // {}).external.template.storageClassName // "") == $sc)' <<<"$1" >/dev/null
}

conf_check_template() {
  local ns tmpl env err_file err rc=0
  ns="$(conf_ns)"
  tmpl="$(conf_template_name)"
  err_file="$(mktemp)"
  # Keep stderr separate from stdout: kubectl-ate v1.0.0-rc.2+ prints a
  # "--token-file" warning on stderr even when "-o json" succeeds.
  env="$(run_kubectl_ate get actor-template "${tmpl}" -a "${ns}" -o json 2>"${err_file}")" || rc=$?
  err="$(<"${err_file}")"
  rm -f "${err_file}"
  if ((rc != 0)); then
    if grep -qi "not found" <<<"${err} ${env}"; then
      return 0
    fi
    echo "error: failed to check actor template ${ns}/${tmpl}: ${err:-${env}}" >&2
    exit 1
  fi
  if ! conf_template_current "${env}"; then
    echo "error: actor template ${ns}/${tmpl} is out of date (want CONF_TEMPLATE_REVISION=${DEMO_CONF_TEMPLATE_REVISION}, storageClassName=${DEMO_CONF_STORAGE_CLASS:-}) and templates are immutable." >&2
    echo "Run: DEMO_CONF_NAMESPACE=${ns} hack/deploy.sh down, then up." >&2
    exit 1
  fi
}

# conf_check_substrate requires Substrate on the cluster at the version
# pinned in go.mod, installing it from SUBSTRATE_REPO if it is absent.
# DEMO_CONF_ALLOW_DIRTY_SUBSTRATE=true also accepts a cluster built from a
# "<pin>-dirty" checkout (see substrate_labels_match).
conf_check_substrate() {
  local pin labels ver_out allow_dirty="${DEMO_CONF_ALLOW_DIRTY_SUBSTRATE:-false}"
  pin="$(substrate_pin)" || { echo "error: cannot read the substrate pin from go.mod" >&2; exit 1; }
  if ! run_kubectl get namespace ate-system >/dev/null 2>&1; then
    if [[ -z "${SUBSTRATE_REPO:-}" ]]; then
      echo "error: Substrate is not installed (no ate-system namespace)." >&2
      echo "Install it from a clean substrate checkout at ${pin}:" >&2
      echo "  SNAPSHOT_LOCATION=\"gs://\${BUCKET_NAME}/snapshots/\" SNAPSHOT_TRANSFER_GSA_NAME=ate-snapshot-transfer go run ./cmd/ate-setup deploy ate-system --sandbox-classes=gvisor --credential-provider='{\"enabled\":false}' --ingress-allowed-spiffe-ids=${DEMO_CONF_ROUTER_SPIFFE_ID}" >&2
      echo "or set SUBSTRATE_REPO to that checkout and run this again." >&2
      exit 1
    fi
    local head want
    head="$(git -C "${SUBSTRATE_REPO}" rev-parse HEAD)"
    if ! want="$(git -C "${SUBSTRATE_REPO}" rev-parse --verify -q "${pin}^{commit}")"; then
      echo "error: SUBSTRATE_REPO has no ${pin}. Run git fetch --tags there first." >&2
      exit 1
    fi
    if [[ "${head}" != "${want}" ]]; then
      echo "error: SUBSTRATE_REPO is at ${head:0:12}, not the pinned ${pin}. Check out ${pin} there first." >&2
      exit 1
    fi
    if ! substrate_repo_clean "${SUBSTRATE_REPO}"; then
      echo "error: SUBSTRATE_REPO has uncommitted changes." >&2
      echo "ate-setup would build ate-api-server as ${pin}-dirty, which the check below refuses by default." >&2
      echo "Commit or stash them, or set DEMO_CONF_ALLOW_DIRTY_SUBSTRATE=true to accept a dirty install." >&2
      exit 1
    fi
    if [[ -z "${SNAPSHOT_LOCATION:-}" && -z "${BUCKET_NAME:-}" && "${ATE_INSTALL_KIND:-false}" != "true" ]]; then
      echo "error: SNAPSHOT_LOCATION or BUCKET_NAME must be set to install Substrate from SUBSTRATE_REPO" >&2
      exit 1
    fi
    log_step "Installing Substrate from ${SUBSTRATE_REPO}..."
    local -a extra_ate_flags=()
    local pool_conns_env=""
    local pg_sc_env=""
    if [[ -n "${ATE_IMAGE_REPO:-}" ]]; then
      extra_ate_flags+=(--image-repo="${ATE_IMAGE_REPO}" --image-tag="${ATE_IMAGE_TAG:-${pin}}")
    fi
    if [[ "$(conf_profile)" == "hyperswarm" ]] \
        && [[ -n "$(run_kubectl get nodes -l ate.dev/workloadType=ate-postgres -o name 2>/dev/null)" ]] \
        && [[ -n "$(run_kubectl get nodes -l ate.dev/workloadType=ate-system-infra -o name 2>/dev/null)" ]]; then
      extra_ate_flags+=(--cluster-size=size10 --cordon-system-infra-components)
      pool_conns_env="24"
      if run_kubectl get storageclass dynamic-rwo >/dev/null 2>&1; then
        pg_sc_env="dynamic-rwo"
      fi
      log_step "Detected ate-postgres & ate-system-infra pools: enabling --cluster-size=size10 --cordon-system-infra-components (poolMaxConns=24, storageClass=${pg_sc_env:-default})"
    fi
    (
      cd "${SUBSTRATE_REPO}"
      if [[ "${ATE_INSTALL_KIND:-false}" == "true" ]]; then
        go run ./cmd/ate-setup deploy ate-system \
          --sandbox-classes=gvisor \
          --credential-provider="${ATE_CREDENTIAL_PROVIDER:-{\"enabled\":false}}" \
          --ingress-allowed-spiffe-ids="${ATE_INGRESS_ALLOWED_SPIFFE_IDS:-${DEMO_CONF_ROUTER_SPIFFE_ID}}"
      else
        SNAPSHOT_LOCATION="${SNAPSHOT_LOCATION:-gs://${BUCKET_NAME}/snapshots/}" \
        SNAPSHOT_TRANSFER_GSA_NAME="${SNAPSHOT_TRANSFER_GSA_NAME:-ate-snapshot-transfer}" \
        ATE_API_POSTGRES_POOL_MAX_CONNS="${ATE_API_POSTGRES_POOL_MAX_CONNS:-${pool_conns_env}}" \
        ATE_API_POSTGRES_STORAGE_CLASS="${ATE_API_POSTGRES_STORAGE_CLASS:-${pg_sc_env}}" \
        go run ./cmd/ate-setup deploy ate-system \
          --sandbox-classes=gvisor \
          --credential-provider="${ATE_CREDENTIAL_PROVIDER:-{\"enabled\":false}}" \
          --ingress-allowed-spiffe-ids="${ATE_INGRESS_ALLOWED_SPIFFE_IDS:-${DEMO_CONF_ROUTER_SPIFFE_ID}}" \
          "${extra_ate_flags[@]}"
      fi
    )
    if [[ "${#extra_ate_flags[@]}" -gt 0 ]]; then
      log_step "Scaling ate-api-server and atenet-router to 16 replicas on ate-system-infra..."
      run_kubectl_fatal scale deployment/ate-api-server -n ate-system --replicas="${ATE_API_SERVER_REPLICAS:-16}"
      run_kubectl_fatal scale deployment/atenet-router -n ate-system --replicas="${ATE_ROUTER_REPLICAS:-16}"
      run_kubectl_fatal rollout status deployment/ate-api-server -n ate-system --timeout=300s
      run_kubectl_fatal rollout status deployment/atenet-router -n ate-system --timeout=300s
    fi
  fi
  ver_out="$(run_kubectl exec -n ate-system deployment/ate-api-server -c ate-api-server -- /ko-app/ateapi --version 2>/dev/null || true)"
  labels="$(substrate_version_from_output "${ver_out}")"
  if ! substrate_labels_match "${pin}" "${labels}" "${allow_dirty}"; then
    case "$(substrate_label_mismatch_kind "${pin}" "${labels}")" in
      unlabeled)
        echo "error: cannot determine the cluster's Substrate version (ate-api-server is not responding in ate-system); is Substrate installed and running?" >&2
        ;;
      dirty)
        echo "error: the cluster runs the pinned ${pin}, but Substrate was installed from a modified checkout ($(sort -u <<<"${labels}" | tr '\n' ' ')):" >&2
        echo "Reinstall Substrate from a clean checkout at ${pin}, or set DEMO_CONF_ALLOW_DIRTY_SUBSTRATE=true to accept it." >&2
        ;;
      *)
        echo "error: the cluster runs Substrate $(sort -u <<<"${labels}" | tr '\n' ' ')but go.mod pins ${pin}." >&2
        echo "Bump the pin to the cluster's tag or commit:" >&2
        echo "  go get -tool ${SUBSTRATE_MODULE}/cmd/kubectl-ate@<tag-or-commit>" >&2
        echo "  go get -tool ${SUBSTRATE_MODULE}/cmd/ateworker-gvisor@<tag-or-commit>" >&2
        echo "  then copy substrate's go directive and k8s.io/apimachinery replace into go.mod and run go mod tidy." >&2
        ;;
    esac
    exit 1
  fi
}

# conf_gke_cluster_name prints the cluster name from a GKE kubeconfig
# cluster entry (gke_<project>_<location>_<name>), or nothing for any other
# kind of entry.
conf_gke_cluster_name() {
  if [[ "$1" =~ ^gke_[^_]+_[^_]+_(.+)$ ]]; then
    echo "${BASH_REMATCH[1]}"
  fi
}

# conf_apply_dashboard creates or updates the "Conference demo (<cluster>)"
# Cloud Monitoring dashboard from manifests/dashboard.json, matched by
# display name so a re-run updates it in place. Its queries filter on the
# cluster label, taken from DEMO_CONF_CLUSTER or the GKE kube context. A
# failure only warns: the demo runs without its dashboard.
conf_apply_dashboard() {
  if [[ -z "${DEMO_CONF_CLUSTER:-}" ]]; then
    DEMO_CONF_CLUSTER="$(conf_gke_cluster_name \
      "$(run_kubectl config view --minify -o jsonpath='{.contexts[0].context.cluster}')")"
  fi
  if [[ -z "${DEMO_CONF_CLUSTER}" ]]; then
    log_step "Skipping the dashboard: not a GKE context; set DEMO_CONF_CLUSTER to the cluster name."
    return 0
  fi
  local display name etag config
  display="Conference demo (${DEMO_CONF_CLUSTER}$(conf_dashboard_suffix))"
  log_step "Applying the \"${display}\" dashboard..."
  config="$(mktemp)"
  if ! name="$(gcloud monitoring dashboards list --project "${PROJECT_ID}" \
      --filter="displayName=\"${display}\"" --format='value(name)' | head -n1)"; then
    echo "warning: cannot list dashboards in ${PROJECT_ID}; the dashboard was not applied." >&2
  elif [[ -n "${name}" ]]; then
    # An update must carry the current etag, or Cloud Monitoring rejects it.
    if ! { etag="$(gcloud monitoring dashboards describe "${name}" --format='value(etag)')" &&
        conf_render manifests/dashboard.json | jq --arg etag "${etag}" '.etag = $etag' >"${config}" &&
        gcloud monitoring dashboards update "${name}" --config-from-file="${config}" >/dev/null; }; then
      echo "warning: failed to update dashboard ${name}." >&2
    fi
  else
    conf_render manifests/dashboard.json >"${config}"
    if ! name="$(gcloud monitoring dashboards create --project "${PROJECT_ID}" \
        --config-from-file="${config}" --format='value(name)')"; then
      echo "warning: failed to create the dashboard." >&2
      name=""
    fi
  fi
  rm -f "${config}"
  if [[ -n "${name}" ]]; then
    echo "Dashboard: https://console.cloud.google.com/monitoring/dashboards/builder/${name##*/}?project=${PROJECT_ID}"
  fi
}

conf_deploy() {
  conf_validate_profile || exit 1
  DEMO_CONF_ACTORS="$(conf_actors)"
  DEMO_CONF_MIN_WORKERS="$(conf_min_workers)"
  DEMO_CONF_MAX_WORKERS="$(conf_max_workers)"
  DEMO_CONF_ACTORS_PER_WORKER="$(conf_actors_per_worker)"
  log_step "Deploying the conference demo (profile: $(conf_profile), actors: ${DEMO_CONF_ACTORS}, workers: ${DEMO_CONF_MIN_WORKERS}-${DEMO_CONF_MAX_WORKERS}, seats/worker: ${DEMO_CONF_ACTORS_PER_WORKER})"
  log_target_context
  : "${KO_DOCKER_REPO:?KO_DOCKER_REPO must be set}"
  local tool
  for tool in kubectl ko docker jq go; do
    command -v "${tool}" >/dev/null || { echo "error: ${tool} is required" >&2; exit 1; }
  done
  if ! docker buildx version >/dev/null 2>&1; then
    echo "error: docker buildx is required" >&2
    exit 1
  fi
  conf_check_substrate
  if ! conf_workers_range_ok "${DEMO_CONF_MIN_WORKERS}" "${DEMO_CONF_MAX_WORKERS}"; then
    echo "error: need 1 <= DEMO_CONF_MIN_WORKERS (${DEMO_CONF_MIN_WORKERS}) <= DEMO_CONF_MAX_WORKERS (${DEMO_CONF_MAX_WORKERS})" >&2
    exit 1
  fi
  if ! conf_workers_range_ok "${DEMO_CONF_ACTORS_PER_WORKER}" "${DEMO_CONF_ACTORS_PER_WORKER}"; then
    echo "error: DEMO_CONF_ACTORS_PER_WORKER (${DEMO_CONF_ACTORS_PER_WORKER}) must be a whole number >= 1" >&2
    exit 1
  fi

  if [[ -n "${PROJECT_ID:-}" ]]; then
    DEMO_CONF_VERTEX_PROJECT="${PROJECT_ID}"
    DEMO_CONF_VERTEX_LOCATION="${VERTEX_LOCATION:-global}"
    DEMO_CONF_MOCK_LLM=false
  else
    log_step "PROJECT_ID is unset: deploying with DEMO_MOCK_LLM=true (no model calls)."
    DEMO_CONF_VERTEX_PROJECT=""
    DEMO_CONF_MOCK_LLM=true
  fi

  # Read-only and cheap: run before the router check so a stale conf-agent
  # aborts the deploy before atenet-router is ever patched.
  conf_resolve_storage_class
  conf_check_template
  conf_check_apiservice
  conf_check_router
  if [[ -n "${PROJECT_ID:-}" ]]; then
    conf_ensure_gsa
    conf_discover_google_auth
  fi

  local ns pool tmpl current_workers apiservice_json apiservice_owner existing_adapter_cfg
  ns="$(conf_ns)"
  pool="$(conf_worker_pool)"
  tmpl="$(conf_template_name)"
  if ! current_workers="$(run_kubectl get workerpools.ate.dev "${pool}" -n "${ns}" \
      -o jsonpath='{.spec.replicas}' --ignore-not-found)"; then
    echo "error: cannot read WorkerPool ${ns}/${pool}" >&2
    exit 1
  fi
  DEMO_CONF_WORKER_REPLICAS="$(conf_worker_replicas "${current_workers}" \
    "${DEMO_CONF_MIN_WORKERS}" "${DEMO_CONF_MAX_WORKERS}")"
  conf_render manifests/workerpool.yaml.tmpl | ko resolve -f - | run_kubectl_fatal apply -f -
  log_step "Waiting for the ${ns}/${pool} worker pool rollout..."
  wait_for_pool_rollout_fatal "${pool}" "${ns}" "${DEMO_CONF_WORKER_ROLLOUT_TIMEOUT:-900s}"

  apiservice_json="$(run_kubectl get apiservice v1beta1.external.metrics.k8s.io -o json --ignore-not-found)"
  apiservice_owner="$(conf_apiservice_owner "${apiservice_json}")"
  if [[ -z "${apiservice_owner}" || "${apiservice_owner}" == "${ns}" ]]; then
    log_step "Installing Prometheus, prometheus-adapter and the ${pool} HPA in ${ns}..."
    existing_adapter_cfg="$(run_kubectl get configmap conf-prometheus-adapter-config -n "${ns}" \
      -o jsonpath='{.data.config\.yaml}' --ignore-not-found)"
    conf_render manifests/autoscaling.yaml.tmpl | run_kubectl_fatal apply -f -
    if [[ -n "${existing_adapter_cfg}" ]]; then
      conf_update_adapter_configmap "${ns}" upsert
      run_kubectl_fatal rollout restart deployment/conf-prometheus-adapter -n "${ns}"
    fi
    run_kubectl_fatal rollout status deployment/conf-prometheus -n "${ns}" --timeout=180s
    run_kubectl_fatal rollout status deployment/conf-prometheus-adapter -n "${ns}" --timeout=180s
    # Register the aggregated API only once the adapter serves it; an
    # unavailable APIService breaks discovery for the whole cluster.
    conf_render manifests/autoscaling-apiservice.yaml.tmpl | run_kubectl_fatal apply -f -
  else
    log_step "Registering $(conf_external_metric) in shared ${apiservice_owner}/conf-prometheus-adapter and installing the ${pool} HPA..."
    local old_prom_cfg new_prom_cfg
    old_prom_cfg="$(run_kubectl get configmap conf-prometheus-config -n "${apiservice_owner}" \
      -o jsonpath='{.data.prometheus\.yml}' --ignore-not-found)"
    conf_render_shared_prometheus "${apiservice_owner}" | run_kubectl_fatal apply -f -
    new_prom_cfg="$(run_kubectl get configmap conf-prometheus-config -n "${apiservice_owner}" \
      -o jsonpath='{.data.prometheus\.yml}' --ignore-not-found)"
    if [[ "${old_prom_cfg}" != "${new_prom_cfg}" ]]; then
      run_kubectl_fatal rollout restart deployment/conf-prometheus -n "${apiservice_owner}"
    fi
    run_kubectl_fatal rollout status deployment/conf-prometheus -n "${apiservice_owner}" --timeout=180s
    conf_update_adapter_configmap "${apiservice_owner}" upsert
    run_kubectl_fatal rollout restart deployment/conf-prometheus-adapter -n "${apiservice_owner}"
    run_kubectl_fatal rollout status deployment/conf-prometheus-adapter -n "${apiservice_owner}" --timeout=180s
    conf_render_hpa | run_kubectl_fatal apply -f -
  fi
  ensure_atespace "${ns}"
  create_actor_template conf_render manifests/actortemplate.yaml.tmpl "${ns}" "${tmpl}" 300

  if [[ -n "${DATA_GENERATOR_REPO:-}" ]]; then
    log_step "Building data-generator orchestrator and worker images from ${DATA_GENERATOR_REPO}..."
    DEMO_CONF_DATAGEN_ORCHESTRATOR_IMAGE="$(cd "${DATA_GENERATOR_REPO}" && ko build ./cmd/orchestrator)"
    DEMO_CONF_DATAGEN_WORKER_IMAGE="$(cd "${DATA_GENERATOR_REPO}" && ko build ./cmd/generator)"
  fi
  if [[ -n "${DEMO_CONF_DATAGEN_ORCHESTRATOR_IMAGE:-}" && -n "${DEMO_CONF_DATAGEN_WORKER_IMAGE:-}" ]]; then
    DEMO_CONF_DATA_GENERATOR_URL="${DEMO_CONF_DATA_GENERATOR_URL:-http://data-generator-orchestrator:8080}"
    log_step "Deploying data-generator orchestrator and workers in ${ns}..."
    conf_render manifests/datagen.yaml.tmpl | run_kubectl_fatal apply -f -
    run_kubectl_fatal rollout status deployment/data-generator-orchestrator -n "${ns}" --timeout=180s
    run_kubectl_fatal rollout status deployment/data-generator-worker -n "${ns}" --timeout=180s
  fi

  log_step "Building conf-gateway image..."
  DEMO_CONF_GATEWAY_IMAGE="$(conf_build_gateway)"
  conf_ensure_secret
  conf_render manifests/gateway.yaml.tmpl | run_kubectl_fatal apply -f -
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    conf_render manifests/gateway-ingress.yaml.tmpl | run_kubectl_fatal apply -f -
  fi
  run_kubectl_fatal rollout status deployment/conf-gateway -n "${ns}" --timeout=180s

  log_step "Creating ${DEMO_CONF_ACTORS} attendee actors in ${ns}..."
  if ((10#${DEMO_CONF_ACTORS} > 65536)) && [[ "${DEMO_CONF_IN_CLUSTER_PROVISION:-true}" == "true" ]]; then
    run_kubectl delete job conf-provision -n "${ns}" --ignore-not-found
    conf_render manifests/provision-job.yaml.tmpl | ko resolve -f - | run_kubectl_fatal apply -f -
    log_step "Waiting for sharded in-cluster conf-provision Indexed Job to finish creating ${DEMO_CONF_ACTORS} actors..."
    run_kubectl_fatal wait --for=condition=complete job/conf-provision -n "${ns}" --timeout="${DEMO_CONF_PROVISION_TIMEOUT:-7200s}"
  elif ((10#${DEMO_CONF_ACTORS} > 100)); then
    go run ./cmd/conf-gateway/cmd/conf-provision \
      --atespace="${ns}" \
      --template="${tmpl}" \
      --count="${DEMO_CONF_ACTORS}" \
      --context="${KUBECTL_CONTEXT:-}"
  else
    local i actor err_out
    for i in $(seq 1 "${DEMO_CONF_ACTORS}"); do
      actor="$(printf "attendee-%02d" "${i}")"
      if ! err_out="$(run_kubectl_ate create actor "${actor}" -a "${ns}" --template "${tmpl}" 2>&1)"; then
        if [[ "${err_out}" != *"AlreadyExists"* && "${err_out}" != *"already exists"* ]] \
            && ! run_kubectl_ate get actor "${actor}" -a "${ns}" >/dev/null 2>&1; then
          echo "error: failed to create actor ${actor}: ${err_out}" >&2
          exit 1
        fi
      fi
    done
  fi

  if [[ -n "${PROJECT_ID:-}" ]]; then
    conf_apply_dashboard
  fi

  log_step "Conference demo deployed in ${ns}."
  echo "Presenter: kubectl port-forward -n ${ns} svc/conf-gateway 8090:80"
  echo "  then open http://localhost:8090/stage?key=\$(kubectl get secret conf-gateway -n ${ns} -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
  echo "Attendees: http://<conf-gateway EXTERNAL-IP>/chat (plain HTTP unless DEMO_DOMAIN is set)"
}

conf_delete() {
  local ns tmpl apiservice apiservice_rc=0 apiservice_owner
  ns="$(conf_ns)"
  tmpl="$(conf_template_name)"
  log_step "Deleting the conference demo in ${ns}"
  log_target_context
  run_kubectl delete job conf-provision -n "${ns}" --ignore-not-found
  run_kubectl delete -f <(conf_render manifests/datagen.yaml.tmpl) --ignore-not-found
  run_kubectl delete -f <(conf_render manifests/gateway.yaml.tmpl) --ignore-not-found
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    run_kubectl delete -f <(conf_render manifests/gateway-ingress.yaml.tmpl) --ignore-not-found
  fi
  run_kubectl delete secret conf-gateway -n "${ns}" --ignore-not-found
  delete_actor_template "${ns}" "${tmpl}"
  run_kubectl_ate delete atespace "${ns}" 2>/dev/null \
    || log_step "atespace ${ns} not deleted (may not exist or is not empty)"
  apiservice="$(run_kubectl get apiservice v1beta1.external.metrics.k8s.io -o json --ignore-not-found)" || apiservice_rc=$?
  apiservice_owner="$(conf_apiservice_owner "${apiservice}")"
  if conf_apiservice_deletable "${apiservice_rc}" "${apiservice}"; then
    run_kubectl delete -f <(conf_render manifests/autoscaling-apiservice.yaml.tmpl) --ignore-not-found
    run_kubectl delete -f <(conf_render manifests/autoscaling.yaml.tmpl) --ignore-not-found
  elif [[ "${apiservice_rc}" != 0 ]]; then
    log_step "cannot read v1beta1.external.metrics.k8s.io; leaving it."
    run_kubectl delete -f <(conf_render manifests/autoscaling.yaml.tmpl) --ignore-not-found
  elif [[ -n "${apiservice_owner}" && "${apiservice_owner}" != "${ns}" ]]; then
    log_step "v1beta1.external.metrics.k8s.io belongs to ${apiservice_owner}; removing $(conf_external_metric) rule and keeping shared adapter."
    conf_update_adapter_configmap "${apiservice_owner}" delete
    run_kubectl rollout restart deployment/conf-prometheus-adapter -n "${apiservice_owner}" || true
    run_kubectl delete -f <(conf_render_hpa) --ignore-not-found
  else
    log_step "v1beta1.external.metrics.k8s.io belongs to another adapter; leaving it."
    run_kubectl delete -f <(conf_render manifests/autoscaling.yaml.tmpl) --ignore-not-found
  fi
  conf_render manifests/workerpool.yaml.tmpl | run_kubectl delete --ignore-not-found -f -
  log_step "atenet-router flags are left as they are."
  if [[ -n "${PROJECT_ID:-}" ]]; then
    local gsa="conf-gateway@${PROJECT_ID}.iam.gserviceaccount.com"
    log_step "conf-gateway service account and its IAM bindings are left in place."
    echo "  Remove them with:"
    echo "    gcloud iam service-accounts delete ${gsa} --project ${PROJECT_ID}"
    echo "    gcloud projects remove-iam-policy-binding ${PROJECT_ID} --member serviceAccount:${gsa} --role roles/aiplatform.user"
    log_step "The \"Conference demo (<cluster>)\" dashboard is left in place; delete it in Cloud Monitoring."
  fi
}

conf_main() {
  local action=""
  while (($# > 0)); do
    case "$1" in
      up | down) action="$1" ;;
      --patch-router) DEMO_CONF_PATCH_ROUTER=true ;;
      --profile=*) DEMO_CONF_PROFILE="${1#--profile=}" ;;
      --profile)
        if (($# < 2)); then
          echo "error: --profile requires an argument (swarm, megaswarm, or hyperswarm)" >&2
          action=help
          break
        fi
        DEMO_CONF_PROFILE="$2"
        shift
        ;;
      -h | --help) action=help ;;
      *) echo "error: unknown argument $1" >&2; action=help ;;
    esac
    shift
  done
  if [[ "${action}" == "up" || "${action}" == "down" ]]; then
    if ! conf_validate_profile; then
      return 1
    fi
  fi
  case "${action}" in
    up) conf_deploy ;;
    down) conf_delete ;;
    *)
      echo "Usage: $0 up|down [--profile=swarm|megaswarm|hyperswarm] [--patch-router]"
      echo "  up                       deploy the demo onto a cluster running Substrate at the go.mod pin"
      echo "  down                     delete it (router flags and the gateway service account stay)"
      echo "  --profile=swarm|megaswarm|hyperswarm select deployment preset (default: DEMO_CONF_PROFILE=swarm):"
      echo "                             swarm      : 50 actors, 2-5 workers, 4 seats/worker, 1 CPU / 512 MiB per actor, Filestore auto-detect"
      echo "                             megaswarm  : 65,536 actors (16^4), 256 workers, 16 seats/worker, 100m CPU / 128 MiB per actor, GCS + /tmp/workspace"
      echo "                             hyperswarm : 16,777,216 actors (64^4), 4,096 workers across 64 nodes, 64 seats/worker, 20m CPU / 128 MiB per actor"
      echo "  --patch-router           patch atenet-router's timeouts, max parked requests and grace period instead of failing"
      echo "  Required env: KO_DOCKER_REPO"
      echo "  Optional env: DEMO_CONF_PROFILE (default swarm; 'swarm', 'megaswarm', or 'hyperswarm'),"
      echo "                PROJECT_ID (enables Gemini on Agent Platform; unset runs mock replies), VERTEX_LOCATION,"
      echo "                DEMO_DOMAIN, DEMO_CONF_STATIC_IP_NAME, ALLOWED_GOOGLE_DOMAINS, GOOGLE_CLIENT_ID, FIREBASE_API_KEY, FIREBASE_PROJECT_ID,"
      echo "                DEMO_CONF_NAMESPACE (default ate-demo-conf; isolates namespace, atespace, worker pool and template),"
      echo "                DEMO_CONF_WORKER_POOL, DEMO_CONF_WORKLOAD_LABEL, DEMO_CONF_TEMPLATE_NAME, DEMO_CONF_EXTERNAL_METRIC,"
      echo "                CHAT_RATE_LIMIT_RPM (default 10), SESSION_RATE_LIMIT_RPM (default 10),"
      echo "                DEMO_CONF_ACTORS (default 50 for swarm, 65536 for megaswarm), SUBSTRATE_REPO, BUCKET_NAME, SNAPSHOT_LOCATION,"
      echo "                DEMO_CONF_MIN_WORKERS (default 2 for swarm, 256 for megaswarm),"
      echo "                DEMO_CONF_MAX_WORKERS (default 5 for swarm, 256 for megaswarm),"
      echo "                DEMO_CONF_ACTORS_PER_WORKER (default 4 for swarm, 16 for megaswarm),"
      echo "                DEMO_CONF_STORAGE_CLASS (external volume StorageClass; auto-detects substrate-volumepool-sc on swarm, defaults to 'none' on megaswarm),"
      echo "                DATA_GENERATOR_REPO, DEMO_CONF_DATAGEN_ORCHESTRATOR_IMAGE, DEMO_CONF_DATAGEN_WORKER_IMAGE (optional in-cluster data-generator fleet),"
      echo "                KUBECTL_CONTEXT (defaults to the current kube context),"
      echo "                DEMO_CONF_CLUSTER (dashboard cluster label; defaults to the GKE context's cluster),"
      echo "                DEMO_CONF_ALLOW_DIRTY_SUBSTRATE (accept a cluster labeled <pin>-dirty),"
      echo "                KO_DEFAULTPLATFORMS=linux/amd64 (skip the arm64 build)"
      echo "  This script sources no dev-env file; export the variables above yourself."
      [[ "${action}" == help ]] && return 0 || return 1
      ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -o errexit -o nounset -o pipefail
  cd "$(dirname "${BASH_SOURCE[0]}")/.."
  conf_main "$@"
fi

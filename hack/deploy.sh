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
DEMO_CONF_ACTORS="${DEMO_CONF_ACTORS:-50}"
DEMO_CONF_MIN_WORKERS="${DEMO_CONF_MIN_WORKERS:-2}"
DEMO_CONF_MAX_WORKERS="${DEMO_CONF_MAX_WORKERS:-5}"
# DEMO_CONF_TEMPLATE_REVISION marks the conf-agent template. Actor templates
# are immutable, so bump it with every change to manifests/actortemplate.yaml.tmpl
# and the install refuses a stale template instead of running old actors.
DEMO_CONF_TEMPLATE_REVISION=3

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

# conf_router_flags_ok prints the changes atenet-router needs for
# long LLM turns and returns 1 if any are missing or too low. It also
# requires GRACE_SECONDS (see conf_grace_ok) to be at least 120,
# but only once the duration flags are themselves acceptable: a low grace
# period is only unsafe once --parked-request-budget is raised to 60s, so
# there is nothing to say about it before that.
conf_router_flags_ok() {
  local args_json="$1" grace="${2:-}" missing=() v s
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

# conf_apiservice_ok returns 0 if APISERVICE_JSON (the
# v1beta1.external.metrics.k8s.io APIService, empty when it does not exist)
# is absent or already served by this demo's adapter.
conf_apiservice_ok() {
  local json="$1"
  [[ -z "${json}" ]] && return 0
  jq -e '(.spec.service // {}) | .namespace == "ate-demo-conf" and .name == "conf-prometheus-adapter"' \
    <<<"${json}" >/dev/null
}

# conf_apiservice_deletable returns 0 if teardown may delete the APIService:
# GET_RC (the exit status of reading it) is 0 and APISERVICE_JSON is absent
# or ours. A failed read leaves the owner unknown, so it is kept.
conf_apiservice_deletable() {
  local rc="$1" json="$2"
  [[ "${rc}" == 0 ]] && conf_apiservice_ok "${json}"
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
    if [[ "${created}" != true ]] || ((attempt >= max)); then
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
  if ! gcloud iam service-accounts describe "${gsa}" --project "${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud iam service-accounts create conf-gateway --project "${PROJECT_ID}" \
      --display-name "Conference demo gateway (Vertex AI only)"
    created=true
  fi
  conf_iam_retry "${created}" gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --condition=None --member "serviceAccount:${gsa}" --role roles/aiplatform.user
  conf_iam_retry "${created}" gcloud iam service-accounts add-iam-policy-binding "${gsa}" \
    --project "${PROJECT_ID}" \
    --member "serviceAccount:${PROJECT_ID}.svc.id.goog[ate-demo-conf/conf-gateway]" \
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
  sed -e "s|\${BUCKET_NAME}|${BUCKET_NAME:-placeholder}|g" \
      -e "s|\${GATEWAY_IMAGE}|${DEMO_CONF_GATEWAY_IMAGE:-placeholder}|g" \
      -e "s|\${GATEWAY_GSA}|${DEMO_CONF_GATEWAY_GSA:-}|g" \
      -e "s|\${VERTEX_PROJECT}|${DEMO_CONF_VERTEX_PROJECT:-}|g" \
      -e "s|\${VERTEX_LOCATION}|${DEMO_CONF_VERTEX_LOCATION:-us-central1}|g" \
      -e "s|\${DEMO_MOCK_LLM}|${DEMO_CONF_MOCK_LLM:-true}|g" \
      -e "s|\${ACTOR_COUNT}|${DEMO_CONF_ACTORS}|g" \
      -e "s|\${DEMO_DOMAIN}|${DEMO_DOMAIN:-}|g" \
      -e "s|\${TEMPLATE_REVISION}|${DEMO_CONF_TEMPLATE_REVISION}|g" \
      -e "s|\${CLUSTER}|${DEMO_CONF_CLUSTER:-}|g" \
      -e "s|\${WORKER_REPLICAS}|${DEMO_CONF_WORKER_REPLICAS:-${DEMO_CONF_MIN_WORKERS}}|g" \
      -e "s|\${MIN_WORKERS}|${DEMO_CONF_MIN_WORKERS}|g" \
      -e "s|\${MAX_WORKERS}|${DEMO_CONF_MAX_WORKERS}|g" \
      "$1"
}

conf_ensure_secret() {
  if run_kubectl get secret conf-gateway -n ate-demo-conf >/dev/null 2>&1; then
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
  run_kubectl_fatal create secret generic conf-gateway -n ate-demo-conf \
    --from-env-file=<(printf 'HMAC_SECRET=%s\nPRESENTER_TOKEN=%s\n' "${hmac}" "${token}")
}

# conf_template_current succeeds if the conf-agent template JSON in
# $1 carries the current DEMO_CONF_TEMPLATE_REVISION.
conf_template_current() {
  jq -e --arg rev "${DEMO_CONF_TEMPLATE_REVISION}" '
    [.containers[]? | select(.name == "conf-workload")] | first // {}
    | [.env[]? | select(.name == "CONF_TEMPLATE_REVISION" and .value == $rev)] | length > 0' <<<"$1" >/dev/null
}

conf_check_template() {
  local env
  if ! env="$(run_kubectl_ate get actor-template conf-agent -a ate-demo-conf -o json 2>&1)"; then
    if grep -qi "not found" <<<"${env}"; then
      return 0
    fi
    echo "error: failed to check actor template conf-agent: ${env}" >&2
    exit 1
  fi
  if ! conf_template_current "${env}"; then
    echo "error: actor template conf-agent is out of date (it lacks CONF_TEMPLATE_REVISION ${DEMO_CONF_TEMPLATE_REVISION}) and templates are immutable." >&2
    echo "Run: hack/deploy.sh down, then hack/deploy.sh up." >&2
    exit 1
  fi
}

# conf_check_substrate requires Substrate on the cluster at the version
# pinned in go.mod, installing it from SUBSTRATE_REPO if it is absent.
# DEMO_CONF_ALLOW_DIRTY_SUBSTRATE=true also accepts a cluster labeled
# "<pin>-dirty" (see substrate_labels_match).
conf_check_substrate() {
  local pin labels allow_dirty="${DEMO_CONF_ALLOW_DIRTY_SUBSTRATE:-false}"
  pin="$(substrate_pin)" || { echo "error: cannot read the substrate pin from go.mod" >&2; exit 1; }
  if ! run_kubectl get namespace ate-system >/dev/null 2>&1; then
    if [[ -z "${SUBSTRATE_REPO:-}" ]]; then
      echo "error: Substrate is not installed (no ate-system namespace)." >&2
      echo "Install it from a clean substrate checkout at ${pin}:" >&2
      echo "  hack/install-ate.sh --deploy-ate-system" >&2
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
      echo "install-ate.sh would stamp nodes ate.dev/substrate-version=${pin}-dirty, which the check below refuses by default." >&2
      echo "Commit or stash them, or set DEMO_CONF_ALLOW_DIRTY_SUBSTRATE=true to accept a dirty install." >&2
      exit 1
    fi
    log_step "Installing Substrate from ${SUBSTRATE_REPO}..."
    (cd "${SUBSTRATE_REPO}" && hack/install-ate.sh --deploy-ate-system)
  fi
  labels="$(run_kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.labels.ate\.dev/substrate-version}{"\n"}{end}')"
  if ! substrate_labels_match "${pin}" "${labels}" "${allow_dirty}"; then
    case "$(substrate_label_mismatch_kind "${pin}" "${labels}")" in
      unlabeled)
        echo "error: no node carries ate.dev/substrate-version; is Substrate installed at a version that labels nodes?" >&2
        ;;
      dirty)
        echo "error: the cluster's nodes run the pinned ${pin}, but Substrate was installed from a modified checkout ($(sort -u <<<"${labels}" | tr '\n' ' ')):" >&2
        echo "Reinstall Substrate from a clean checkout at ${pin}, or set DEMO_CONF_ALLOW_DIRTY_SUBSTRATE=true to accept it." >&2
        ;;
      *)
        echo "error: the cluster's nodes run Substrate $(sort -u <<<"${labels}" | tr '\n' ' ')but go.mod pins ${pin}." >&2
        echo "Bump the pin to the cluster's tag or commit:" >&2
        echo "  go get -tool ${SUBSTRATE_MODULE}/cmd/kubectl-ate@<tag-or-commit>" >&2
        echo "  go get -tool ${SUBSTRATE_MODULE}/cmd/ateom-gvisor@<tag-or-commit>" >&2
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
  local display="Conference demo (${DEMO_CONF_CLUSTER})" name etag config
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
  log_step "Deploying the conference demo"
  log_target_context
  : "${BUCKET_NAME:?BUCKET_NAME must be set}"
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

  if [[ -n "${PROJECT_ID:-}" ]]; then
    DEMO_CONF_VERTEX_PROJECT="${PROJECT_ID}"
    DEMO_CONF_VERTEX_LOCATION="${VERTEX_LOCATION:-us-central1}"
    DEMO_CONF_MOCK_LLM=false
  else
    log_step "PROJECT_ID is unset: deploying with DEMO_MOCK_LLM=true (no model calls)."
    DEMO_CONF_VERTEX_PROJECT=""
    DEMO_CONF_MOCK_LLM=true
  fi

  # Read-only and cheap: run before the router check so a stale conf-agent
  # aborts the deploy before atenet-router is ever patched.
  conf_check_template
  conf_check_apiservice
  conf_check_router
  if [[ -n "${PROJECT_ID:-}" ]]; then
    conf_ensure_gsa
  fi

  local current_workers
  if ! current_workers="$(run_kubectl get workerpools.ate.dev conf-workers -n ate-demo-conf \
      -o jsonpath='{.spec.replicas}' --ignore-not-found)"; then
    echo "error: cannot read WorkerPool conf-workers" >&2
    exit 1
  fi
  DEMO_CONF_WORKER_REPLICAS="$(conf_worker_replicas "${current_workers}" \
    "${DEMO_CONF_MIN_WORKERS}" "${DEMO_CONF_MAX_WORKERS}")"
  conf_render manifests/workerpool.yaml.tmpl | ko resolve -f - | run_kubectl_fatal apply -f -
  log_step "Waiting for the conf-workers worker pool rollout..."
  wait_for_pool_rollout_fatal conf-workers ate-demo-conf

  log_step "Installing Prometheus, prometheus-adapter and the conf-workers HPA..."
  conf_render manifests/autoscaling.yaml.tmpl | run_kubectl_fatal apply -f -
  run_kubectl_fatal rollout status deployment/conf-prometheus -n ate-demo-conf --timeout=180s
  run_kubectl_fatal rollout status deployment/conf-prometheus-adapter -n ate-demo-conf --timeout=180s
  # Register the aggregated API only once the adapter serves it; an
  # unavailable APIService breaks discovery for the whole cluster.
  conf_render manifests/autoscaling-apiservice.yaml.tmpl | run_kubectl_fatal apply -f -
  ensure_atespace ate-demo-conf
  create_actor_template conf_render manifests/actortemplate.yaml.tmpl ate-demo-conf conf-agent 300

  log_step "Building conf-gateway image..."
  DEMO_CONF_GATEWAY_IMAGE="$(conf_build_gateway)"
  conf_ensure_secret
  conf_render manifests/gateway.yaml.tmpl | run_kubectl_fatal apply -f -
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    conf_render manifests/gateway-ingress.yaml.tmpl | run_kubectl_fatal apply -f -
  fi
  run_kubectl_fatal rollout status deployment/conf-gateway -n ate-demo-conf --timeout=180s

  log_step "Creating ${DEMO_CONF_ACTORS} attendee actors in ate-demo-conf..."
  local i actor err_out
  for i in $(seq 1 "${DEMO_CONF_ACTORS}"); do
    actor="$(printf "attendee-%02d" "${i}")"
    if ! err_out="$(run_kubectl_ate create actor "${actor}" -a ate-demo-conf --template conf-agent 2>&1)"; then
      if [[ "${err_out}" != *"AlreadyExists"* && "${err_out}" != *"already exists"* ]] \
          && ! run_kubectl_ate get actor "${actor}" -a ate-demo-conf >/dev/null 2>&1; then
        echo "error: failed to create actor ${actor}: ${err_out}" >&2
        exit 1
      fi
    fi
  done

  if [[ -n "${PROJECT_ID:-}" ]]; then
    conf_apply_dashboard
  fi

  log_step "Conference demo deployed."
  echo "Presenter: kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80"
  echo "  then open http://localhost:8090/stage?key=\$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
  echo "Attendees: http://<conf-gateway EXTERNAL-IP>/chat (plain HTTP unless DEMO_DOMAIN is set)"
}

conf_delete() {
  log_step "Deleting the conference demo"
  log_target_context
  run_kubectl delete -f <(conf_render manifests/gateway.yaml.tmpl) --ignore-not-found
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    run_kubectl delete -f <(conf_render manifests/gateway-ingress.yaml.tmpl) --ignore-not-found
  fi
  run_kubectl delete secret conf-gateway -n ate-demo-conf --ignore-not-found
  delete_actor_template ate-demo-conf conf-agent
  run_kubectl_ate delete atespace ate-demo-conf 2>/dev/null \
    || log_step "atespace ate-demo-conf not deleted (may not exist or is not empty)"
  local apiservice apiservice_rc=0
  apiservice="$(run_kubectl get apiservice v1beta1.external.metrics.k8s.io -o json --ignore-not-found)" || apiservice_rc=$?
  if conf_apiservice_deletable "${apiservice_rc}" "${apiservice}"; then
    run_kubectl delete -f <(conf_render manifests/autoscaling-apiservice.yaml.tmpl) --ignore-not-found
  elif [[ "${apiservice_rc}" != 0 ]]; then
    log_step "cannot read v1beta1.external.metrics.k8s.io; leaving it."
  else
    log_step "v1beta1.external.metrics.k8s.io belongs to another adapter; leaving it."
  fi
  run_kubectl delete -f <(conf_render manifests/autoscaling.yaml.tmpl) --ignore-not-found
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
      echo "                DEMO_DOMAIN, DEMO_CONF_ACTORS (default 50), SUBSTRATE_REPO,"
      echo "                DEMO_CONF_MIN_WORKERS (default 2), DEMO_CONF_MAX_WORKERS (default 5),"
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

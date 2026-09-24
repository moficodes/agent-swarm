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
# This is sourced as part of install-ate.sh, or executed directly.

ATE_DEMOS+=(demo-conference) # register demo-conference

DEMO_CONF_PATCH_ROUTER="${DEMO_CONF_PATCH_ROUTER:-false}"
DEMO_CONF_ACTORS=50
# DEMO_CONF_TEMPLATE_REVISION marks the conf-agent template. Actor templates
# are immutable, so bump it with every change to conference-template.yaml.tmpl
# and the install refuses a stale template instead of running old actors.
DEMO_CONF_TEMPLATE_REVISION=2

demo-conference_usage() {
  echo "  --patch-router                                Patch atenet-router's parked-request-budget/route-timeout"
  echo "                                                 flags and terminationGracePeriodSeconds instead of failing"
  echo "                                                 when they are too low for LLM turns and the shutdown they need."
  echo "                                                 Must appear before --deploy-demo-conference: the install script"
  echo "                                                 processes flags in order, so a --patch-router given after"
  echo "                                                 deploy has already started has no effect. DEMO_CONF_PATCH_ROUTER=true"
  echo "                                                 in the environment is equivalent and order-independent."
  echo "  Required env: BUCKET_NAME, KO_DOCKER_REPO"
  echo "  Optional env: PROJECT_ID (enables Vertex AI; unset runs in mock mode), VERTEX_LOCATION (default us-central1),"
  echo "                DEMO_DOMAIN (fronts the gateway with a GKE-managed TLS Ingress)"
}

demo-conference_cmdline() {
  case "${1}" in
    --deploy-demo-conference) demo-conference_deploy ;;
    --delete-demo-conference) demo-conference_delete ;;
    --patch-router) DEMO_CONF_PATCH_ROUTER=true ;;
    *)
      ate_demo_flag_unhandled
      ;;
  esac
}

# demo-conference_seconds converts a Go duration made of h/m/s parts to
# whole seconds, or prints nothing if it cannot parse it.
demo-conference_seconds() {
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

# demo-conference_flag_value prints the value of a --flag from a JSON array
# of args, accepting both "--flag=v" and "--flag" "v".
demo-conference_flag_value() {
  local args_json="$1" flag="$2"
  jq -r --arg f "${flag}" '
    . as $a | [range(0; length)] | map(
      if ($a[.] | startswith($f + "=")) then ($a[.] | ltrimstr($f + "="))
      elif $a[.] == $f then $a[. + 1]
      else empty end) | last // empty' <<<"${args_json}"
}

# demo-conference_grace_ok returns 0 if GRACE_SECONDS (the Deployment's
# spec.template.spec.terminationGracePeriodSeconds, or empty/unparseable if
# it could not be read) is at least 120: the minimum that fits
# atenet-router's shutdown sequence once --parked-request-budget is raised
# to 60s. At that budget the derived --drain-timeout alone (budget +
# drainRouteBudget + margin, see cmd/atenet/internal/router/xds.go and
# config.go) already reaches about 75s, on top of --drain-delay and the
# Envoy drain window (manifests/ate-install/atenet-router.yaml); a shorter
# grace period lets the kubelet SIGKILL the router mid-drain.
demo-conference_grace_ok() {
  local grace="${1:-}"
  [[ "${grace}" =~ ^[0-9]+$ ]] && ((10#${grace} >= 120))
}

# demo-conference_router_flags_ok prints the changes atenet-router needs for
# long LLM turns and returns 1 if any are missing or too low. It also
# requires GRACE_SECONDS (see demo-conference_grace_ok) to be at least 120,
# but only once the duration flags are themselves acceptable: a low grace
# period is only unsafe once --parked-request-budget is raised to 60s, so
# there is nothing to say about it before that.
demo-conference_router_flags_ok() {
  local args_json="$1" grace="${2:-}" missing=() v s
  v="$(demo-conference_flag_value "${args_json}" --parked-request-budget)"
  s="$(demo-conference_seconds "${v:-5s}")"
  if [[ -z "${s}" || "${s}" -lt 60 ]]; then
    missing+=("--parked-request-budget=60s")
  fi
  v="$(demo-conference_flag_value "${args_json}" --route-timeout)"
  if [[ -n "${v}" ]]; then
    s="$(demo-conference_seconds "${v}")"
    if [[ -z "${s}" || "${s}" -lt 60 ]]; then
      missing+=("--route-timeout=5m")
    fi
  fi
  if ((${#missing[@]} == 0)) && ! demo-conference_grace_ok "${grace}"; then
    missing+=("terminationGracePeriodSeconds=120")
  fi
  if ((${#missing[@]} > 0)); then
    printf '%s\n' "${missing[@]}"
    return 1
  fi
}

# demo-conference_router_patch prints a JSON patch that adds FLAGS... to the
# atenet-router container of the Deployment JSON in $1. The container is
# found by name, since the Deployment also runs Envoy; a container without
# args gets a new args array. Fails if there is no atenet-router container.
demo-conference_router_patch() {
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

demo-conference_check_router() {
  local args_json grace missing flag flags patch
  args_json="$(run_kubectl get deployment atenet-router -n ate-system \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="atenet-router")].args}')"
  grace="$(run_kubectl get deployment atenet-router -n ate-system \
    -o jsonpath='{.spec.template.spec.terminationGracePeriodSeconds}')"
  if missing="$(demo-conference_router_flags_ok "${args_json}" "${grace}")"; then
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
    if ! patch="$(demo-conference_router_patch \
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
  if ! demo-conference_grace_ok "${grace}"; then
    run_kubectl_fatal patch deployment atenet-router -n ate-system --type=json \
      -p='[{"op":"add","path":"/spec/template/spec/terminationGracePeriodSeconds","value":120}]'
  fi
  run_kubectl_fatal rollout status deployment/atenet-router -n ate-system --timeout=120s
}

# demo-conference_iam_retry runs an IAM-binding gcloud command, retrying it
# a few times with a short sleep when CREATED is true: IAM's view of a
# service account lags its creation by a few seconds, so the first binding
# attempt on a brand-new GSA can 404. A pre-existing GSA gets one attempt,
# so a real permission or config error still fails immediately.
demo-conference_iam_retry() {
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

demo-conference_ensure_gsa() {
  local gsa="conf-gateway@${PROJECT_ID}.iam.gserviceaccount.com" created=false
  if ! gcloud iam service-accounts describe "${gsa}" --project "${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud iam service-accounts create conf-gateway --project "${PROJECT_ID}" \
      --display-name "Conference demo gateway (Vertex AI only)"
    created=true
  fi
  demo-conference_iam_retry "${created}" gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --condition=None --member "serviceAccount:${gsa}" --role roles/aiplatform.user
  demo-conference_iam_retry "${created}" gcloud iam service-accounts add-iam-policy-binding "${gsa}" \
    --project "${PROJECT_ID}" \
    --member "serviceAccount:${PROJECT_ID}.svc.id.goog[ate-demo-conf/conf-gateway]" \
    --role roles/iam.workloadIdentityUser
  DEMO_CONF_GATEWAY_GSA="${gsa}"
}

# demo-conference_build_gateway pushes the gateway image and echoes its
# digest-pinned reference.
demo-conference_build_gateway() {
  local repo="${KO_DOCKER_REPO}/conf-gateway"
  # shellcheck disable=SC2155 # safe initialization
  local stage_tag="${repo}:build-$(date +%s)"
  docker buildx build --platform=linux/amd64 --push \
    -f demos/conference/Dockerfile -t "${stage_tag}" . >&2
  local digest
  digest="$(docker buildx imagetools inspect "${stage_tag}" --format '{{json .}}' | jq -r '.manifest.digest')"
  if [[ -z "${digest}" || "${digest}" == "null" ]]; then
    echo "error: failed to resolve digest for ${stage_tag}" >&2
    return 1
  fi
  echo "${repo}@${digest}"
}

demo-conference_render() {
  sed -e "s|\${BUCKET_NAME}|${BUCKET_NAME:-placeholder}|g" \
      -e "s|\${GATEWAY_IMAGE}|${DEMO_CONF_GATEWAY_IMAGE:-placeholder}|g" \
      -e "s|\${GATEWAY_GSA}|${DEMO_CONF_GATEWAY_GSA:-}|g" \
      -e "s|\${VERTEX_PROJECT}|${DEMO_CONF_VERTEX_PROJECT:-}|g" \
      -e "s|\${VERTEX_LOCATION}|${DEMO_CONF_VERTEX_LOCATION:-us-central1}|g" \
      -e "s|\${DEMO_MOCK_LLM}|${DEMO_CONF_MOCK_LLM:-true}|g" \
      -e "s|\${ACTOR_COUNT}|${DEMO_CONF_ACTORS}|g" \
      -e "s|\${DEMO_DOMAIN}|${DEMO_DOMAIN:-}|g" \
      -e "s|\${TEMPLATE_REVISION}|${DEMO_CONF_TEMPLATE_REVISION}|g" \
      "$1" | substitute_version
}

demo-conference_ensure_secret() {
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

# demo-conference_template_current succeeds if the conf-agent template JSON in
# $1 carries the current DEMO_CONF_TEMPLATE_REVISION.
demo-conference_template_current() {
  jq -e --arg rev "${DEMO_CONF_TEMPLATE_REVISION}" '
    [.containers[]? | select(.name == "conf-workload")] | first // {}
    | [.env[]? | select(.name == "CONF_TEMPLATE_REVISION" and .value == $rev)] | length > 0' <<<"$1" >/dev/null
}

demo-conference_check_template() {
  local env
  if ! env="$(run_kubectl_ate get actor-template conf-agent -a ate-demo-conf -o json 2>&1)"; then
    if grep -qi "not found" <<<"${env}"; then
      return 0
    fi
    echo "error: failed to check actor template conf-agent: ${env}" >&2
    exit 1
  fi
  if ! demo-conference_template_current "${env}"; then
    echo "error: actor template conf-agent is out of date (it lacks CONF_TEMPLATE_REVISION ${DEMO_CONF_TEMPLATE_REVISION}) and templates are immutable." >&2
    echo "Run: hack/install-ate.sh --delete-demo-conference, then deploy again." >&2
    exit 1
  fi
}

demo-conference_deploy() {
  log_step "demo-conference_deploy"
  : "${BUCKET_NAME:?BUCKET_NAME must be set}"
  : "${KO_DOCKER_REPO:?KO_DOCKER_REPO must be set}"
  command -v jq >/dev/null || { echo "error: jq is required" >&2; exit 1; }

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
  demo-conference_check_template
  demo-conference_check_router
  if [[ -n "${PROJECT_ID:-}" ]]; then
    demo-conference_ensure_gsa
  fi

  deploy_substrate_demo demo-conference_render \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-workers 300 \
    demos/conference/conference-template.yaml.tmpl conf-agent

  log_step "Building conf-gateway image..."
  DEMO_CONF_GATEWAY_IMAGE="$(demo-conference_build_gateway)"
  demo-conference_ensure_secret
  demo-conference_render demos/conference/manifests/gateway.yaml.tmpl | run_kubectl_fatal apply -f -
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    demo-conference_render demos/conference/manifests/gateway-ingress.yaml.tmpl | run_kubectl_fatal apply -f -
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

  log_step "Conference demo deployed."
  echo "Presenter: kubectl port-forward -n ate-demo-conf svc/conf-gateway 8090:80"
  echo "  then open http://localhost:8090/stage?key=\$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
  echo "Attendees: http://<conf-gateway EXTERNAL-IP>/chat (plain HTTP unless DEMO_DOMAIN is set)"
}

demo-conference_delete() {
  log_step "demo-conference_delete"
  run_kubectl delete -f <(demo-conference_render demos/conference/manifests/gateway.yaml.tmpl) --ignore-not-found
  if [[ -n "${DEMO_DOMAIN:-}" ]]; then
    run_kubectl delete -f <(demo-conference_render demos/conference/manifests/gateway-ingress.yaml.tmpl) --ignore-not-found
  fi
  run_kubectl delete secret conf-gateway -n ate-demo-conf --ignore-not-found
  delete_substrate_demo demo-conference_render \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-agent
  log_step "atenet-router flags are left as they are."
  if [[ -n "${PROJECT_ID:-}" ]]; then
    local gsa="conf-gateway@${PROJECT_ID}.iam.gserviceaccount.com"
    log_step "conf-gateway service account and its IAM bindings are left in place."
    echo "  Remove them with:"
    echo "    gcloud iam service-accounts delete ${gsa} --project ${PROJECT_ID}"
    echo "    gcloud projects remove-iam-policy-binding ${PROJECT_ID} --member serviceAccount:${gsa} --role roles/aiplatform.user"
  fi
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -o errexit -o nounset -o pipefail
  ROOT="$(git rev-parse --show-toplevel 2>/dev/null || (cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd))"
  cd "${ROOT}"

  action=""
  extra_args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --deploy|--deploy-demo-conference)
        action="deploy"
        shift
        ;;
      --delete|--delete-demo-conference)
        action="delete"
        shift
        ;;
      -h|--help)
        echo "Usage: $0 [--deploy|--delete] [--patch-router]"
        echo "  --patch-router patches atenet-router's flags instead of failing when they"
        echo "  are too low for LLM turns. It is forwarded ahead of --deploy so it always"
        echo "  takes effect, regardless of where it appears on this command line."
        exit 0
        ;;
      *)
        extra_args+=("$1")
        shift
        ;;
    esac
  done

  if [[ -z "${action}" ]]; then
    echo "Usage: $0 [--deploy|--delete] [--patch-router]" >&2
    exit 1
  fi

  # extra_args (e.g. --patch-router) precede the action flag: install-ate.sh
  # processes flags in the order given, and --patch-router only has an effect
  # if it runs before --deploy-demo-conference starts the deploy.
  if [[ "${action}" == "deploy" ]]; then
    exec "${ROOT}/hack/install-ate.sh" ${extra_args[@]+"${extra_args[@]}"} --deploy-demo-conference
  else
    exec "${ROOT}/hack/install-ate.sh" ${extra_args[@]+"${extra_args[@]}"} --delete-demo-conference
  fi
fi

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

# Fallback definitions if invoked or sourced directly outside install-ate.sh
if ! declare -F log_step >/dev/null 2>&1; then
  log_step() {
    echo -e "\033[1;36m[step]: $1\033[0m"
  }
fi

if ! declare -F ate_demo_flag_unhandled >/dev/null 2>&1; then
  ate_demo_flag_unhandled() {
    ATE_DEMO_FLAG_HANDLED=false
  }
fi

if ! declare -F run_kubectl >/dev/null 2>&1; then
  run_kubectl() {
    kubectl ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
  }
fi

if ! declare -F run_kubectl_ate >/dev/null 2>&1; then
  run_kubectl_ate() {
    if command -v kubectl-ate >/dev/null 2>&1; then
      kubectl-ate ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
    else
      go run ./cmd/kubectl-ate ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
    fi
  }
fi

if ! declare -F run_ko >/dev/null 2>&1; then
  run_ko() {
    ko "$@"
  }
fi

if ! declare -F render_demo_manifest >/dev/null 2>&1; then
  render_demo_manifest() {
    local bucket="${BUCKET_NAME:-ate-snapshots}"
    sed -e "s|\${BUCKET_NAME}|${bucket}|g" "$1"
  }
fi

if ! declare -F deploy_substrate_demo >/dev/null 2>&1; then
  deploy_substrate_demo() {
    local render_fn="$1"
    local pool_manifest="$2"
    local atespace="$3"
    local pool="$4"
    local golden_timeout="$5"
    shift 5

    "${render_fn}" "${pool_manifest}" | run_ko apply -f -
    log_step "Waiting for workerpool/${pool}..."
    run_kubectl wait --for=condition=Ready "workerpool/${pool}" -n "${atespace}" --timeout="${golden_timeout}s" 2>/dev/null || true

    run_kubectl_ate create atespace "${atespace}" 2>/dev/null || true

    local template_manifest template
    while (($# > 0)); do
      template_manifest="$1"
      template="$2"
      shift 2
      "${render_fn}" "${template_manifest}" | run_ko resolve -f - | run_kubectl_ate create actor-template -f - 2>/dev/null || true
    done
  }
fi

if ! declare -F delete_substrate_demo >/dev/null 2>&1; then
  delete_substrate_demo() {
    local render_fn="$1"
    local pool_manifest="$2"
    local atespace="$3"
    shift 3

    for template in "$@"; do
      run_kubectl_ate delete actor-template "${template}" -a "${atespace}" 2>/dev/null || true
    done
    run_kubectl_ate delete atespace "${atespace}" 2>/dev/null || true
    "${render_fn}" "${pool_manifest}" | run_kubectl delete --ignore-not-found -f - 2>/dev/null || true
  }
fi

demo-conference_usage() {
  echo "  --deploy-demo-conference                      Deploy interactive conference demo"
  echo "  --delete-demo-conference                      Delete interactive conference demo"
}

demo-conference_cmdline() {
  case "${1}" in
    --deploy-demo-conference|--deploy) demo-conference_deploy ;;
    --delete-demo-conference|--delete) demo-conference_delete ;;
    *)
      ate_demo_flag_unhandled
      ;;
  esac
}

demo-conference_deploy() {
  log_step "demo-conference_deploy"
  if [[ -z "${BUCKET_NAME:-}" ]]; then
    echo "BUCKET_NAME must be set" >&2
    return 1
  fi

  log_step "Deploying conference worker pool and actor template..."
  deploy_substrate_demo render_demo_manifest \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-workers 300 \
    demos/conference/conference-template.yaml.tmpl conf-agent

  log_step "Creating 50 attendee actors (attendee-01 through attendee-50)..."
  local created=0
  local existing=0
  for i in $(seq 1 50); do
    local actor
    actor=$(printf "attendee-%02d" "${i}")
    if run_kubectl_ate get actor "${actor}" -a ate-demo-conf >/dev/null 2>&1; then
      existing=$((existing + 1))
    else
      if run_kubectl_ate create actor "${actor}" -a ate-demo-conf --template conf-agent >/dev/null 2>&1; then
        created=$((created + 1))
      else
        echo "warning: failed to create actor ${actor}" >&2
      fi
    fi
  done
  log_step "Attendee actors ready: ${created} created, ${existing} already existed."

  echo ""
  echo "==================================================================="
  echo "  Interactive Conference Demo Deployed Successfully!"
  echo "==================================================================="
  echo ""
  echo "Next steps to run the presentation & attendee experience:"
  echo ""
  echo "1. Port-forward the router and status services:"
  echo "   kubectl \${KUBECTL_CONTEXT:+--context=\${KUBECTL_CONTEXT}} port-forward -n ate-system svc/atenet-router 8000:80 &"
  echo "   kubectl \${KUBECTL_CONTEXT:+--context=\${KUBECTL_CONTEXT}} port-forward -n ate-system svc/ate-status 4040:80 &"
  echo ""
  echo "2. Start the conference edge gateway:"
  echo "   export GEMINI_API_KEY=\"<your-api-key>\"  # Optional: defaults to mock LLM"
  echo "   go run ./demos/conference/cmd/conf-gateway"
  echo ""
  echo "3. Open the web interfaces in your browser:"
  echo "   - Presentation Stage Dashboard: http://localhost:8090/stage"
  echo "   - Mobile Attendee Chat:         http://localhost:8090/chat?attendee=attendee-01"
  echo "==================================================================="
  echo ""
}

demo-conference_delete() {
  log_step "demo-conference_delete"
  delete_substrate_demo render_demo_manifest \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-agent
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -o errexit -o nounset -o pipefail
  ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
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
        echo "Usage: $0 [--deploy|--delete]"
        exit 0
        ;;
      *)
        extra_args+=("$1")
        shift
        ;;
    esac
  done

  if [[ -z "${action}" ]]; then
    echo "Usage: $0 [--deploy|--delete]" >&2
    exit 1
  fi

  if [[ "${action}" == "deploy" ]]; then
    exec "${ROOT}/hack/install-ate.sh" --deploy-demo-conference "${extra_args[@]}"
  else
    exec "${ROOT}/hack/install-ate.sh" --delete-demo-conference "${extra_args[@]}"
  fi
fi

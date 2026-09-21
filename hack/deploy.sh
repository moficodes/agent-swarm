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

demo-conference_cmdline() {
  case "${1}" in
    --deploy-demo-conference) demo-conference_deploy ;;
    --delete-demo-conference) demo-conference_delete ;;
    *)
      ate_demo_flag_unhandled
      ;;
  esac
}

demo-conference_deploy() {
  log_step "demo-conference_deploy"

  log_step "Ensuring atenet-router has --parked-request-budget=60s..."
  local current_args
  current_args="$(run_kubectl get deployment atenet-router -n ate-system -o jsonpath='{.spec.template.spec.containers[?(@.name=="atenet-router")].args}' 2>/dev/null || true)"
  if [[ "${current_args}" != *"--parked-request-budget=60s"* ]]; then
    run_kubectl patch deployment atenet-router -n ate-system --type=json \
      -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--parked-request-budget=60s"}]' 2>/dev/null || true
    run_kubectl rollout status deployment/atenet-router -n ate-system --timeout=60s 2>/dev/null || true
  fi

  deploy_substrate_demo render_demo_manifest \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-workers 300 \
    demos/conference/conference-template.yaml.tmpl conf-agent

  log_step "Creating 50 attendee actors in ate-demo-conf..."
  for i in $(seq 1 50); do
    local actor
    actor=$(printf "attendee-%02d" "${i}")
    run_kubectl_ate create actor "${actor}" -a ate-demo-conf --template conf-agent 2>/dev/null || true
  done
  log_step "Successfully deployed 50 attendee actors in ate-demo-conf on 2-worker pool."
}

demo-conference_delete() {
  log_step "demo-conference_delete"
  log_step "Deleting 50 attendee actors in ate-demo-conf..."
  for i in $(seq 1 50); do
    local actor
    actor=$(printf "attendee-%02d" "${i}")
    run_kubectl_ate delete actor "${actor}" -a ate-demo-conf 2>/dev/null || true
  done
  delete_substrate_demo render_demo_manifest \
    demos/conference/conference.yaml.tmpl ate-demo-conf conf-agent
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

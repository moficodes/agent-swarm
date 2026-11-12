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
# Creates a GKE cluster that can run Substrate and this demo. It reads the
# variable names substrate-gke's tools/setup-gcp writes to .ate-dev-env.sh, so
# sourcing that file works. The bucket, image registry and IAM bindings are
# per project, not per cluster; README.md's Prerequisites set them up. Sourced by
# hack/test-deploy.sh for its cluster-free tests.

# shellcheck source=hack/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER_LOCATION="${CLUSTER_LOCATION:-us-west1-c}"
NETWORK="${NETWORK:-default}"
SUBNETWORK="${SUBNETWORK:-default}"
DEMO_CONF_PROFILE="${DEMO_CONF_PROFILE:-swarm}"
# Substrate needs GKE 1.37 or later, which some zones offer only in RAPID.
GKE_RELEASE_CHANNEL="${GKE_RELEASE_CHANNEL:-rapid}"
GKE_MINOR_VERSION="${GKE_MINOR_VERSION:-1.37}"
CC_DRY_RUN="${CC_DRY_RUN:-false}"

cc_profile() {
  echo "${DEMO_CONF_PROFILE:-swarm}"
}

cc_validate_profile() {
  local p
  p="$(cc_profile)"
  case "${p}" in
    swarm | megaswarm) return 0 ;;
    *)
      echo "error: unknown DEMO_CONF_PROFILE '${p}' (expected 'swarm' or 'megaswarm')" >&2
      return 1
      ;;
  esac
}

cc_node_machine_type() {
  if [[ -n "${NODE_MACHINE_TYPE:-}" ]]; then
    echo "${NODE_MACHINE_TYPE}"
  elif [[ "$(cc_profile)" == "megaswarm" ]]; then
    echo "c3-standard-8"
  else
    echo "c3-standard-4"
  fi
}

cc_node_count() {
  if [[ -n "${NODE_COUNT:-}" ]]; then
    echo "${NODE_COUNT}"
  elif [[ "$(cc_profile)" == "megaswarm" ]]; then
    echo "16"
  else
    echo "2"
  fi
}

# A channel cluster auto-upgrades its nodes, which recreates worker pods
# mid-demo. The exclusion blocks node and minor upgrades until the version's
# end of support; control-plane patches still apply.
CC_EXCLUSION=substrate-no-node-upgrades

# cc_run runs a command, or prints it under --dry-run.
cc_run() {
  if [[ "${CC_DRY_RUN}" == true ]]; then
    echo "+ $*"
  else
    "$@"
  fi
}

# cc_pick_version prints the newest version of MINOR (e.g. 1.37) in the
# semicolon-separated, newest-first list gcloud reports for a channel.
cc_pick_version() {
  local minor="$1" versions="$2" v
  local -a list
  IFS=';' read -ra list <<<"${versions}"
  for v in "${list[@]}"; do
    if [[ "${v}" == "${minor}."* ]]; then
      echo "${v}"
      return 0
    fi
  done
  return 1
}

# cc_resolve_version prints the newest GKE_MINOR_VERSION release in
# GKE_RELEASE_CHANNEL at CLUSTER_LOCATION.
cc_resolve_version() {
  local channel versions v
  channel="$(tr '[:lower:]' '[:upper:]' <<<"${GKE_RELEASE_CHANNEL}")"
  versions="$(gcloud container get-server-config --project "${PROJECT_ID}" --location "${CLUSTER_LOCATION}" \
    --flatten=channels --filter="channels.channel=${channel}" --format='value(channels.validVersions)')" || return 1
  if ! v="$(cc_pick_version "${GKE_MINOR_VERSION}" "${versions}")"; then
    echo "error: no GKE ${GKE_MINOR_VERSION} release in the ${channel} channel at ${CLUSTER_LOCATION}; it offers: ${versions}" >&2
    return 1
  fi
  echo "${v}"
}

# cc_create_args prints the `gcloud beta container clusters create` arguments
# for VERSION, one per line.
cc_create_args() {
  local version="$1"
  printf '%s\n' \
    "${CLUSTER_NAME}" \
    --project "${PROJECT_ID}" \
    --location "${CLUSTER_LOCATION}" \
    --release-channel "${GKE_RELEASE_CHANNEL}" \
    --cluster-version "${version}" \
    --num-nodes "$(cc_node_count)" \
    --machine-type "$(cc_node_machine_type)" \
    --enable-nested-virtualization \
    --network "${NETWORK}" \
    --subnetwork "${SUBNETWORK}" \
    --workload-pool "${PROJECT_ID}.svc.id.goog" \
    --managed-otel-scope COLLECTION_AND_INSTRUMENTATION_COMPONENTS
}

# cc_exclusion_args prints the `gcloud container clusters update` arguments
# that add the node-upgrade exclusion, one per line.
cc_exclusion_args() {
  printf '%s\n' \
    "${CLUSTER_NAME}" \
    --project "${PROJECT_ID}" \
    --location "${CLUSTER_LOCATION}" \
    "--add-maintenance-exclusion-name=${CC_EXCLUSION}" \
    --add-maintenance-exclusion-scope=no_minor_or_node_upgrades \
    --add-maintenance-exclusion-until-end-of-support
}

# cc_lines_to_args reads stdin, one argument per line, into the array args
# (bash 3.2 on macOS has no mapfile).
cc_lines_to_args() {
  local line
  args=()
  while IFS= read -r line; do
    args+=("${line}")
  done
}

cc_describe() {
  gcloud container clusters describe "${CLUSTER_NAME}" --project "${PROJECT_ID}" \
    --location "${CLUSTER_LOCATION}" --format="$1"
}

cc_create() {
  local -a args
  local version context previous
  context="gke_${PROJECT_ID}_${CLUSTER_LOCATION}_${CLUSTER_NAME}"

  if cc_describe 'value(name)' >/dev/null 2>&1; then
    log_step "Cluster ${CLUSTER_NAME} already exists; checking its settings."
  else
    version="$(cc_resolve_version)"
    log_step "Creating ${CLUSTER_NAME} on GKE ${version} (${GKE_RELEASE_CHANNEL})..."
    cc_lines_to_args < <(cc_create_args "${version}")
    cc_run gcloud beta container clusters create "${args[@]}"
  fi

  # gcloud cannot set a maintenance exclusion on create.
  if ! cc_describe 'value(maintenancePolicy.window.maintenanceExclusions)' 2>/dev/null | grep -qF "${CC_EXCLUSION}="; then
    log_step "Adding maintenance exclusion ${CC_EXCLUSION}..."
    cc_lines_to_args < <(cc_exclusion_args)
    cc_run gcloud container clusters update "${args[@]}"
  fi

  # get-credentials also makes the cluster the current kube context; put
  # the previous one back, since hack/deploy.sh takes KUBECTL_CONTEXT.
  previous="$(kubectl config current-context 2>/dev/null || true)"
  cc_run gcloud container clusters get-credentials "${CLUSTER_NAME}" --project "${PROJECT_ID}" --location "${CLUSTER_LOCATION}"
  if [[ -n "${previous}" && "${previous}" != "${context}" ]]; then
    cc_run kubectl config use-context "${previous}" >/dev/null
  fi

  log_step "Cluster ready. Deploy with:"
  if [[ "$(cc_profile)" == "megaswarm" ]]; then
    echo "  KUBECTL_CONTEXT=${context} hack/deploy.sh up --profile=megaswarm"
  else
    echo "  KUBECTL_CONTEXT=${context} hack/deploy.sh up"
  fi
}

cc_main() {
  while (($# > 0)); do
    case "$1" in
      --dry-run) CC_DRY_RUN=true ;;
      --profile=*) DEMO_CONF_PROFILE="${1#--profile=}" ;;
      --profile)
        if (($# < 2)); then
          echo "error: --profile requires an argument (swarm or megaswarm)" >&2
          cc_usage
          return 1
        fi
        DEMO_CONF_PROFILE="$2"
        shift
        ;;
      -h | --help) cc_usage; return 0 ;;
      *) echo "error: unknown argument $1" >&2; cc_usage; return 1 ;;
    esac
    shift
  done
  if ! cc_validate_profile; then
    cc_usage
    return 1
  fi
  if [[ -z "${PROJECT_ID:-}" || -z "${CLUSTER_NAME:-}" ]]; then
    echo "error: PROJECT_ID and CLUSTER_NAME are required" >&2
    cc_usage
    return 1
  fi
  local tool
  for tool in gcloud kubectl; do
    command -v "${tool}" >/dev/null || { echo "error: ${tool} is required" >&2; return 1; }
  done
  cc_create
}

cc_usage() {
  echo "Usage: $0 [--profile=swarm|megaswarm] [--dry-run]"
  echo "  Creates a GKE cluster for Substrate on the newest GKE_MINOR_VERSION release, with a"
  echo "  maintenance exclusion against node upgrades. On an existing cluster it adds the"
  echo "  exclusion if missing and leaves the version alone. --dry-run prints the gcloud"
  echo "  commands that change anything."
  echo "  --profile=swarm|megaswarm select cluster size preset (default: DEMO_CONF_PROFILE=swarm):"
  echo "                             swarm     : 2 x c3-standard-4 nodes"
  echo "                             megaswarm : 16 x c3-standard-8 nodes"
  echo "  Required env: PROJECT_ID, CLUSTER_NAME"
  echo "  Optional env: DEMO_CONF_PROFILE (default swarm; 'swarm' or 'megaswarm'),"
  echo "                CLUSTER_LOCATION (default us-west1-c), NETWORK, SUBNETWORK (default default),"
  echo "                NODE_MACHINE_TYPE (default c3-standard-4 for swarm, c3-standard-8 for megaswarm),"
  echo "                NODE_COUNT (default 2 for swarm, 16 for megaswarm),"
  echo "                GKE_RELEASE_CHANNEL (default rapid), GKE_MINOR_VERSION (default 1.37)"
  echo "  substrate-gke's .ate-dev-env.sh sets these names; source it or export them yourself."
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -o errexit -o nounset -o pipefail
  cd "$(dirname "${BASH_SOURCE[0]}")/.."
  cc_main "$@"
fi

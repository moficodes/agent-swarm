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

# Helpers for hack/deploy.sh, ported from substrate's hack/install-ate.sh.
# Sourced; expects to run from the repo root.

SUBSTRATE_MODULE=go.ate.dev/substrate

log_step() {
  echo -e "\033[1;36m[step]: $1\033[0m"
}

run_kubectl() {
  kubectl ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
}

# log_target_context logs the cluster context a deploy or delete run will
# target. `kubectl --context=X config current-context` prints the
# kubeconfig file's current-context, not X, so an explicit KUBECTL_CONTEXT
# is reported directly instead of asking kubectl.
log_target_context() {
  if [[ -n "${KUBECTL_CONTEXT:-}" ]]; then
    log_step "Target cluster context: ${KUBECTL_CONTEXT}"
  else
    log_step "Target cluster context: $(run_kubectl config current-context)"
  fi
}

# run_kubectl_fatal runs kubectl and aborts the deploy if it fails.
run_kubectl_fatal() {
  if ! run_kubectl "$@"; then
    echo "error: kubectl $* failed" >&2
    exit 1
  fi
}

# run_kubectl_ate runs the kubectl-ate pinned in go.mod.
run_kubectl_ate() {
  go tool kubectl-ate ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
}

# substrate_pin prints the substrate version pinned in go.mod: the release
# tag (e.g. v0.2.0) when go.mod names one, or the 12-character commit taken
# from a pseudo-version otherwise.
substrate_pin() {
  local v
  v="$(go list -m -f '{{.Version}}' "${SUBSTRATE_MODULE}")" || return 1
  if [[ "${v}" =~ [-.][0-9]{14}-([0-9a-f]{12})$ ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    echo "${v}"
  fi
}

# substrate_version_from_output normalizes `ateapi --version` output
# (e.g. "go.ate.dev/substrate.Version=v0.6.0 commit=d3f23635... built=...")
# to a version label ("v0.6.0", "v0.6.0-dirty", or a 12-character commit
# prefix) that substrate_labels_match can compare against substrate_pin.
substrate_version_from_output() {
  local out="$1" ver commit="" dirty=""
  [[ -n "${out}" ]] || return 0
  ver="${out%%[[:space:]]*}"
  ver="${ver#*.Version=}"
  if [[ "${ver}" == *+dirty ]]; then
    ver="${ver%+dirty}"
    dirty="-dirty"
  elif [[ "${ver}" == *-dirty ]]; then
    ver="${ver%-dirty}"
    dirty="-dirty"
  fi
  if [[ "${out}" =~ commit=([0-9a-f]+)(-dirty)? ]]; then
    commit="${BASH_REMATCH[1]}"
    [[ -n "${BASH_REMATCH[2]}" ]] && dirty="-dirty"
  fi
  # A Go pseudo-version or "(devel)" from ReadBuildInfo names a commit, not a release tag.
  if [[ "${ver}" == "(devel)" || "${ver}" =~ [-.][0-9]{14}-([0-9a-f]{12})$ ]]; then
    if [[ -n "${commit}" ]]; then
      ver="${commit:0:12}"
    elif [[ -n "${BASH_REMATCH[1]:-}" ]]; then
      ver="${BASH_REMATCH[1]}"
    fi
  fi
  echo "${ver}${dirty}"
}

# substrate_label_is_pin succeeds if one clean (non-"-dirty") Substrate
# version LABEL names PIN. Substrate stamps binaries (and, prior to v0.5.0,
# nodes) with the release tag or `git describe --tags --always` of the
# checkout it was installed from, so a tag PIN must match the label
# exactly, and a commit PIN matches a label that is a prefix of it, either
# bare ("c7a1be17") or in describe form after a tag ("v0.1.0-12-gc7a1be17").
substrate_label_is_pin() {
  local pin="$1" label="$2"
  [[ -n "${label}" ]] || return 1
  if [[ ! "${pin}" =~ ^[0-9a-f]{12}$ ]]; then
    [[ "${label}" == "${pin}" ]]
    return
  fi
  if [[ "${label}" =~ ^.+-[0-9]+-g([0-9a-f]+)$ ]]; then
    label="${BASH_REMATCH[1]}"
  fi
  [[ "${pin}" == "${label}"* ]]
}

# substrate_labels_match succeeds if at least one line of LABELS is set and
# every set line names PIN (see substrate_label_is_pin). LABELS holds one
# node's ate.dev/substrate-version per line; unlabeled nodes are empty lines.
# ALLOW_DIRTY (default false; pass "true", e.g. from
# DEMO_CONF_ALLOW_DIRTY_SUBSTRATE) also accepts "<label>-dirty", which
# Substrate stamps on nodes when installed from a modified checkout.
substrate_labels_match() {
  local pin="$1" labels="$2" allow_dirty="${3:-false}" label seen=false
  while IFS= read -r label; do
    [[ -z "${label}" ]] && continue
    if substrate_label_is_pin "${pin}" "${label}"; then
      seen=true
      continue
    fi
    if [[ "${allow_dirty}" == true && "${label}" == *-dirty ]] \
        && substrate_label_is_pin "${pin}" "${label%-dirty}"; then
      seen=true
      continue
    fi
    return 1
  done <<<"${labels}"
  [[ "${seen}" == true ]]
}

# substrate_label_mismatch_kind classifies why LABELS failed
# substrate_labels_match against PIN, so conf_check_substrate can print a
# message that fits the actual cause instead of always suggesting a pin
# bump. Prints one of:
#   unlabeled  no node carries ate.dev/substrate-version at all
#   dirty      every set label is "<PIN's label>-dirty": the cluster runs
#              the pinned version, but from a modified checkout
#   mismatch   otherwise: at least one node runs a different version
substrate_label_mismatch_kind() {
  local pin="$1" labels="$2" label seen=false all_dirty=true
  while IFS= read -r label; do
    [[ -z "${label}" ]] && continue
    seen=true
    if [[ "${label}" == *-dirty ]] && substrate_label_is_pin "${pin}" "${label%-dirty}"; then
      continue
    fi
    all_dirty=false
  done <<<"${labels}"
  if [[ "${seen}" == false ]]; then
    echo unlabeled
  elif [[ "${all_dirty}" == true ]]; then
    echo dirty
  else
    echo mismatch
  fi
}

# substrate_repo_clean succeeds if REPO's working tree has no uncommitted
# changes. install-ate.sh stamps nodes "<commit>-dirty" when run from a
# modified checkout, which substrate_labels_match then refuses by default,
# so conf_check_substrate checks this before installing from SUBSTRATE_REPO.
substrate_repo_clean() {
  local repo="$1"
  [[ -z "$(git -C "${repo}" status --porcelain)" ]]
}

# wait_for_pool_rollout_fatal waits for a WorkerPool's Deployment, which
# ate-controller creates after the apply returns, to exist and roll out.
wait_for_pool_rollout_fatal() {
  local pool="$1" namespace="$2" timeout="${3:-300s}"
  if ! run_kubectl wait --for=create "deployment/${pool}" -n "${namespace}" --timeout="${timeout}" \
      || ! run_kubectl rollout status "deployment/${pool}" -n "${namespace}" --timeout="${timeout}"; then
    echo "error: worker pool ${pool} did not roll out" >&2
    exit 1
  fi
}

# wait_actortemplate_ready polls an ActorTemplate until its golden snapshot
# exists, failing fast when the template reconciler reports an error.
wait_actortemplate_ready() {
  local atespace="$1" template="$2" timeout_secs="${3:-300}"
  local deadline=$((SECONDS + timeout_secs)) json snapshot error_message
  while ((SECONDS < deadline)); do
    if json=$(run_kubectl_ate get actor-template "${template}" -a "${atespace}" -o json 2>/dev/null); then
      snapshot=$(jq -r '.status.goldenSnapshotStatus.goldenTag.name // empty' <<<"${json}")
      [[ -n "${snapshot}" ]] && return 0
      error_message=$(jq -r '.status.goldenSnapshotStatus.errorMessage // empty' <<<"${json}")
      if [[ -n "${error_message}" ]]; then
        echo "actor template ${atespace}/${template} failed: ${error_message}" >&2
        return 1
      fi
    fi
    sleep 5
  done
  echo "timed out waiting for actor template ${atespace}/${template} golden snapshot" >&2
  return 1
}

# ensure_atespace creates the atespace if it does not already exist.
ensure_atespace() {
  local atespace="$1"
  if ! run_kubectl_ate create atespace "${atespace}" >/dev/null 2>&1 \
      && ! run_kubectl_ate get atespace "${atespace}" >/dev/null 2>&1; then
    echo "error: failed to create atespace ${atespace}" >&2
    exit 1
  fi
}

# create_actor_template renders a protojson ActorTemplate, resolves its ko://
# images, creates it and waits for its golden snapshot. Templates are
# immutable, so an existing one is kept.
create_actor_template() {
  local render_fn="$1" manifest="$2" atespace="$3" template="$4" timeout_secs="${5:-300}"
  if ! "${render_fn}" "${manifest}" | ko resolve -f - | run_kubectl_ate create actor-template -f -; then
    if run_kubectl_ate get actor-template "${template}" -a "${atespace}" >/dev/null 2>&1; then
      log_step "actor template ${atespace}/${template} already exists; keeping it"
    else
      echo "error: failed to create actor template ${atespace}/${template}" >&2
      exit 1
    fi
  fi
  log_step "Waiting for the ${atespace}/${template} golden snapshot..."
  wait_actortemplate_ready "${atespace}" "${template}" "${timeout_secs}" || exit 1
}

# delete_template_actors deletes every actor created from ATESPACE/TEMPLATE.
delete_template_actors() {
  local tmpl_atespace="$1" tmpl="$2" actors_json atespace actor
  if ! actors_json=$(run_kubectl_ate get actors -A -o json 2>/dev/null); then
    echo "warning: could not list actors; skipping actor cleanup" >&2
    return 0
  fi
  log_step "Deleting actors for ${tmpl_atespace}/${tmpl}"
  while IFS=$'\t' read -r atespace actor; do
    [[ -z "${actor}" ]] && continue
    run_kubectl_ate delete actor "${actor}" -a "${atespace}"
  done < <(jq -r --arg as "${tmpl_atespace}" --arg tmpl "${tmpl}" \
    '.actors[]? | select(.actorTemplate.atespace == $as and .actorTemplate.name == $tmpl) | "\(.metadata.atespace)\t\(.metadata.name)"' \
    <<<"${actors_json}")
}

# delete_actor_template deletes a template's actors, then the template,
# which server-side also removes its golden actor and snapshot.
delete_actor_template() {
  local atespace="$1" template="$2"
  delete_template_actors "${atespace}" "${template}"
  run_kubectl_ate delete actor-template "${template}" -a "${atespace}" 2>/dev/null \
    || log_step "actor template ${atespace}/${template} not deleted (may not exist)"
}

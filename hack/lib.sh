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

SUBSTRATE_MODULE=github.com/agent-substrate/substrate

log_step() {
  echo -e "\033[1;36m[step]: $1\033[0m"
}

run_kubectl() {
  kubectl ${KUBECTL_CONTEXT:+--context=${KUBECTL_CONTEXT}} "$@"
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

# substrate_pin prints the 12-character commit of the substrate module
# pinned in go.mod, taken from its pseudo-version.
substrate_pin() {
  local v
  v="$(go list -m -f '{{.Version}}' "${SUBSTRATE_MODULE}")" || return 1
  echo "${v##*-}"
}

# substrate_labels_match succeeds if at least one line of LABELS is set and
# every set line is a prefix of PIN. LABELS holds one node's
# ate.dev/substrate-version per line; unlabeled nodes are empty lines.
substrate_labels_match() {
  local pin="$1" labels="$2" label seen=false
  while IFS= read -r label; do
    [[ -z "${label}" ]] && continue
    [[ "${pin}" == "${label}"* ]] || return 1
    seen=true
  done <<<"${labels}"
  [[ "${seen}" == true ]]
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

# prepare_actor_for_delete suspends an actor (resuming it first if paused)
# until DeleteActor accepts it.
prepare_actor_for_delete() {
  local actor="$1" atespace="$2" deadline=$((SECONDS + 120)) json state
  while ((SECONDS < deadline)); do
    json=$(run_kubectl_ate get actor "${actor}" -a "${atespace}" -o json 2>/dev/null) || return 0
    state=$(jq -r '.status.state // empty' <<<"${json}")
    case "${state}" in
      ACTOR_STATE_SUSPENDED | ACTOR_STATE_CRASHED | ACTOR_STATE_DELETING) return 0 ;;
      ACTOR_STATE_PAUSED) run_kubectl_ate resume actor "${actor}" -a "${atespace}" -o json >/dev/null ;;
      ACTOR_STATE_RUNNING) run_kubectl_ate suspend actor "${actor}" -a "${atespace}" -o json >/dev/null ;;
      ACTOR_STATE_RESUMING | ACTOR_STATE_SUSPENDING | ACTOR_STATE_PAUSING) ;;
      *)
        echo "cannot delete actor ${actor}: unexpected state ${state}" >&2
        return 1
        ;;
    esac
    sleep 2
  done
  echo "timed out waiting for actor ${actor} to become deletable" >&2
  return 1
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
    prepare_actor_for_delete "${actor}" "${atespace}"
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

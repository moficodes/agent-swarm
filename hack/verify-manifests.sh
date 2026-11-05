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

# Renders the conference demo manifests with placeholder values and checks
# that no placeholder survives and that the YAML parses as Kubernetes
# objects. This is offline: it never contacts a cluster, so it cannot pick
# up or depend on whatever kube context happens to be current.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "${ROOT}"

GATEWAY_MANIFEST="manifests/gateway.yaml.tmpl"
INGRESS_MANIFEST="manifests/gateway-ingress.yaml.tmpl"
ACTOR_TEMPLATE="manifests/actortemplate.yaml.tmpl"
WORKERPOOL_MANIFEST="manifests/workerpool.yaml.tmpl"
AUTOSCALING_MANIFEST="manifests/autoscaling.yaml.tmpl"
APISERVICE_MANIFEST="manifests/autoscaling-apiservice.yaml.tmpl"

# render substitutes every install-script placeholder in $1, using $2 for
# ${DEMO_DOMAIN}. Callers exercise the ingress template with both an empty
# and a non-empty domain, since the install script applies it only when
# DEMO_DOMAIN is set.
render() {
  sed -e "s|\${GATEWAY_IMAGE}|gcr.io/p/conf-gateway@sha256:0000000000000000000000000000000000000000000000000000000000000000|g" \
      -e "s|\${GATEWAY_GSA}|conf-gateway@p.iam.gserviceaccount.com|g" \
      -e "s|\${VERTEX_PROJECT}|p|g" \
      -e "s|\${VERTEX_LOCATION}|global|g" \
      -e "s|\${DEMO_MOCK_LLM}|false|g" \
      -e "s|\${SANDBOX_WORKSPACE}|/tmp/workspace|g" \
      -e "s|\${ACTOR_COUNT}|50|g" \
      -e "s|\${DEMO_DOMAIN}|$2|g" \
      -e "s|\${TEMPLATE_REVISION}|1|g" \
      -e "s|\${WORKER_REPLICAS}|2|g" \
      -e "s|\${MIN_WORKERS}|2|g" \
      -e "s|\${MAX_WORKERS}|5|g" \
      -e "s|\${ACTORS_PER_WORKER}|4|g" \
      -e "s|\${ALLOWED_GOOGLE_DOMAINS}||g" \
      -e "s|\${GOOGLE_CLIENT_ID}||g" \
      -e "s|\${FIREBASE_API_KEY}||g" \
      -e "s|\${CHAT_RATE_LIMIT_RPM}|10|g" \
      -e "s|\${SESSION_RATE_LIMIT_RPM}|10|g" \
      -e "s|\${WORKER_CPU_LIMIT}|4500m|g" \
      -e "s|\${WORKER_MEMORY_REQUEST}|2304Mi|g" \
      "$1"
}

# check_objects pipes a rendered manifest through the checkmanifests helper,
# which parses it with the module's vendored sigs.k8s.io/yaml and confirms
# every document has an apiVersion, a kind and a metadata.name — all without
# contacting a cluster.
check_objects() {
  go run ./hack/checkmanifests <<<"$1"
}

status=0
for f in "${GATEWAY_MANIFEST}" "${INGRESS_MANIFEST}" "${ACTOR_TEMPLATE}" \
         "${WORKERPOOL_MANIFEST}" "${AUTOSCALING_MANIFEST}" "${APISERVICE_MANIFEST}"; do
  out="$(render "${f}" "demo.example.com")"
  # shellcheck disable=SC2016 # matches a literal, unrendered ${
  if grep -n '\${' <<<"${out}"; then
    echo "error: ${f} has unsubstituted placeholders" >&2
    status=1
  fi
done

# The ingress template is also rendered with an empty domain, the value the
# install script passes when DEMO_DOMAIN is unset and the Ingress is not
# applied.
out_empty_domain="$(render "${INGRESS_MANIFEST}" "")"
# shellcheck disable=SC2016 # matches a literal, unrendered ${
if grep -n '\${' <<<"${out_empty_domain}"; then
  echo "error: ${INGRESS_MANIFEST} has unsubstituted placeholders (empty domain)" >&2
  status=1
fi

out="$(render "${GATEWAY_MANIFEST}" "demo.example.com")"
for want in "kind: ServiceAccount" "iam.gke.io/gcp-service-account" "kind: Deployment" \
            "resourceNames:" "- ate-client" "clustertrustbundles" \
            "endpointslices" "horizontalpodautoscalers" "workerpools" "- conf-workers" \
            "https://atenet-router.ate-system.svc:443" \
            "podidentity.podcert.ate.dev/identity" "servicedns.podcert.ate.dev/identity" \
            "readOnlyRootFilesystem: true" "runAsNonRoot: true" "runAsUser: 65532" \
            "cloud.google.com/backend-config: '{\"default\":\"conf-gateway\"}'"; do
  if ! grep -qF -- "${want}" <<<"${out}"; then
    echo "error: gateway manifest lacks ${want}" >&2
    status=1
  fi
done
# Actors have no egress; the gateway makes their model calls itself, so it
# exposes nothing to them.
for unwanted in "conf-gateway-internal" "8091"; do
  if grep -qF -- "${unwanted}" <<<"${out}"; then
    echo "error: gateway manifest still has ${unwanted}" >&2
    status=1
  fi
done
if ! check_objects "${out}"; then
  echo "error: ${GATEWAY_MANIFEST} failed offline object validation" >&2
  status=1
fi

out_ingress="$(render "${INGRESS_MANIFEST}" "demo.example.com")"
for want in "kind: ManagedCertificate" "kind: Ingress" "networking.gke.io/managed-certificates" "demo.example.com" \
            "apiVersion: cloud.google.com/v1" "kind: BackendConfig" "timeoutSec: 3600"; do
  if ! grep -qF -- "${want}" <<<"${out_ingress}"; then
    echo "error: ingress manifest lacks ${want}" >&2
    status=1
  fi
done
if ! check_objects "${out_ingress}"; then
  echo "error: ${INGRESS_MANIFEST} failed offline object validation" >&2
  status=1
fi
if ! check_objects "${out_empty_domain}"; then
  echo "error: ${INGRESS_MANIFEST} failed offline object validation (empty domain)" >&2
  status=1
fi

for f in "${WORKERPOOL_MANIFEST}" "${AUTOSCALING_MANIFEST}" "${APISERVICE_MANIFEST}"; do
  if ! check_objects "$(render "${f}" "")"; then
    echo "error: ${f} failed offline object validation" >&2
    status=1
  fi
done

out_template="$(render "${ACTOR_TEMPLATE}" "")"
for want in "preferredFidelity: SNAPSHOT_FIDELITY_MEMORY" "cpuMillicores: 1000" "memoryBytes: 536870912" "name: SANDBOX_WORKSPACE" "gemini-3.8-flash"; do
  if ! grep -qF -- "${want}" <<<"${out_template}"; then
    echo "error: actor template manifest lacks ${want}" >&2
    status=1
  fi
done
for unwanted in "onCommit:" "SANDBOX_CLASS_GVISOR" "sandboxConfig:" "quantity:" "storageLocation:"; do
  if grep -qF -- "${unwanted}" <<<"${out_template}"; then
    echo "error: actor template manifest still has removed field ${unwanted}" >&2
    status=1
  fi
done

out_workerpool="$(render "${WORKERPOOL_MANIFEST}" "")"
for want in 'ate.dev/api-access: "true"' "apiVersion: ate.dev/v1" "sandboxClasses:" "- name: gvisor" "ko://go.ate.dev/substrate/cmd/ateworker-gvisor"; do
  if ! grep -qF -- "${want}" <<<"${out_workerpool}"; then
    echo "error: workerpool manifest lacks ${want}" >&2
    status=1
  fi
done
if grep -qF -- "ate.dev/v1alpha1" <<<"${out_workerpool}"; then
  echo "error: workerpool manifest still references ate.dev/v1alpha1" >&2
  status=1
fi

out_autoscaling="$(render "${AUTOSCALING_MANIFEST}" "")"
if ! grep -qF -- "apiVersion: ate.dev/v1" <<<"${out_autoscaling}"; then
  echo "error: autoscaling manifest lacks scaleTargetRef apiVersion: ate.dev/v1" >&2
  status=1
fi
if grep -qF -- "ate.dev/v1alpha1" <<<"${out_autoscaling}"; then
  echo "error: autoscaling manifest still references ate.dev/v1alpha1" >&2
  status=1
fi

exit "${status}"

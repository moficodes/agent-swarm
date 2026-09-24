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
# that no placeholder survives and that the YAML parses as Kubernetes objects.
set -o errexit -o nounset -o pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "${ROOT}"

GATEWAY_MANIFEST="demos/conference/manifests/gateway.yaml.tmpl"
INGRESS_MANIFEST="demos/conference/manifests/gateway-ingress.yaml.tmpl"
ACTOR_TEMPLATE="demos/conference/conference-template.yaml.tmpl"

# render substitutes every install-script placeholder in $1, using $2 for
# ${DEMO_DOMAIN}. Callers exercise the ingress template with both an empty
# and a non-empty domain, since Task 17 only applies it when DEMO_DOMAIN is
# set.
render() {
  sed -e "s|\${GATEWAY_IMAGE}|gcr.io/p/conf-gateway@sha256:0000000000000000000000000000000000000000000000000000000000000000|g" \
      -e "s|\${GATEWAY_GSA}|conf-gateway@p.iam.gserviceaccount.com|g" \
      -e "s|\${VERTEX_PROJECT}|p|g" \
      -e "s|\${VERTEX_LOCATION}|us-central1|g" \
      -e "s|\${DEMO_MOCK_LLM}|false|g" \
      -e "s|\${DEMO_DOMAIN}|$2|g" \
      -e "s|\${BUCKET_NAME}|b|g" \
      "$1"
}

status=0
for f in "${GATEWAY_MANIFEST}" "${INGRESS_MANIFEST}" "${ACTOR_TEMPLATE}"; do
  out="$(render "${f}" "demo.example.com")"
  if grep -n '\${' <<<"${out}"; then
    echo "error: ${f} has unsubstituted placeholders" >&2
    status=1
  fi
done

# The ingress template is also rendered with an empty domain, the value
# Task 17 passes when DEMO_DOMAIN is unset and the Ingress is not applied.
out_empty_domain="$(render "${INGRESS_MANIFEST}" "")"
if grep -n '\${' <<<"${out_empty_domain}"; then
  echo "error: ${INGRESS_MANIFEST} has unsubstituted placeholders (empty domain)" >&2
  status=1
fi

out="$(render "${GATEWAY_MANIFEST}" "demo.example.com")"
for want in "kind: ServiceAccount" "iam.gke.io/gcp-service-account" "kind: Deployment" \
            "name: conf-gateway-internal" "resourceNames:" "- ate-client" "clustertrustbundles" \
            "endpointslices" "readOnlyRootFilesystem: true" "runAsNonRoot: true"; do
  if ! grep -qF -- "${want}" <<<"${out}"; then
    echo "error: gateway manifest lacks ${want}" >&2
    status=1
  fi
done
if command -v kubectl >/dev/null 2>&1; then
  kubectl apply --dry-run=client --validate=false -f - <<<"${out}" >/dev/null || status=1
fi

out_ingress="$(render "${INGRESS_MANIFEST}" "demo.example.com")"
for want in "kind: ManagedCertificate" "kind: Ingress" "networking.gke.io/managed-certificates" "demo.example.com"; do
  if ! grep -qF -- "${want}" <<<"${out_ingress}"; then
    echo "error: ingress manifest lacks ${want}" >&2
    status=1
  fi
done
if command -v kubectl >/dev/null 2>&1; then
  kubectl apply --dry-run=client --validate=false -f - <<<"${out_ingress}" >/dev/null || status=1
  kubectl apply --dry-run=client --validate=false -f - <<<"${out_empty_domain}" >/dev/null || status=1
fi

exit "${status}"

<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Substrate gaps this demo works around

This demo runs against upstream Substrate as-is, but it has to work around a
few gaps to do it. Each is listed here with its symptom, the workaround in
this repo, and where the real fix belongs.

## The actor rootfs overlay is not traversable by a non-root user

Substrate creates the actor rootfs overlay 0700, so a container that drops to
a non-root user cannot traverse it. The `conf-workload` container needs a
non-root workload user for the model-generated code it executes, but cannot
have one; instead it runs as root inside gVisor and grants itself
`CHOWN`, `SETUID`, `SETGID` and `DAC_OVERRIDE` so its executor can `setuid`
down to `nobody` per sandboxed run
(`manifests/actortemplate.yaml.tmpl`). The fix belongs in atelet
(`cmd/atelet/oci.go`, which builds the overlay) and ateom
(`internal/imagecache/bundle_linux.go`, which lays out the bundle), so the
overlay is traversable by whatever user the image specifies.

## `kubectl-ate` cannot manage egress policies

`kubectl-ate` has no verb to create, read or update an actor's egress
policy, so `hack/deploy.sh` cannot restrict `conf-workload`'s egress from the
deploy script. Actors already have no egress by default, which happens to be
what this demo wants (see "Actor to model" in the Architecture section of the
README), but a demo that needed to *allow* one specific destination would
have no scriptable way to do it. The fix belongs in `kubectl-ate` itself: an
`egress-policy` resource verb alongside its existing `actor` and
`actor-template` ones.

## `atenet-router`'s defaults are too short for LLM turns

`atenet-router`'s default `--parked-request-budget`, `--route-timeout` and
termination grace period are sized for short-lived requests, not multi-second
LLM turns; deployed as-is, the router either 503s a parked request or gets
SIGKILLed mid-drain during a rollout. `hack/deploy.sh` checks these values
before deploying and, with `--patch-router`, raises them; see the thresholds
and exact values in the README's "Deploy" section. The fix belongs in
`atenet-router`'s own defaults, or in a Substrate-level profile for
long-running backends, so a deploy that fronts an LLM does not need an
external patch step at all.

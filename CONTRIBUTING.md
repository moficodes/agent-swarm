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

# Contributing

- Run `make verify test` before sending changes.
- Write tests for new code; we will not merge code that lacks them.
- Use American English in code, comments and docs.
- Commit messages describe the change and why it was needed, with no issue
  or pull request references — the message outlives the branch and the PR
  thread.
- `make verify-local` exercises the full chat/approve/telemetry flow against
  a local gateway and router, without a cluster; run it for any change that
  touches the gateway or workload.
- Actor templates are immutable: bump `DEMO_CONF_TEMPLATE_REVISION` in
  `hack/deploy.sh` with any change to `manifests/actortemplate.yaml.tmpl`, or
  the deploy will keep running actors on the old template.

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

# Fails if a tracked source file lacks the Apache 2.0 license header.
set -o errexit -o nounset -o pipefail
cd "$(git rev-parse --show-toplevel)"

fail=0
while IFS= read -r f; do
  if ! head -20 "${f}" | grep -q 'Licensed under the Apache License, Version 2.0'; then
    echo "missing license header: ${f}" >&2
    fail=1
  fi
done < <(git ls-files '*.go' '*.sh' '*.py' '*.js' '*.css' '*.html' '*.tmpl' '*.yaml' '*.yml' \
  'Makefile' '*Dockerfile' '*.dockerignore' ':!:go.sum')
exit "${fail}"

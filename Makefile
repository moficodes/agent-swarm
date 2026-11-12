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

GO ?= go
BINDIR := bin
LINT := $(GO) tool -modfile=hack/tools/go.mod golangci-lint

.PHONY: all
all: verify test build

.PHONY: test
test:
	$(GO) test ./...
	bash hack/test-deploy.sh

.PHONY: fmt
fmt:
	gofmt -w $$(git ls-files '*.go')

.PHONY: verify
verify:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(LINT) run ./...
	$(GO) mod tidy -diff
	cd hack/tools && $(GO) mod tidy -diff
	bash hack/verify-boilerplate.sh
	bash hack/verify-manifests.sh
	shellcheck -x $$(git ls-files 'hack/*.sh')

.PHONY: build
build:
	$(GO) build -o $(BINDIR)/ ./cmd/...
	$(GO) build -o $(BINDIR)/kubectl-ate go.ate.dev/substrate/cmd/kubectl-ate
	docker buildx build --platform=linux/amd64 -f build/gateway.Dockerfile -t agent-swarm/conf-gateway:dev .

# Local mode: two actor slots on this machine and mock model replies.
.PHONY: run-local
run-local:
	@trap 'kill 0' EXIT; \
	$(GO) run ./cmd/conf-localrouter --addr=127.0.0.1:18080 --slots=2 & \
	sleep 2; \
	LOCAL_MODE=true DEMO_MOCK_LLM=true PORT=18090 \
	ROUTER_URL=http://127.0.0.1:18080 STATUS_URL=http://127.0.0.1:18080 ROUTER_STATUS_DISCOVERY=static \
	$(GO) run ./cmd/conf-gateway

.PHONY: verify-local
verify-local:
	bash hack/verify.sh --mock-mode

.PHONY: deploy
deploy:
	bash hack/deploy.sh up $(DEPLOY_FLAGS)

.PHONY: deploy-swarm
deploy-swarm:
	bash hack/deploy.sh up --profile=swarm $(DEPLOY_FLAGS)

.PHONY: deploy-megaswarm
deploy-megaswarm:
	bash hack/deploy.sh up --profile=megaswarm $(DEPLOY_FLAGS)

.PHONY: undeploy
undeploy:
	bash hack/deploy.sh down $(DEPLOY_FLAGS)

.PHONY: undeploy-megaswarm
undeploy-megaswarm:
	bash hack/deploy.sh down --profile=megaswarm $(DEPLOY_FLAGS)

.PHONY: e2e
e2e:
	bash hack/verify.sh

.PHONY: clean
clean:
	rm -rf $(BINDIR)

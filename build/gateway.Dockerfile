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

# Build context: repository root.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -o /out/kubectl-ate ./cmd/kubectl-ate \
 && go build -trimpath -o /out/conf-gateway ./demos/conference/cmd/conf-gateway

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/kubectl-ate /usr/local/bin/kubectl-ate
COPY --from=build /out/conf-gateway /usr/local/bin/conf-gateway
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/conf-gateway"]

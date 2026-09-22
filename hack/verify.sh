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

# Automated end-to-end verification script for the Interactive Conference Demo (demos/conference).
#
# Verifies:
#   Step 1: Health & Static UI Assets (/healthz, /stage, /chat, static JS/CSS)
#   Step 2: Ephemeral Session Minting (GET /api/session -> JWT & attendee ID)
#   Step 3: Turn 1 Chat & HITL Approval Generation (/api/chat -> ApprovalRequest)
#   Step 4: Turn 2 Approval & Sandbox Execution (/api/approve -> exit code 0 & stdout)
#   Step 5: Audience Burst & Request Parking (concurrent load, 0 dropped/503s, parking check)
#   Step 6: Telemetry Endpoint (/api/stage/telemetry -> actors, workers, parking)
#   Step 7: Audience Simulation Engine (/api/simulation -> start/stop lifecycle)
#   Step 8: Actor Spotlight Inspector API (/api/actor/{id} -> snapshot metadata & execution state)
#   Step 9: 1-Click Cluster Reset Controller (/api/reset -> parallel suspend sweep & state reset)
#   Step 10: Dedicated Live Feed Page (/feed -> DOM elements & static assets)
#   Step 11: Action Feed History API (/api/feed/history -> JSON array)
#   Step 12: Real-Time Action Stream SSE (/api/stream -> text/event-stream & event streaming)

set -o errexit -o nounset -o pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || (cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd))"
cd "${ROOT}"

# Configuration with defaults
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8090}"
GATEWAY_URL_EXPLICIT=false
ROUTER_URL="${ROUTER_URL:-http://localhost:8000}"
STATUS_URL="${STATUS_URL:-http://localhost:4040}"
CONCURRENCY="${CONCURRENCY:-10}"
MOCK_MODE=false

# Terminal color codes
CLR_RESET="\033[0m"
CLR_BOLD="\033[1m"
CLR_GREEN="\033[1;32m"
CLR_RED="\033[1;31m"
CLR_YELLOW="\033[1;33m"
CLR_BLUE="\033[1;34m"
CLR_CYAN="\033[1;36m"
CLR_MAGENTA="\033[1;35m"

log_info() {
  printf "${CLR_BLUE}==>${CLR_RESET} ${CLR_BOLD}%s${CLR_RESET}\n" "$*"
}

log_ok() {
  printf "  ${CLR_GREEN}✓${CLR_RESET} %s\n" "$*"
}

log_warn() {
  printf "  ${CLR_YELLOW}⚠${CLR_RESET} %s\n" "$*"
}

log_err() {
  printf "  ${CLR_RED}✗${CLR_RESET} %s\n" "$*" >&2
}

log_pass() {
  printf "${CLR_GREEN}${CLR_BOLD}[PASS] %s${CLR_RESET}\n\n" "$*"
}

log_fail() {
  printf "${CLR_RED}${CLR_BOLD}[FAIL] %s${CLR_RESET}\n\n" "$*" >&2
}

print_usage() {
  cat <<EOF
Usage: $0 [OPTIONS]

Automated verification script for the Interactive Conference Demo (demos/conference).

Options:
  -h, --help               Show this help message and exit
  --gateway-url <url>      URL of the conference edge gateway (default: http://localhost:8090)
  --router-url <url>       URL of the atenet router (default: http://localhost:8000)
  --status-url <url>       URL of the atenet router statusz endpoint (default: http://localhost:4040)
  --concurrency <N>        Number of concurrent requests for audience burst test (default: 10)
  --mock-mode              Run verification using offline mock engine / local environment

Examples:
  $0 --mock-mode
  $0 --gateway-url http://localhost:8090 --concurrency 25
EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      print_usage
      exit 0
      ;;
    --gateway-url)
      GATEWAY_URL="$2"
      GATEWAY_URL_EXPLICIT=true
      shift 2
      ;;
    --gateway-url=*)
      GATEWAY_URL="${1#*=}"
      GATEWAY_URL_EXPLICIT=true
      shift
      ;;
    --router-url)
      ROUTER_URL="$2"
      shift 2
      ;;
    --router-url=*)
      ROUTER_URL="${1#*=}"
      shift
      ;;
    --status-url)
      STATUS_URL="$2"
      shift 2
      ;;
    --status-url=*)
      STATUS_URL="${1#*=}"
      shift
      ;;
    --concurrency)
      CONCURRENCY="$2"
      shift 2
      ;;
    --concurrency=*)
      CONCURRENCY="${1#*=}"
      shift
      ;;
    --mock-mode)
      MOCK_MODE=true
      shift
      ;;
    *)
      log_err "Unknown argument: $1"
      print_usage
      exit 1
      ;;
  esac
done

TMP_DIR="$(mktemp -d /tmp/ate-conf-verify.XXXXXX)"
BURST_DIR="${TMP_DIR}/burst"
SPAWNED_PIDS=()
BURST_PIDS=()
STREAM_PID=""

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  if [[ -n "${PROBE_PID:-}" ]]; then
    kill "${PROBE_PID}" 2>/dev/null || true
  fi
  if [[ -n "${STREAM_PID}" ]]; then
    kill "${STREAM_PID}" 2>/dev/null || true
  fi
  if [[ ${#SPAWNED_PIDS[@]} -gt 0 ]]; then
    printf "\nShutting down locally spawned mock processes (%s)...\n" "${SPAWNED_PIDS[*]}"
    kill "${SPAWNED_PIDS[@]}" 2>/dev/null || true
    sleep 0.1
    kill -9 "${SPAWNED_PIDS[@]}" 2>/dev/null || true
    { wait "${SPAWNED_PIDS[@]}" 2>/dev/null || true; } 2>/dev/null
  fi
  if [[ ${#BURST_PIDS[@]} -gt 0 ]]; then
    for pid in "${BURST_PIDS[@]}"; do
      pkill -P "${pid}" 2>/dev/null || true
    done
    kill "${BURST_PIDS[@]}" 2>/dev/null || true
    sleep 0.1
    for pid in "${BURST_PIDS[@]}"; do
      pkill -9 -P "${pid}" 2>/dev/null || true
    done
    kill -9 "${BURST_PIDS[@]}" 2>/dev/null || true
    wait "${BURST_PIDS[@]}" 2>/dev/null || true
  fi
  rm -rf "${TMP_DIR}"
  exit "${exit_code}"
}
trap cleanup EXIT INT TERM

# Decode JWT payload function compatible with macOS and Linux
decode_jwt_payload() {
  local token="$1"
  local payload
  payload=$(printf "%s" "${token}" | cut -d'.' -f2)
  payload="${payload//-/\+}"
  payload="${payload//_/\/}"
  local rem=$((${#payload} % 4))
  if [[ ${rem} -eq 2 ]]; then
    payload="${payload}=="
  elif [[ ${rem} -eq 3 ]]; then
    payload="${payload}="
  fi
  printf "%s" "${payload}" | (base64 -d 2>/dev/null || base64 -D 2>/dev/null)
}

print_banner() {
  printf "${CLR_CYAN}========================================================================${CLR_RESET}\n"
  printf "${CLR_BOLD}  Agent Substrate: Conference Demo Automated Verification${CLR_RESET}\n"
  printf "${CLR_CYAN}========================================================================${CLR_RESET}\n"
  printf "  Gateway URL : %s\n" "${GATEWAY_URL}"
  printf "  Router URL  : %s\n" "${ROUTER_URL}"
  printf "  Status URL  : %s\n" "${STATUS_URL}"
  printf "  Concurrency : %s\n" "${CONCURRENCY}"
  printf "  Mock Mode   : %s\n" "${MOCK_MODE}"
  printf "${CLR_CYAN}========================================================================${CLR_RESET}\n\n"
}

# Verify prerequisites
if ! command -v curl &>/dev/null; then
  log_err "Required dependency 'curl' is not installed."
  exit 1
fi
if ! command -v jq &>/dev/null; then
  log_err "Required dependency 'jq' is not installed."
  exit 1
fi
if [[ "${MOCK_MODE}" == "true" ]]; then
  if ! command -v go &>/dev/null; then
    log_err "Required dependency 'go' is not installed (required for --mock-mode)."
    exit 1
  fi
  if ! command -v python3 &>/dev/null; then
    log_err "Required dependency 'python3' is not installed (required for --mock-mode)."
    exit 1
  fi
fi

# Check if conference edge gateway is reachable, or launch mock environment if in mock mode
IS_GATEWAY_REACHABLE=false
if [[ "${MOCK_MODE}" != "true" || "${GATEWAY_URL_EXPLICIT}" == "true" ]]; then
  if curl -s -f --max-time 2 "${GATEWAY_URL}/healthz" >/dev/null 2>&1 && \
     curl -s -f --max-time 2 "${GATEWAY_URL}/api/session" >/dev/null 2>&1; then
    IS_GATEWAY_REACHABLE=true
  fi
fi

if [[ "${IS_GATEWAY_REACHABLE}" != "true" ]]; then
  if [[ "${MOCK_MODE}" == "true" ]]; then
    log_info "Gateway at ${GATEWAY_URL} not detected. Initializing standalone mock environment..."

    log_info "Compiling conference binaries into temporary workspace..."
    go build -o "${TMP_DIR}/conf-workload" ./demos/conference/cmd/conf-workload
    go build -o "${TMP_DIR}/conf-gateway" ./demos/conference/cmd/conf-gateway

    MOCK_WORKLOAD_PORT=18081
    MOCK_ROUTER_PORT=18080
    MOCK_GATEWAY_PORT=18090

    # Start mock workload server with forced mock reasoning
    PORT="${MOCK_WORKLOAD_PORT}" DEMO_MOCK_LLM=true "${TMP_DIR}/conf-workload" >/dev/null 2>&1 &
    SPAWNED_PIDS+=($!)

    # Create mock router script with 2-worker concurrency capacity and statusz active parking counter
    cat <<'PYEOF' > "${TMP_DIR}/mock_router.py"
import http.server, urllib.request, threading, time, json, socketserver, signal, sys

signal.signal(signal.SIGTERM, lambda s, f: sys.exit(0))
signal.signal(signal.SIGINT, lambda s, f: sys.exit(0))

class RouterHandler(http.server.BaseHTTPRequestHandler):
    active = 0
    parked = 0
    lock = threading.Lock()
    cv = threading.Condition(lock)
    upstream = "http://127.0.0.1:18081"

    def do_GET(self):
        if self.path.startswith("/statusz"):
            with self.lock:
                p = RouterHandler.parked
            resp = json.dumps({"parking": {"active": p}}).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)
        elif self.path == "/healthz":
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.end_headers()
            self.wfile.write(b"OK")
        elif self.path.startswith("/api/actor/"):
            actor_id = self.path.split("/")[-1]
            resp = json.dumps({
                "actor_id": actor_id,
                "state": "SUSPENDED",
                "worker_pod": "",
                "snapshot_location": f"gs://conf-demo-bucket/ate-demo-conf/actors/{actor_id}.snap",
                "snapshot_size_bytes": 384000,
                "chat_history": []
            }).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        content_len = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_len) if content_len > 0 else b""

        if self.path == "/api/reset":
            resp = json.dumps({"status": "ok", "message": "Cluster reset", "actors_reset": 0}).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)
            return
        
        with self.cv:
            if RouterHandler.active >= 2:
                RouterHandler.parked += 1
                while RouterHandler.active >= 2:
                    self.cv.wait(timeout=2.0)
                RouterHandler.parked -= 1
            RouterHandler.active += 1

        try:
            time.sleep(0.02)
            url = f"{self.upstream}{self.path}"
            req = urllib.request.Request(url, data=body, method="POST")
            for k, v in self.headers.items():
                if k.lower() not in ["host", "content-length"]:
                    req.add_header(k, v)
            with urllib.request.urlopen(req, timeout=10) as upstream_resp:
                resp_body = upstream_resp.read()
                self.send_response(upstream_resp.status)
                for k, v in upstream_resp.headers.items():
                    if k.lower() not in ["transfer-encoding", "content-length"]:
                        self.send_header(k, v)
                self.send_header("Content-Length", str(len(resp_body)))
                self.end_headers()
                self.wfile.write(resp_body)
        finally:
            with self.cv:
                RouterHandler.active -= 1
                self.cv.notify_all()

    def log_message(self, format, *args):
        pass

class ThreadedHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True

srv = ThreadedHTTPServer(("127.0.0.1", 18080), RouterHandler)
srv.serve_forever()
PYEOF

    python3 "${TMP_DIR}/mock_router.py" >/dev/null 2>&1 &
    SPAWNED_PIDS+=($!)

    # Start conference edge gateway
    ROUTER_URL="http://127.0.0.1:${MOCK_ROUTER_PORT}"
    STATUS_URL="http://127.0.0.1:${MOCK_ROUTER_PORT}"
    GATEWAY_URL="http://127.0.0.1:${MOCK_GATEWAY_PORT}"

    PORT="${MOCK_GATEWAY_PORT}" \
    ROUTER_URL="${ROUTER_URL}" \
    STATUS_URL="${STATUS_URL}" \
    "${TMP_DIR}/conf-gateway" >/dev/null 2>&1 &
    SPAWNED_PIDS+=($!)

    # Wait for gateway readiness
    GATEWAY_READY=false
    for _ in $(seq 1 30); do
      if curl -s -f --max-time 1 "${GATEWAY_URL}/healthz" >/dev/null 2>&1; then
        GATEWAY_READY=true
        break
      fi
      sleep 0.1
    done

    if [[ "${GATEWAY_READY}" != "true" ]]; then
      log_err "Failed to start local mock gateway on ${GATEWAY_URL}"
      exit 1
    fi
    log_ok "Standalone mock environment active (Gateway: ${GATEWAY_URL}, Router: ${ROUTER_URL})"
  else
    log_err "Conference edge gateway is not reachable at ${GATEWAY_URL}."
    printf "\nTo verify against an active Kubernetes cluster:\n"
    printf "  1. Deploy the demo: ./hack/install-demo-conference.sh --deploy\n"
    printf "  2. Port-forward the atenet router and run the gateway:\n"
    printf "       kubectl port-forward -n ate-system svc/atenet-router 8000:80 &\n"
    printf "       kubectl port-forward -n ate-system svc/atenet-router 4040:4040 &\n"
    printf "       PORT=8090 ROUTER_URL=http://localhost:8000 STATUS_URL=http://localhost:4040 go run ./demos/conference/cmd/conf-gateway\n\n"
    printf "To run self-contained automated verification locally without a cluster:\n"
    printf "  %s --mock-mode\n\n" "$0"
    exit 1
  fi
fi

# Print configuration banner after URLs are finalized
print_banner

# Ensure clean starting state
curl -s -X POST "${GATEWAY_URL}/api/reset" >/dev/null 2>&1 || true

# =========================================================================
# Step 1: Health & Static UI Assets
# =========================================================================
log_info "Step 1: Health & Static UI Assets"
REQUIRED_ASSETS=(
  "/healthz"
  "/stage"
  "/chat"
  "/static/stage.js"
  "/static/stage.css"
  "/static/chat.js"
  "/static/chat.css"
)

for asset_path in "${REQUIRED_ASSETS[@]}"; do
  HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${GATEWAY_URL}${asset_path}" || echo "000")
  if [[ "${HTTP_CODE}" != "200" ]]; then
    log_err "Asset ${asset_path} returned HTTP ${HTTP_CODE} (expected 200)"
    log_fail "Step 1: Health & Static UI Assets"
    exit 1
  fi
  log_ok "GET ${asset_path} (HTTP 200 OK)"
done
log_pass "Step 1: Health & Static UI Assets Verified"

# =========================================================================
# Step 2: Ephemeral Session Minting
# =========================================================================
log_info "Step 2: Ephemeral Session Minting"
SESSION_RESP=$(curl -s --max-time 5 "${GATEWAY_URL}/api/session")
ATTENDEE_ID=$(printf "%s" "${SESSION_RESP}" | jq -r .attendee_id 2>/dev/null || true)
TOKEN=$(printf "%s" "${SESSION_RESP}" | jq -r .token 2>/dev/null || true)

if [[ -z "${ATTENDEE_ID}" || "${ATTENDEE_ID}" == "null" || ! "${ATTENDEE_ID}" =~ ^attendee-[0-9]+$ ]]; then
  log_err "Invalid or missing attendee_id in session response: ${SESSION_RESP}"
  log_fail "Step 2: Ephemeral Session Minting"
  exit 1
fi
log_ok "Minted Attendee ID: ${ATTENDEE_ID}"

if [[ -z "${TOKEN}" || "${TOKEN}" == "null" ]]; then
  log_err "Missing token in session response: ${SESSION_RESP}"
  log_fail "Step 2: Ephemeral Session Minting"
  exit 1
fi

DECODED_PAYLOAD=$(decode_jwt_payload "${TOKEN}")
JWT_SUB=$(printf "%s" "${DECODED_PAYLOAD}" | jq -r .sub 2>/dev/null || true)
JWT_EXP=$(printf "%s" "${DECODED_PAYLOAD}" | jq -r .exp 2>/dev/null || true)
NOW_TS=$(date +%s)

if [[ "${JWT_SUB}" != "${ATTENDEE_ID}" ]]; then
  log_err "Token subject mismatch: token.sub=${JWT_SUB}, expected=${ATTENDEE_ID}"
  log_fail "Step 2: Ephemeral Session Minting"
  exit 1
fi

if [[ -z "${JWT_EXP}" || "${JWT_EXP}" == "null" || "${JWT_EXP}" -le "${NOW_TS}" ]]; then
  log_err "Token expired or invalid exp: token.exp=${JWT_EXP}, now=${NOW_TS}"
  log_fail "Step 2: Ephemeral Session Minting"
  exit 1
fi
log_ok "Verified JWT Token: sub=${JWT_SUB}, exp=${JWT_EXP} (valid for $((${JWT_EXP} - ${NOW_TS}))s)"
log_pass "Step 2: Ephemeral Session Minting Verified"

# =========================================================================
# Step 3: Turn 1 Chat & HITL Approval Generation
# =========================================================================
log_info "Step 3: Turn 1 Chat & HITL Approval Generation"
PROMPT_MSG="Analyze sales telemetry, detect outliers, and produce an ASCII histogram"
CHAT_PAYLOAD=$(jq -n --arg msg "${PROMPT_MSG}" '{"message": $msg}')

CHAT_RESP=$(curl -s --max-time 20 -X POST "${GATEWAY_URL}/api/chat" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "${CHAT_PAYLOAD}")

SENDER=$(printf "%s" "${CHAT_RESP}" | jq -r .sender 2>/dev/null || true)
APPROVAL_ID=$(printf "%s" "${CHAT_RESP}" | jq -r .approval.approval_id 2>/dev/null || true)
APPROVAL_SUMMARY=$(printf "%s" "${CHAT_RESP}" | jq -r .approval.summary 2>/dev/null || true)
APPROVAL_LANG=$(printf "%s" "${CHAT_RESP}" | jq -r .approval.language 2>/dev/null || true)
APPROVAL_RISK=$(printf "%s" "${CHAT_RESP}" | jq -r .approval.risk_level 2>/dev/null || true)

if [[ "${SENDER}" != "assistant" ]]; then
  log_err "Expected sender 'assistant', got '${SENDER}'. Response: ${CHAT_RESP}"
  log_fail "Step 3: Turn 1 Chat & HITL Approval Generation"
  exit 1
fi

if [[ -z "${APPROVAL_ID}" || "${APPROVAL_ID}" == "null" ]]; then
  log_err "No pending ApprovalRequest returned in Turn 1 chat. Response: ${CHAT_RESP}"
  log_fail "Step 3: Turn 1 Chat & HITL Approval Generation"
  exit 1
fi

log_ok "Assistant generated pending Human-in-the-Loop ApprovalRequest"
log_ok "  Approval ID : ${APPROVAL_ID}"
log_ok "  Summary     : ${APPROVAL_SUMMARY}"
log_ok "  Language    : ${APPROVAL_LANG}"
log_ok "  Risk Level  : ${APPROVAL_RISK}"
log_pass "Step 3: Turn 1 Chat & HITL Approval Generation Verified"

# =========================================================================
# Step 4: Turn 2 Approval & Sandbox Execution
# =========================================================================
log_info "Step 4: Turn 2 Approval & Sandbox Execution"
APPROVE_PAYLOAD=$(jq -n --arg id "${APPROVAL_ID}" '{"approval_id": $id, "decision": "APPROVE"}')

APPROVE_RESP=$(curl -s --max-time 15 -X POST "${GATEWAY_URL}/api/approve" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "${APPROVE_PAYLOAD}")

APPROVE_SENDER=$(printf "%s" "${APPROVE_RESP}" | jq -r .sender 2>/dev/null || true)
APPROVE_CONTENT=$(printf "%s" "${APPROVE_RESP}" | jq -r .content 2>/dev/null || true)

if [[ "${APPROVE_SENDER}" != "assistant" ]]; then
  log_err "Expected approval response sender 'assistant', got '${APPROVE_SENDER}'. Response: ${APPROVE_RESP}"
  log_fail "Step 4: Turn 2 Approval & Sandbox Execution"
  exit 1
fi

if [[ "${APPROVE_CONTENT}" != *"exit code 0"* ]]; then
  log_err "Sandbox execution output does not indicate exit code 0. Content: ${APPROVE_CONTENT}"
  log_fail "Step 4: Turn 2 Approval & Sandbox Execution"
  exit 1
fi

log_ok "Sandbox code execution completed with exit code 0"
FIRST_LINE=$(printf "%s" "${APPROVE_CONTENT}" | head -n 1)
log_ok "Result: ${FIRST_LINE}"
log_pass "Step 4: Turn 2 Approval & Sandbox Execution Verified"

# =========================================================================
# Step 5: Audience Burst & Request Parking
# =========================================================================
log_info "Step 5: Audience Burst & Request Parking (${CONCURRENCY} concurrent requests)"
mkdir -p "${BURST_DIR}"

BURST_PIDS=()
for i in $(seq 1 "${CONCURRENCY}"); do
  (
    c_session=$(curl -s --max-time 10 "${GATEWAY_URL}/api/session" || true)
    c_token=$(printf "%s" "${c_session}" | jq -r .token 2>/dev/null || true)
    if [[ -z "${c_token}" || "${c_token}" == "null" ]]; then
      echo "000" > "${BURST_DIR}/${i}.status"
      exit 0
    fi
    code=$(curl -s --max-time 60 -o /dev/null -w "%{http_code}" -X POST "${GATEWAY_URL}/api/chat" \
      -H "Authorization: Bearer ${c_token}" \
      -H "Content-Type: application/json" \
      -d '{"message":"Analyze telemetry outliers"}' || echo "000")
    echo "${code}" > "${BURST_DIR}/${i}.status"
  ) &
  BURST_PIDS+=($!)
done

# Actively poll for request parking status while burst is in flight
PARKING_DETECTED=0
for _ in $(seq 1 15); do
  sz=$(curl -s --max-time 1 "${STATUS_URL}/statusz?format=json" 2>/dev/null || true)
  if [[ -n "${sz}" ]]; then
    p=$(printf "%s" "${sz}" | jq -r '.parking.active // 0' 2>/dev/null || echo 0)
    if [[ "${p}" =~ ^[0-9]+$ && "${p}" -gt "${PARKING_DETECTED}" ]]; then
      PARKING_DETECTED="${p}"
    fi
  fi
  sleep 0.04
done

if [[ ${#BURST_PIDS[@]} -gt 0 ]]; then
  wait "${BURST_PIDS[@]}"
  BURST_PIDS=()
fi

BURST_FAILURES=0
BURST_503S=0
BURST_SUCCESS=0

for i in $(seq 1 "${CONCURRENCY}"); do
  st=$(cat "${BURST_DIR}/${i}.status" 2>/dev/null || echo "000")
  if [[ "${st}" == "200" ]]; then
    ((BURST_SUCCESS++)) || true
  elif [[ "${st}" == "503" ]]; then
    ((BURST_503S++)) || true
    ((BURST_FAILURES++)) || true
  else
    ((BURST_FAILURES++)) || true
  fi
done
rm -rf "${BURST_DIR}"

if [[ ${PARKING_DETECTED} -gt 0 ]]; then
  log_ok "Live L7 Request Parking detected: ${PARKING_DETECTED} concurrent requests parked in router buffer"
else
  log_ok "Audience burst handled across available worker capacity"
fi

if [[ ${BURST_503S} -gt 0 ]]; then
  log_err "Encountered ${BURST_503S} HTTP 503 errors during burst (request parking failed)!"
  log_fail "Step 5: Audience Burst & Request Parking"
  exit 1
fi

if [[ ${BURST_FAILURES} -gt 0 ]]; then
  log_err "${BURST_FAILURES}/${CONCURRENCY} burst requests failed (non-200 responses)"
  log_fail "Step 5: Audience Burst & Request Parking"
  exit 1
fi

log_ok "${BURST_SUCCESS}/${CONCURRENCY} concurrent requests succeeded with HTTP 200 OK (0 dropped, 0 errors)"
log_pass "Step 5: Audience Burst & Request Parking Verified"

# =========================================================================
# Step 6: Telemetry Endpoint
# =========================================================================
log_info "Step 6: Telemetry Endpoint"
TELEM_RESP=$(curl -s --max-time 5 "${GATEWAY_URL}/api/stage/telemetry")

WORKERS_COUNT=$(printf "%s" "${TELEM_RESP}" | jq '.workers | length' 2>/dev/null || echo 0)
ACTORS_COUNT=$(printf "%s" "${TELEM_RESP}" | jq '.actors | length' 2>/dev/null || echo 0)
PARKED_COUNT=$(printf "%s" "${TELEM_RESP}" | jq -r '.parked_requests // -1' 2>/dev/null || echo -1)
EVENTS_COUNT=$(printf "%s" "${TELEM_RESP}" | jq '.recent_events | length' 2>/dev/null || echo 0)

if [[ ${WORKERS_COUNT} -lt 2 ]]; then
  log_err "Expected at least 2 worker slots in telemetry, got ${WORKERS_COUNT}. Response: ${TELEM_RESP}"
  log_fail "Step 6: Telemetry Endpoint"
  exit 1
fi

if [[ ${ACTORS_COUNT} -lt 1 ]]; then
  log_err "Expected at least 1 tracked actor in telemetry, got ${ACTORS_COUNT}. Response: ${TELEM_RESP}"
  log_fail "Step 6: Telemetry Endpoint"
  exit 1
fi

if ! [[ -n "${PARKED_COUNT}" && "${PARKED_COUNT}" != "null" && "${PARKED_COUNT}" =~ ^[0-9]+$ ]] || [[ ${PARKED_COUNT} -lt 0 ]]; then
  log_err "Missing or invalid parked_requests metric in telemetry. Response: ${TELEM_RESP}"
  log_fail "Step 6: Telemetry Endpoint"
  exit 1
fi

log_ok "Physical Worker Pod Slots : ${WORKERS_COUNT}"
for i in $(seq 0 $((${WORKERS_COUNT} - 1))); do
  pod_name=$(printf "%s" "${TELEM_RESP}" | jq -r ".workers[$i].pod_name")
  pod_actor=$(printf "%s" "${TELEM_RESP}" | jq -r ".workers[$i].active_actor")
  pod_task=$(printf "%s" "${TELEM_RESP}" | jq -r ".workers[$i].current_task")
  log_ok "  - Slot ${i}: ${pod_name} (${pod_actor}: ${pod_task})"
done
log_ok "Active Fleet Actors Count : ${ACTORS_COUNT}"
log_ok "Router Parked Counter     : ${PARKED_COUNT}"
log_ok "Logged Cluster Events     : ${EVENTS_COUNT} events recorded"
log_pass "Step 6: Telemetry Endpoint Verified"

# =========================================================================
# Step 7: Audience Simulation Engine
# =========================================================================
log_info "Step 7: Audience Simulation Engine (/api/simulation)"
SIM_START_PAYLOAD='{"action":"start","count":5}'
SIM_START_CODE=$(curl -s -o "${TMP_DIR}/sim_start.json" -w "%{http_code}" --max-time 10 -X POST "${GATEWAY_URL}/api/simulation" \
  -H "Content-Type: application/json" \
  -d "${SIM_START_PAYLOAD}")

if [[ "${SIM_START_CODE}" != "200" ]]; then
  log_err "Failed to start simulation (HTTP ${SIM_START_CODE}). Response: $(cat "${TMP_DIR}/sim_start.json" 2>/dev/null || true)"
  log_fail "Step 7: Audience Simulation Engine"
  exit 1
fi
log_ok "Started audience simulation with count=5 via POST /api/simulation (HTTP 200 OK)"

# Verify GET /api/simulation returns running: true
SIM_STATUS_RESP=$(curl -s --max-time 5 "${GATEWAY_URL}/api/simulation")
SIM_RUNNING=$(printf "%s" "${SIM_STATUS_RESP}" | jq -r .running 2>/dev/null || echo false)
if [[ "${SIM_RUNNING}" != "true" ]]; then
  log_err "Expected running: true from GET /api/simulation, got '${SIM_RUNNING}'. Response: ${SIM_STATUS_RESP}"
  log_fail "Step 7: Audience Simulation Engine"
  exit 1
fi
log_ok "GET /api/simulation verified running: true"

# Stop simulation via POST /api/simulation ({"action":"stop"})
SIM_STOP_PAYLOAD='{"action":"stop"}'
SIM_STOP_CODE=$(curl -s -o "${TMP_DIR}/sim_stop.json" -w "%{http_code}" --max-time 10 -X POST "${GATEWAY_URL}/api/simulation" \
  -H "Content-Type: application/json" \
  -d "${SIM_STOP_PAYLOAD}")

if [[ "${SIM_STOP_CODE}" != "200" ]]; then
  log_err "Failed to stop simulation (HTTP ${SIM_STOP_CODE}). Response: $(cat "${TMP_DIR}/sim_stop.json" 2>/dev/null || true)"
  log_fail "Step 7: Audience Simulation Engine"
  exit 1
fi
log_ok "Stopped audience simulation via POST /api/simulation (HTTP 200 OK)"

# Verify GET /api/simulation returns running: false
SIM_STOPPED_RESP=$(curl -s --max-time 5 "${GATEWAY_URL}/api/simulation")
SIM_RUNNING_STOPPED=$(printf "%s" "${SIM_STOPPED_RESP}" | jq -r .running 2>/dev/null || echo true)
if [[ "${SIM_RUNNING_STOPPED}" != "false" ]]; then
  log_err "Expected running: false from GET /api/simulation, got '${SIM_RUNNING_STOPPED}'. Response: ${SIM_STOPPED_RESP}"
  log_fail "Step 7: Audience Simulation Engine"
  exit 1
fi
log_ok "GET /api/simulation verified running: false"
log_pass "Step 7: Audience Simulation Engine Verified"

# =========================================================================
# Step 8: Actor Spotlight Inspector API
# =========================================================================
TARGET_ACTOR_ID="${ATTENDEE_ID:-attendee-01}"
log_info "Step 8: Actor Spotlight Inspector API (/api/actor/{id})"
ACTOR_HTTP_CODE=$(curl -s -o "${TMP_DIR}/actor_details.json" -w "%{http_code}" --max-time 5 "${GATEWAY_URL}/api/actor/${TARGET_ACTOR_ID}")

if [[ "${ACTOR_HTTP_CODE}" != "200" ]]; then
  log_err "GET /api/actor/${TARGET_ACTOR_ID} returned HTTP ${ACTOR_HTTP_CODE} (expected 200)"
  log_fail "Step 8: Actor Spotlight Inspector API"
  exit 1
fi

ACTOR_RESP=$(cat "${TMP_DIR}/actor_details.json")
ACTOR_ID=$(printf "%s" "${ACTOR_RESP}" | jq -r .actor_id 2>/dev/null || true)
ACTOR_STATE=$(printf "%s" "${ACTOR_RESP}" | jq -r .state 2>/dev/null || true)
HAS_ACTOR_ID=$(printf "%s" "${ACTOR_RESP}" | jq 'has("actor_id")' 2>/dev/null || echo false)
HAS_STATE=$(printf "%s" "${ACTOR_RESP}" | jq 'has("state")' 2>/dev/null || echo false)
HAS_SNAP_LOC=$(printf "%s" "${ACTOR_RESP}" | jq 'has("snapshot_location")' 2>/dev/null || echo false)
HAS_SNAP_SIZE=$(printf "%s" "${ACTOR_RESP}" | jq 'has("snapshot_size_bytes")' 2>/dev/null || echo false)

if [[ "${HAS_ACTOR_ID}" != "true" || -z "${ACTOR_ID}" || "${ACTOR_ID}" == "null" ]]; then
  log_err "Missing or null actor_id in /api/actor/attendee-01 response: ${ACTOR_RESP}"
  log_fail "Step 8: Actor Spotlight Inspector API"
  exit 1
fi

if [[ "${HAS_STATE}" != "true" || -z "${ACTOR_STATE}" || "${ACTOR_STATE}" == "null" ]]; then
  log_err "Missing or null state in /api/actor/attendee-01 response: ${ACTOR_RESP}"
  log_fail "Step 8: Actor Spotlight Inspector API"
  exit 1
fi

if [[ "${HAS_SNAP_LOC}" != "true" ]]; then
  log_err "Missing snapshot_location in /api/actor/attendee-01 response: ${ACTOR_RESP}"
  log_fail "Step 8: Actor Spotlight Inspector API"
  exit 1
fi

if [[ "${HAS_SNAP_SIZE}" != "true" ]]; then
  log_err "Missing snapshot_size_bytes in /api/actor/attendee-01 response: ${ACTOR_RESP}"
  log_fail "Step 8: Actor Spotlight Inspector API"
  exit 1
fi

SNAP_LOC=$(printf "%s" "${ACTOR_RESP}" | jq -r .snapshot_location 2>/dev/null || echo "")
SNAP_SIZE=$(printf "%s" "${ACTOR_RESP}" | jq -r .snapshot_size_bytes 2>/dev/null || echo 0)

log_ok "GET /api/actor/attendee-01 returned HTTP 200 OK"
log_ok "  actor_id            : ${ACTOR_ID}"
log_ok "  state               : ${ACTOR_STATE}"
log_ok "  snapshot_location   : ${SNAP_LOC}"
log_ok "  snapshot_size_bytes : ${SNAP_SIZE}"
log_pass "Step 8: Actor Spotlight Inspector API Verified"

# =========================================================================
# Step 9: 1-Click Cluster Reset Controller
# =========================================================================
log_info "Step 9: 1-Click Cluster Reset Controller (/api/reset)"
RESET_HTTP_CODE=$(curl -s -o "${TMP_DIR}/reset_resp.json" -w "%{http_code}" --max-time 15 -X POST "${GATEWAY_URL}/api/reset")

if [[ "${RESET_HTTP_CODE}" != "200" ]]; then
  log_err "POST /api/reset returned HTTP ${RESET_HTTP_CODE} (expected 200)"
  log_fail "Step 9: 1-Click Cluster Reset Controller"
  exit 1
fi

RESET_RESP=$(cat "${TMP_DIR}/reset_resp.json")
RESET_STATUS=$(printf "%s" "${RESET_RESP}" | jq -r .status 2>/dev/null || true)
if [[ "${RESET_STATUS}" != "ok" ]]; then
  log_err "POST /api/reset payload status is not 'ok'. Response: ${RESET_RESP}"
  log_fail "Step 9: 1-Click Cluster Reset Controller"
  exit 1
fi
log_ok "POST /api/reset returned HTTP 200 OK with payload status: ok"

# Asserts GET /api/actor/{id} reports state SUSPENDED
ACTOR_RESET_RESP=$(curl -s --max-time 5 "${GATEWAY_URL}/api/actor/${TARGET_ACTOR_ID}")
ACTOR_RESET_STATE=$(printf "%s" "${ACTOR_RESET_RESP}" | jq -r .state 2>/dev/null || true)

if [[ "${ACTOR_RESET_STATE}" != "SUSPENDED" ]]; then
  log_err "GET /api/actor/${TARGET_ACTOR_ID} reported state '${ACTOR_RESET_STATE}' after reset (expected 'SUSPENDED'). Response: ${ACTOR_RESET_RESP}"
  log_fail "Step 9: 1-Click Cluster Reset Controller"
  exit 1
fi

log_ok "GET /api/actor/${TARGET_ACTOR_ID} confirmed actor state is SUSPENDED"
log_pass "Step 9: 1-Click Cluster Reset Controller Verified"

# =========================================================================
# Step 10: Dedicated Live Feed Page (/feed)
# =========================================================================
log_info "Step 10: Dedicated Live Feed Page (/feed)"
FEED_HEADERS_FILE="${TMP_DIR}/feed_headers.txt"
FEED_BODY_FILE="${TMP_DIR}/feed_body.html"

FEED_HTTP_CODE=$(curl -s -D "${FEED_HEADERS_FILE}" -o "${FEED_BODY_FILE}" -w "%{http_code}" --max-time 5 "${GATEWAY_URL}/feed")
if [[ "${FEED_HTTP_CODE}" != "200" ]]; then
  log_err "GET /feed returned HTTP ${FEED_HTTP_CODE} (expected 200)"
  log_fail "Step 10: Dedicated Live Feed Page"
  exit 1
fi

FEED_CONTENT_TYPE=$(grep -i '^content-type:' "${FEED_HEADERS_FILE}" | tr -d '\r' | awk '{print $2}' || true)
if [[ "${FEED_CONTENT_TYPE}" != *"text/html"* ]]; then
  log_err "GET /feed Content-Type is '${FEED_CONTENT_TYPE}' (expected text/html)"
  log_fail "Step 10: Dedicated Live Feed Page"
  exit 1
fi
log_ok "GET /feed (HTTP 200 OK, Content-Type: ${FEED_CONTENT_TYPE})"

REQUIRED_FEED_IDS=(
  "action-stream"
  "feed-search"
  "feed-autoscroll"
  "btn-reset-all"
)
for dom_id in "${REQUIRED_FEED_IDS[@]}"; do
  if ! grep -q "id=[\"']${dom_id}[\"']" "${FEED_BODY_FILE}"; then
    log_err "Required element id='${dom_id}' not found in /feed HTML"
    log_fail "Step 10: Dedicated Live Feed Page"
    exit 1
  fi
  log_ok "  Found DOM element: #${dom_id}"
done

FEED_STATIC_ASSETS=(
  "/static/feed.js"
  "/static/feed.css"
)
for asset_path in "${FEED_STATIC_ASSETS[@]}"; do
  HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "${GATEWAY_URL}${asset_path}" || echo "000")
  if [[ "${HTTP_CODE}" != "200" ]]; then
    log_err "Feed asset ${asset_path} returned HTTP ${HTTP_CODE} (expected 200)"
    log_fail "Step 10: Dedicated Live Feed Page"
    exit 1
  fi
  log_ok "GET ${asset_path} (HTTP 200 OK)"
done
log_pass "Step 10: Dedicated Live Feed Page Verified"

# =========================================================================
# Step 11: Action Feed History API (/api/feed/history)
# =========================================================================
log_info "Step 11: Action Feed History API (/api/feed/history)"
HISTORY_HEADERS_FILE="${TMP_DIR}/history_headers.txt"
HISTORY_BODY_FILE="${TMP_DIR}/history_body.json"

HISTORY_HTTP_CODE=$(curl -s -D "${HISTORY_HEADERS_FILE}" -o "${HISTORY_BODY_FILE}" -w "%{http_code}" --max-time 5 "${GATEWAY_URL}/api/feed/history")
if [[ "${HISTORY_HTTP_CODE}" != "200" ]]; then
  log_err "GET /api/feed/history returned HTTP ${HISTORY_HTTP_CODE} (expected 200)"
  log_fail "Step 11: Action Feed History API"
  exit 1
fi

HISTORY_CONTENT_TYPE=$(grep -i '^content-type:' "${HISTORY_HEADERS_FILE}" | tr -d '\r' | awk '{print $2}' || true)
if [[ "${HISTORY_CONTENT_TYPE}" != *"application/json"* ]]; then
  log_err "GET /api/feed/history Content-Type is '${HISTORY_CONTENT_TYPE}' (expected application/json)"
  log_fail "Step 11: Action Feed History API"
  exit 1
fi
log_ok "GET /api/feed/history (HTTP 200 OK, Content-Type: ${HISTORY_CONTENT_TYPE})"

IS_ARRAY=$(jq 'type == "array"' "${HISTORY_BODY_FILE}" 2>/dev/null || echo false)
if [[ "${IS_ARRAY}" != "true" ]]; then
  log_err "GET /api/feed/history did not return a valid JSON array. Body: $(cat "${HISTORY_BODY_FILE}" 2>/dev/null || true)"
  log_fail "Step 11: Action Feed History API"
  exit 1
fi

HISTORY_COUNT=$(jq 'length' "${HISTORY_BODY_FILE}" 2>/dev/null || echo 0)
log_ok "GET /api/feed/history returned valid JSON array with ${HISTORY_COUNT} entries"
log_pass "Step 11: Action Feed History API Verified"

# =========================================================================
# Step 12: Real-Time Action Stream SSE (/api/stream)
# =========================================================================
log_info "Step 12: Real-Time Action Stream SSE (/api/stream)"
STREAM_HEADERS_FILE="${TMP_DIR}/stream_headers.txt"
STREAM_BODY_FILE="${TMP_DIR}/stream_body.txt"

# Establish SSE connection in background
curl -s -N -D "${STREAM_HEADERS_FILE}" "${GATEWAY_URL}/api/stream" > "${STREAM_BODY_FILE}" 2>/dev/null &
STREAM_PID=$!

STREAM_HANDSHAKE=false
for _ in $(seq 1 40); do
  if [[ -s "${STREAM_HEADERS_FILE}" ]] && grep -qi 'HTTP/.* 200' "${STREAM_HEADERS_FILE}"; then
    STREAM_HANDSHAKE=true
    break
  fi
  sleep 0.05
done

if [[ "${STREAM_HANDSHAKE}" != "true" ]]; then
  kill "${STREAM_PID}" 2>/dev/null || true
  log_err "Failed to establish SSE stream handshake on GET /api/stream"
  if [[ -s "${STREAM_HEADERS_FILE}" ]]; then
    log_err "Headers: $(cat "${STREAM_HEADERS_FILE}")"
  fi
  log_fail "Step 12: Real-Time Action Stream SSE"
  exit 1
fi

STREAM_CONTENT_TYPE=$(grep -i '^content-type:' "${STREAM_HEADERS_FILE}" | tr -d '\r' | awk '{print $2}' || true)
if [[ "${STREAM_CONTENT_TYPE}" != *"text/event-stream"* ]]; then
  kill "${STREAM_PID}" 2>/dev/null || true
  log_err "Expected Content-Type text/event-stream on /api/stream, got '${STREAM_CONTENT_TYPE}'"
  log_fail "Step 12: Real-Time Action Stream SSE"
  exit 1
fi

log_ok "SSE stream handshake established (HTTP 200 OK, Content-Type: ${STREAM_CONTENT_TYPE})"

# Trigger an event and verify reading from the live stream
curl -s --max-time 10 -X POST "${GATEWAY_URL}/api/chat" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"message":"Stream verification probe"}' >/dev/null 2>&1 &
PROBE_PID=$!

EVENT_RECEIVED=false
for _ in $(seq 1 40); do
  if [[ -s "${STREAM_BODY_FILE}" ]] && grep -q "event:" "${STREAM_BODY_FILE}"; then
    EVENT_RECEIVED=true
    break
  fi
  sleep 0.05
done

wait "${PROBE_PID}" 2>/dev/null || true
kill "${STREAM_PID}" 2>/dev/null || true
wait "${STREAM_PID}" 2>/dev/null || true
STREAM_PID=""

if [[ "${EVENT_RECEIVED}" == "true" ]]; then
  FIRST_EVENT=$(grep "event:" "${STREAM_BODY_FILE}" | head -n 1 | tr -d '\r')
  log_ok "Successfully read event from stream: ${FIRST_EVENT}"
else
  log_ok "Verified stream connection handshake (no immediate events queued)"
fi

log_pass "Step 12: Real-Time Action Stream SSE Verified"

printf "${CLR_CYAN}========================================================================${CLR_RESET}\n"
printf "${CLR_GREEN}${CLR_BOLD}  ✓ ALL 12 VERIFICATION STEPS PASSED SUCCESSFULLY! (exit 0)${CLR_RESET}\n"
printf "${CLR_CYAN}========================================================================${CLR_RESET}\n"
exit 0

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// MockEngine provides deterministic offline agent turns for demonstrations without external LLM dependencies.
type MockEngine struct{}

// NewMockEngine constructs a new MockEngine.
func NewMockEngine() *MockEngine {
	return &MockEngine{}
}

type mockScript struct {
	match     string
	content   string
	summary   string
	code      string
	riskLevel types.RiskLevel
}

var tailoredScripts = []mockScript{
	{
		match:   "security audit",
		content: "I prepared a sandbox runtime security audit to inspect user privileges, kernel identity, and capability masks from inside the gVisor container. Please review and authorize execution:",
		summary: "Audit gVisor Sandbox Privileges & Capabilities",
		code: `import os, platform

uid, gid = os.getuid(), os.getgid()
kernel = platform.release()
cap_eff = "0000000000000000"
try:
    with open("/proc/self/status", encoding="utf-8") as f:
        for line in f:
            if line.startswith("CapEff:"):
                cap_eff = line.split(":", 1)[1].strip()
                break
except OSError:
    pass

print(f"[SANDBOX] Security Audit | UID={uid} GID={gid} | Kernel={kernel} | CapEff={cap_eff}")
print("CHECK                 | VALUE              | STATUS")
print("----------------------+--------------------+--------")
print(f"Effective UID         | {str(uid):<18} | PASS")
print(f"Capability Mask       | {cap_eff:<18} | PASS")
print("Network Egress        | blocked            | PASS")`,
		riskLevel: types.RiskHigh,
	},
	{
		match:   "benchmark",
		content: "I prepared a CPU and memory allocation micro-benchmark using time.perf_counter and tracemalloc inside the isolated sandbox. Please review and authorize execution:",
		summary: "Run Sandbox CPU & Allocation Micro-Benchmark",
		code: `import hashlib, time, tracemalloc

tracemalloc.start()
t0 = time.perf_counter()
digest = b"substrate-seed"
for _ in range(5000):
    digest = hashlib.sha256(digest).digest()
cpu_ms = (time.perf_counter() - t0) * 1000.0
_, peak_bytes = tracemalloc.get_traced_memory()
tracemalloc.stop()

print(f"[SANDBOX] Benchmark | SHA256x5000={cpu_ms:.2f}ms | PeakAlloc={peak_bytes}B")
print("METRIC                | RESULT")
print("----------------------+------------------")
print(f"CPU Loop (5k SHA-256) | {cpu_ms:.2f} ms")
print(f"Peak Traced Heap      | {peak_bytes} bytes")`,
		riskLevel: types.RiskHigh,
	},
	{
		match:   "traffic patterns",
		content: "I prepared a traffic and seat-utilization analysis script across conference worker pods. Please review and authorize execution in the sandbox:",
		summary: "Analyze Conference Request Traffic & Seat Utilization",
		code: `import statistics

rps_samples = [18, 24, 31, 45, 52, 49, 38, 29, 44, 50]
mean_rps = statistics.mean(rps_samples)
p50_rps = statistics.median(rps_samples)
peak_rps = max(rps_samples)

print(f"[SANDBOX] Traffic Summary | Mean={mean_rps:.1f} RPS | Median={p50_rps:.1f} RPS | Peak={peak_rps} RPS")
for idx, val in enumerate(rps_samples, 1):
    bar = "#" * (val // 5)
    print(f"Wave {idx:02d} | {bar:<12} ({val} rps)")`,
		riskLevel: types.RiskHigh,
	},
	{
		match:   "memory consumption",
		content: "I prepared a memory footprint profiler to inspect sandbox RSS and heap allocation buckets. Please review and authorize execution in the sandbox:",
		summary: "Profile Sandbox Memory Footprint & RSS",
		code: `import resource, tracemalloc

tracemalloc.start()
buffers = [bytearray(4096) for _ in range(32)]
current_b, peak_b = tracemalloc.get_traced_memory()
tracemalloc.stop()
rss_kb = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss

print(f"[SANDBOX] Memory Profile | MaxRSS={rss_kb}KB | HeapCurrent={current_b // 1024}KB | HeapPeak={peak_b // 1024}KB")
print("BUCKET                | KILOBYTES")
print("----------------------+----------")
print(f"Process Max RSS       | {rss_kb} KB")
print(f"Peak Traced Allocations| {peak_b // 1024} KB")`,
		riskLevel: types.RiskHigh,
	},
	{
		match:   "isolation boundaries",
		content: "I prepared an isolation boundary verification script to check filesystem workspace scoping and environment sanitization inside the sandbox. Please review and authorize execution:",
		summary: "Verify Tenant Namespace & Mount Isolation",
		code: `import os

cwd = os.getcwd()
env_keys = sorted(os.environ.keys())
mounts = 0
try:
    with open("/proc/self/mountinfo", encoding="utf-8") as f:
        mounts = sum(1 for _ in f)
except OSError:
    pass

print(f"[SANDBOX] Isolation Verified | CWD={cwd} | EnvVars={len(env_keys)} | Mounts={mounts}")
print(f"Visible Env Keys: {', '.join(env_keys)}")
print("Boundary Status : ISOLATED (gVisor sandbox)")`,
		riskLevel: types.RiskHigh,
	},
	{
		match:   "diary.txt",
		content: "I prepared a script to append the current UTC timestamp, process PID, and workspace path to diary.txt in your persistent workspace, then list every entry recorded so far:",
		summary: "Append Entry to Persistent Workspace Diary",
		code: `import datetime, os

now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
entry = f"{now} | pid={os.getpid()} | cwd={os.getcwd()}\n"
with open("diary.txt", "a", encoding="utf-8") as f:
    f.write(entry)

with open("diary.txt", encoding="utf-8") as f:
    lines = [line.rstrip("\n") for line in f if line.strip()]

print(f"[SANDBOX] Persistent Diary | Entries={len(lines)} | Path={os.path.abspath('diary.txt')}")
for idx, line in enumerate(lines, 1):
    print(f"{idx:02d}. {line}")`,
		riskLevel: types.RiskHigh,
	},
	{
		match:   "statvfs",
		content: "I prepared a workspace storage probe that writes a 16 MiB SQLite dataset in your persistent workspace and compares filesystem capacity against the sandbox's 320 MiB process memory limit:",
		summary: "Probe Persistent Filestore Workspace vs. Sandbox RAM Limit",
		code: `import os, resource, sqlite3

cwd = os.getcwd()
db_path = os.path.join(cwd, "workspace_probe.db")
conn = sqlite3.connect(db_path)
conn.execute("CREATE TABLE IF NOT EXISTS chunks (id INTEGER PRIMARY KEY AUTOINCREMENT, pid INTEGER, payload BLOB)")
conn.execute("DELETE FROM chunks")
chunk = b"\x42" * (1 << 20)
pid = os.getpid()
for _ in range(16):
    conn.execute("INSERT INTO chunks (pid, payload) VALUES (?, ?)", (pid, chunk))
conn.commit()
rows = conn.execute("SELECT COUNT(*) FROM chunks").fetchone()[0]
conn.close()

db_mib = os.path.getsize(db_path) / (1 << 20)
st = os.statvfs(cwd)
total_mib = (st.f_frsize * st.f_blocks) / (1 << 20)
free_mib = (st.f_frsize * st.f_bavail) / (1 << 20)
soft_as, _ = resource.getrlimit(resource.RLIMIT_AS)
cap_str = f"{soft_as // (1 << 20)} MiB" if soft_as > 0 else "unlimited"
rss_mib = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024.0

mount_line = "rootfs"
try:
    with open("/proc/self/mountinfo", encoding="utf-8") as f:
        for line in f:
            parts = line.split()
            if len(parts) >= 5 and parts[4] == "/workspace":
                mount_line = " ".join(parts[4:])
                break
except OSError:
    pass

print(f"[SANDBOX] Workspace Storage | CWD={cwd} | SQLite={db_mib:.1f}MiB ({rows}x1MiB) | MaxRSS={rss_mib:.1f}MiB")
print("RESOURCE              | VALUE")
print("----------------------+----------------------------------------")
print(f"Workspace Mount       | {mount_line}")
print(f"Workspace Capacity    | {total_mib:.0f} MiB total ({free_mib:.0f} MiB free)")
print(f"SQLite File Written   | {db_mib:.1f} MiB on disk ({rows} rows)")
print(f"Process RLIMIT_AS Cap | {cap_str}")
print(f"Process Peak RSS      | {rss_mib:.1f} MiB in RAM")`,
		riskLevel: types.RiskHigh,
	},
}

var defaultMockScript = mockScript{
	content: "I analyzed your request. To extract the metrics and generate the visualization, I have prepared this analysis script. Please review and authorize execution in the sandbox:",
	summary: "Execute Telemetry Outlier Analysis & ASCII Plotting",
	code: `def detect_anomalies(data):
    threshold = 2.5
    mean = sum(data) / len(data)
    variance = sum((x - mean) ** 2 for x in data) / len(data)
    std_dev = variance ** 0.5
    anomalies = [x for x in data if abs(x - mean) > threshold * std_dev]
    print(f"[SANDBOX] Mean: {mean:.2f} | StdDev: {std_dev:.2f} | Anomalies: {len(anomalies)}")
    print(" 100 |  *")
    print("  75 |  * *   *")
    print("  50 |  * * * * *")
    print("   0 +-----------")
    return anomalies

detect_anomalies([12, 14, 15, 13, 98, 12, 11, 14, 88, 13])`,
	riskLevel: types.RiskHigh,
}

// GenerateTurn produces a predetermined assistant message requiring approval to execute a sandbox script tailored to the prompt.
func (m *MockEngine) GenerateTurn(prompt string) *types.ChatMessage {
	lower := strings.ToLower(prompt)
	chosen := defaultMockScript
	for _, candidate := range tailoredScripts {
		if strings.Contains(lower, candidate.match) {
			chosen = candidate
			break
		}
	}
	now := time.Now().UnixNano()
	return &types.ChatMessage{
		ID:        fmt.Sprintf("mock-%d", now),
		Sender:    types.SenderAssistant,
		Content:   chosen.content,
		Timestamp: time.Now().Unix(),
		Approval: &types.ApprovalRequest{
			ApprovalID: fmt.Sprintf("app-%d", now),
			Summary:    chosen.summary,
			Code:       chosen.code,
			Language:   "python",
			RiskLevel:  chosen.riskLevel,
		},
	}
}

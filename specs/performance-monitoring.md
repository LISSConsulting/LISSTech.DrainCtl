# Feature Spec: RDS Performance Monitoring

**Status:** Draft
**Date:** 2025-04-07
**Author:** Marcin Wisniowski

---

## 1. Problem Statement

DrainCtl monitors drain mode lifecycle and session counts but has no visibility
into *how well* sessions are performing. An RDSH host can show 40% session
utilization while users experience 200ms input delay, degraded frame quality, or
45-second logon times. Without performance telemetry, drain decisions rely
entirely on an administrative flag and a coarse session count, missing the
signals that predict problems before users open tickets.

## 2. Goals

1. Collect host-level and per-session performance metrics via Windows Performance
   Data Helper (PDH) API during every poll cycle.
2. Surface performance data in `CheckResult`, audit trail, dashboard, and CLI
   output.
3. Add performance-based notification triggers so operators are alerted when
   user experience degrades, not just when drain mode changes.
4. Provide configurable thresholds with sensible defaults drawn from Microsoft
   guidance and industry benchmarks.
5. Keep the feature optional: zero-config falls back to current behavior; opting
   in is a single `"performance"` block in `config.json`.

## 3. Non-Goals

- Per-process metrics (e.g., User Input Delay per Process). Deferred to a future
  release; requires process enumeration and correlation logic.
- GPU / RemoteFX GPU counters. Most DrainCtl deployments target CPU-bound RDSH
  workloads, not GPU-accelerated VDI.
- Automated drain decisions based on performance thresholds. DrainCtl reports
  status; N-Central or operators act on it.
- Historical trending or time-series storage. The dashboard already keeps a
  100-record ring buffer per host; performance snapshots ride the same bus.

## 4. Metrics

### 4.1 Host-Level Counters

| ID | Counter Path | Field Name | Unit | Default Threshold (Warn / Crit) | Why |
|----|---|---|---|---|---|
| H1 | `\Processor(_Total)\% Processor Time` | `cpu_pct` | % | 70 / 85 | Universal overload indicator. Correlates directly with input delay. |
| H2 | `\Memory\Available MBytes` | `mem_avail_mb` | MB | 20% free / 10% free | Memory pressure causes paging, logon failures. Thresholds are % of installed RAM, stored as absolute MB for comparison. |
| H3 | `\Memory\Pages/sec` | `pages_sec` | count/s | 500 / 1000 | Sustained paging = active memory thrashing. |
| H4 | `\PhysicalDisk(_Total)\Avg. Disk Queue Length` | `disk_queue` | float | 2.0 / 4.0 | I/O contention. Especially critical when UPD/FSLogix lives on local storage. |
| H5 | `\TCPv4\Segments Retransmitted/sec` | `tcp_retrans` | count/s | informational | Network reliability. No default threshold (baseline-dependent). |

### 4.2 Per-Session Counters (Aggregated)

These counters exist per session instance. DrainCtl collects them across all
sessions and reports **P50, P95, and Max** aggregates.

| ID | Counter Path | Field Name Prefix | Unit | Default Threshold (Warn / Crit) | Why |
|----|---|---|---|---|---|
| S1 | `\User Input Delay per Session(*)\Max Input Delay` | `input_delay` | ms | 50 / 100 | Microsoft's #1 recommended metric. Directly measures what users feel. |
| S2 | `\Terminal Services Session(*)\% Processor Time` | `session_cpu` | % | informational | Identifies noisy neighbors. No threshold; surfaced in dashboard for drill-down. |
| S3 | `\Terminal Services Session(*)\Working Set` | `session_mem` | bytes | informational | Per-session memory footprint. |

### 4.3 RemoteFX Counters (Optional)

Collected only when the `\RemoteFX Graphics` counter set is present (i.e., the
RemoteFX role is installed). Absence is not an error.

| ID | Counter Path | Field Name Prefix | Unit | Default Threshold (Warn / Crit) | Why |
|----|---|---|---|---|---|
| R1 | `\RemoteFX Graphics(*)\Output Frames/Second` | `rfx_fps_out` | fps | informational | Baseline display quality. |
| R2 | `\RemoteFX Graphics(*)\Frames Skipped/Second - Insufficient Server Resources` | `rfx_skip_srv` | count/s | 1 / 5 | Server bottleneck causing visible quality loss. |
| R3 | `\RemoteFX Graphics(*)\Frames Skipped/Second - Insufficient Network Resources` | `rfx_skip_net` | count/s | 1 / 5 | Network bottleneck. |
| R4 | `\RemoteFX Graphics(*)\Average Encoding Time` | `rfx_encode_ms` | ms | 17 / 33 | At 33ms, the server hits the 30fps ceiling. |
| R5 | `\RemoteFX Graphics(*)\Frame Quality` | `rfx_quality` | % | 70 / 50 | Output quality as % of source (inverted: lower = worse). |
| R6 | `\RemoteFX Network(*)\Current TCP RTT` | `rfx_rtt` | ms | 100 / 150 | Network latency. Users notice lag above ~120ms. |
| R7 | `\RemoteFX Network(*)\Loss Rate` | `rfx_loss` | % | 0.5 / 2.0 | Even 0.5% packet loss degrades RDP display quality. |

### 4.4 User Input Delay Prerequisites

The `\User Input Delay per Session` counter set requires:
- Windows Server 2019+ or Windows 10 1809+.
- On older OS: registry key `HKLM\System\CurrentControlSet\Control\Terminal Server\EnableLagCounter` = 1 (DWORD).

DrainCtl should attempt to open the counter. If it fails, log a one-time
`LvlWRN` message referencing the registry key and continue without it. The
remaining counters are unaffected.

## 5. Data Model

### 5.1 PerfSnapshot

```go
// PerfSnapshot holds one point-in-time performance sample.
type PerfSnapshot struct {
    // Host-level
    CPUPct       float64 `json:"cpu_pct"`
    MemAvailMB   float64 `json:"mem_avail_mb"`
    MemTotalMB   float64 `json:"mem_total_mb"`       // for % calculation
    PagesSec     float64 `json:"pages_sec"`
    DiskQueue    float64 `json:"disk_queue"`
    TCPRetrans   float64 `json:"tcp_retrans_sec"`

    // Per-session aggregates (User Input Delay)
    InputDelayP50 float64 `json:"input_delay_p50_ms"`
    InputDelayP95 float64 `json:"input_delay_p95_ms"`
    InputDelayMax float64 `json:"input_delay_max_ms"`

    // Per-session aggregates (Terminal Services Session)
    SessionCPUP95 float64 `json:"session_cpu_p95_pct,omitempty"`
    SessionMemP95 float64 `json:"session_mem_p95_bytes,omitempty"`

    // RemoteFX (zero-valued when unavailable)
    RFXAvailable  bool    `json:"rfx_available"`
    RFXFPSOut     float64 `json:"rfx_fps_out,omitempty"`
    RFXSkipServer float64 `json:"rfx_skip_server_sec,omitempty"`
    RFXSkipNet    float64 `json:"rfx_skip_net_sec,omitempty"`
    RFXEncodeMS   float64 `json:"rfx_encode_ms,omitempty"`
    RFXQuality    float64 `json:"rfx_quality_pct,omitempty"`
    RFXRTT        float64 `json:"rfx_rtt_ms,omitempty"`
    RFXLoss       float64 `json:"rfx_loss_pct,omitempty"`
}
```

### 5.2 CheckResult Extension

```go
type CheckResult struct {
    // ... existing fields ...
    Performance *PerfSnapshot `json:"performance,omitempty"`
}
```

The `Performance` field is `nil` when performance monitoring is disabled,
preserving backward compatibility for all consumers (CLI, dashboard, N-Central).

### 5.3 AuditRecord Extension

```go
type AuditRecord struct {
    // ... existing fields ...
    CPUPct        float64 `json:"cpu_pct,omitempty"`
    InputDelayMax float64 `json:"input_delay_max_ms,omitempty"`
}
```

Only headline metrics are persisted in the audit trail to keep JSONL line sizes
manageable. The full `PerfSnapshot` lives in the dashboard's in-memory history.

## 6. Configuration

### 6.1 Config Schema

```go
type PerformanceConfig struct {
    Enabled bool `json:"enabled"` // default: false

    // Thresholds — 0 means "use default", -1 means "disabled".
    CPUWarnPct       int `json:"cpu_warn_pct"`       // default: 70
    CPUCritPct       int `json:"cpu_crit_pct"`       // default: 85
    MemWarnPct       int `json:"mem_warn_pct"`       // default: 20 (% free)
    MemCritPct       int `json:"mem_crit_pct"`       // default: 10 (% free)
    InputDelayWarnMS int `json:"input_delay_warn_ms"` // default: 50
    InputDelayCritMS int `json:"input_delay_crit_ms"` // default: 100

    // Counter collection control
    CollectRemoteFX  bool `json:"collect_remotefx"`   // default: false
    CollectPerSession bool `json:"collect_per_session"` // default: true
}
```

```json
{
  "grace_period": 60,
  "poll_interval": 300,
  "performance": {
    "enabled": true,
    "cpu_warn_pct": 70,
    "cpu_crit_pct": 85,
    "mem_warn_pct": 20,
    "mem_crit_pct": 10,
    "input_delay_warn_ms": 50,
    "input_delay_crit_ms": 100,
    "collect_remotefx": false,
    "collect_per_session": true
  }
}
```

### 6.2 Config Field in Parent Struct

```go
type Config struct {
    // ... existing fields ...
    Performance PerformanceConfig `json:"performance"`
}
```

### 6.3 Defaults

When `performance.enabled` is `false` (or the `performance` key is absent),
no PDH queries are opened and `CheckResult.Performance` is `nil`. This is the
zero-cost default for existing installations.

## 7. Notification Triggers

### 7.1 New Triggers

| Trigger | Fires When | Repeat Behavior |
|---|---|---|
| `cpu_warning` | CPU >= `cpu_warn_pct` for 2 consecutive polls | Per-target repeat interval (like `session_warning`) |
| `cpu_critical` | CPU >= `cpu_crit_pct` for 2 consecutive polls | Per-target repeat interval |
| `input_delay_warning` | Input Delay P95 >= `input_delay_warn_ms` | Per-target repeat interval |
| `input_delay_critical` | Input Delay P95 >= `input_delay_crit_ms` | Per-target repeat interval |
| `memory_warning` | Available memory <= `mem_warn_pct` of total | Per-target repeat interval |
| `memory_critical` | Available memory <= `mem_crit_pct` of total | Per-target repeat interval |

### 7.2 Consecutive-Poll Requirement

CPU and memory fluctuate. To avoid flapping, `cpu_warning`, `cpu_critical`,
`memory_warning`, and `memory_critical` require the threshold to be breached on
**2 consecutive polls** before firing. `input_delay_*` fires on a single breach
because sustained input delay is immediately impactful.

### 7.3 Notification Payload

The existing webhook/ntfy/email payload gains a `performance` object when
performance monitoring is enabled. Example webhook body:

```json
{
  "version": "25.097.0",
  "host": "RDSH-01",
  "status": "Healthy",
  "drain_mode": "ALLOW_ALL_CONNECTIONS",
  "trigger": "cpu_warning",
  "sessions": { "active_sessions": 12, "total_sessions": 14, "max_sessions": 20 },
  "performance": {
    "cpu_pct": 78.3,
    "mem_avail_mb": 2048,
    "input_delay_p95_ms": 42,
    "input_delay_max_ms": 88
  },
  "message": "CPU at 78% (threshold: 70%)"
}
```

### 7.4 DefaultTriggers Update

The existing `DefaultTriggers` set is unchanged. New performance triggers are
opt-in only: operators must explicitly list them in a target's `triggers` array.

```go
var ValidTriggers = map[Trigger]bool{
    // ... existing ...
    TriggerCPUWarning:        true,
    TriggerCPUCritical:       true,
    TriggerInputDelayWarning: true,
    TriggerInputDelayCritical: true,
    TriggerMemoryWarning:     true,
    TriggerMemoryCritical:    true,
}
```

## 8. Implementation

### 8.1 Package: `internal/perfmon`

New package encapsulating PDH API interaction.

```
internal/perfmon/
    pdh.go          — pdh.dll syscall bindings
    collector.go    — Collector struct, Open/Collect/Close lifecycle
    snapshot.go     — PerfSnapshot assembly + percentile aggregation
```

#### pdh.go — Syscall Bindings

Thin wrappers around:

| Function | Purpose |
|---|---|
| `PdhOpenQueryW` | Create a query handle |
| `PdhAddEnglishCounterW` | Add counter by English name (locale-independent) |
| `PdhCollectQueryData` | Sample all counters in the query |
| `PdhGetFormattedCounterValue` | Read a scalar counter value (double) |
| `PdhGetFormattedCounterArray` | Read a multi-instance counter (per-session) |
| `PdhCloseQuery` | Release query handle |

Using `PdhAddEnglishCounterW` (available since Vista SP1) avoids
locale-sensitive counter path strings. All counter paths in this spec are
English canonical names.

#### collector.go — Lifecycle

```go
type Collector struct {
    query   syscall.Handle
    // counter handles, one per metric
    cpuH, memH, pagesH, diskH, retransH syscall.Handle
    // per-session
    inputDelayH, sessCPUH, sessMemH syscall.Handle
    // remotefx (may be zero if unavailable)
    rfxFPSH, rfxSkipSrvH, rfxSkipNetH, rfxEncH, rfxQualH, rfxRTTH, rfxLossH syscall.Handle
    rfxAvailable bool
}

// Open creates the PDH query and adds counters.
// Counters that fail to add (e.g., RemoteFX not installed, User Input Delay
// not available) are logged at LvlWRN and skipped.
func Open(cfg PerformanceConfig, log LogFunc) (*Collector, error)

// Collect samples all counters and returns a PerfSnapshot.
// Must be called at least twice (PDH needs two samples for rate counters).
// The first call returns partial data; callers should discard it.
func (c *Collector) Collect() (*PerfSnapshot, error)

// Close releases the PDH query and all counter handles.
func (c *Collector) Close()
```

### 8.2 Integration Points

#### check.go

After session tracking (~line 102), before audit store evaluation:

```go
// ── Performance counters ───────────────────────────────────────────
if perfCollector != nil {
    snap, err := perfCollector.Collect()
    if err != nil {
        log(LvlWRN, fmt.Sprintf("perfmon collect failed: %v", err))
    } else {
        res.Performance = snap
    }
}
```

The `perfCollector` is created once at service startup and reused across polls.
CLI `check` command does **not** collect performance data (PDH rate counters
need two samples separated by an interval; a single CLI invocation cannot
provide meaningful rates). Performance monitoring is service-mode only.

#### internal/svc/handler.go

- Create `perfmon.Collector` during service init (after config load).
- First `Collect()` call immediately after creation (primes rate counters).
- Subsequent `Collect()` calls at each poll tick.
- Close collector on service stop.
- Re-create collector on config reload if `performance.enabled` changes.

#### internal/svc/check.go (service-level check orchestration)

Add performance evaluation after session-warning evaluation:

```go
// ── Performance thresholds ─────────────────────────────────────────
if snap := result.Performance; snap != nil {
    evaluatePerformanceThresholds(snap, cfg.Performance, notifyState, result, log)
}
```

#### Dashboard

- `CheckResult.Performance` flows through the existing report API unchanged.
- Dashboard HTML gains a "Performance" tab or expandable section per server
  card showing: CPU gauge, memory gauge, input delay sparkline.
- `/api/v1/servers/{host}` response includes performance data.
- Dashboard health endpoint (`/api/v1/health`) gains aggregate performance
  status across all reporting hosts.

#### CLI

- `drainctl check --format json` includes `performance: null` (service not
  running or feature disabled).
- `drainctl status` (future command, out of scope) would query the service pipe
  for live performance data.

### 8.3 PDH Sampling Considerations

1. **Two-sample requirement:** Rate counters (`\Processor\% Processor Time`,
   `\Memory\Pages/sec`, etc.) require two `PdhCollectQueryData` calls to
   compute a rate. The service calls `Collect()` once at startup (discards the
   result) and then once per poll tick. With a 300s default poll interval, the
   rate values represent a 5-minute average. This is intentional — 5-minute
   averages smooth out transient spikes and match industry monitoring practice.

2. **Counter handle lifetime:** PDH query handles are opened once and reused
   for the lifetime of the service (or until config reload). This avoids the
   overhead of re-enumerating counter instances on every poll.

3. **Instance enumeration:** Per-session counters (`\Terminal Services Session`,
   `\User Input Delay per Session`) have dynamic instances that appear/disappear
   as sessions connect/disconnect. `PdhGetFormattedCounterArray` handles this
   correctly, returning the current set of instances at collection time.

4. **Locale independence:** `PdhAddEnglishCounterW` resolves English counter
   names to the local language internally. This is critical for non-English
   Windows Server installations.

## 9. Exit Code Semantics

Performance metrics do **not** alter the exit code. DrainCtl's exit code
reflects drain mode status only:

| Exit Code | Meaning |
|---|---|
| 0 | Healthy or Grace (drain within grace period) |
| 1 | Alert (drain exceeds grace period) |
| 2 | Error (registry unreadable, service failure) |

Performance degradation fires notification triggers but does not change the
exit code. This preserves backward compatibility with N-Central automation
policies that key on exit codes for drain-mode decisions.

## 10. Dashboard Display

### 10.1 Server Card Enhancement

Each server card gains a collapsible "Performance" section below the existing
session gauge:

```
┌─────────────────────────────────────────────┐
│  RDSH-01  ●  Healthy                        │
│  Sessions: 12/20 (60%)  ████████░░          │
│  ▸ Performance                              │
│    CPU: 45%  ████░░░░░░  Memory: 68% free   │
│    Input Delay P95: 22ms  Max: 48ms         │
│    Disk Queue: 0.3                          │
└─────────────────────────────────────────────┘
```

### 10.2 Threshold Indicators

- Values within healthy range: default text color.
- Values at warning threshold: amber/yellow.
- Values at critical threshold: red.

### 10.3 History Charts

The existing uPlot time-series chart gains optional series for CPU % and Input
Delay P95 (toggle-able via checkboxes). These share the y-axis with session
count or get a secondary axis.

## 11. Testing Strategy

### 11.1 Unit Tests

| Area | Approach |
|---|---|
| `internal/perfmon/snapshot.go` | Test percentile aggregation with known input arrays |
| `PerfSnapshot` serialization | Round-trip JSON marshal/unmarshal |
| Threshold evaluation | Table-driven tests with `PerfSnapshot` inputs and expected triggers |
| Config parsing | Verify defaults, clamping, validation |
| Consecutive-poll logic | State machine tests: single breach → no fire, two consecutive → fire, drop below → reset |

### 11.2 Integration Tests

| Area | Approach |
|---|---|
| PDH counter open/collect | Open real counters, collect twice, verify non-negative values. Requires Windows. |
| RemoteFX fallback | Verify graceful degradation when RemoteFX counters are absent |
| User Input Delay fallback | Verify graceful degradation on Server 2016 (counter absent) |

### 11.3 Build Constraint

All files in `internal/perfmon/` carry `//go:build windows` per project rules.
Tests use `_test.go` convention and run only on Windows.

## 12. Rollout

### Phase 1: Core Counters (MVP)

- `internal/perfmon` package with PDH bindings
- Host-level counters: CPU, memory, pages/sec, disk queue
- `PerfSnapshot` in `CheckResult`
- `PerformanceConfig` in `config.json`
- Basic threshold evaluation + `cpu_warning`/`cpu_critical`/`memory_warning`/
  `memory_critical` triggers
- Unit + integration tests

### Phase 2: Input Delay + Per-Session

- User Input Delay counter with OS-version detection and fallback
- Per-session CPU/memory aggregation (P50/P95/Max)
- `input_delay_warning`/`input_delay_critical` triggers
- Dashboard performance section

### Phase 3: RemoteFX (Optional)

- RemoteFX Graphics + Network counters
- Conditional collection (counter set presence detection)
- Dashboard sparklines for frame rate, RTT, quality
- Extended webhook payload

## 13. Open Questions

1. **Poll interval interaction.** A 300s poll interval yields 5-minute rate
   averages. Is this granular enough for input delay alerting, or should
   performance counters sample on a shorter sub-interval (e.g., 30s) independent
   of the main poll tick?

2. **Per-session detail in dashboard.** Should the dashboard expose a per-session
   breakdown (top-5 sessions by CPU, top-5 by input delay), or only aggregates?
   Per-session detail adds significant UI complexity.

3. **CLI `perf` subcommand.** Should there be a `drainctl perf` command that
   opens PDH, collects two samples 5 seconds apart, and prints a one-shot
   snapshot? Useful for ad-hoc diagnostics but adds surface area.

4. **Threshold auto-baselining.** Microsoft recommends establishing environment-
   specific baselines. Should DrainCtl offer a "learning mode" that observes
   metrics for N days and suggests thresholds? This adds substantial complexity
   and may belong in a future release.

## 14. References

- [Microsoft: User Input Delay Performance Counters](https://learn.microsoft.com/en-us/windows-server/remote/remote-desktop-services/rds-rdsh-performance-counters)
- [Microsoft: RemoteFX Graphics Performance Counters](https://learn.microsoft.com/en-us/azure/virtual-desktop/remotefx-graphics-performance-counters)
- [Microsoft: Remote Desktop Protocol (RDP) bandwidth requirements](https://learn.microsoft.com/en-us/azure/virtual-desktop/rdp-bandwidth)
- [PDH API Reference](https://learn.microsoft.com/en-us/windows/win32/perfctrs/using-the-pdh-functions-to-consume-counter-data)
- [PdhAddEnglishCounterW](https://learn.microsoft.com/en-us/windows/win32/api/pdh/nf-pdh-pdhaddEnglishcounterw)

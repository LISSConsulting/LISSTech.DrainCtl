# Data Model: Runtime Resilience and Diagnostics (012)

This document defines the durable and event contracts required by [spec.md](./spec.md). All times are UTC RFC3339Nano in APIs and UTC instants in storage.

## 1. Existing entities reused

### `ServerInfo` / `ServerState`

**Source**: `internal/dashboard/store.go`.

| Field | Role in this sprint |
|---|---|
| Canonical hostname | Stable key for all host freshness state. |
| `LastSeen` | Server-side time of the last accepted report; defines the report epoch. |
| `LastResult` | Last reported health and optional performance state. It does not itself determine staleness. |
| `RegisteredAt` | Registration metadata only; not a freshness substitute. |

**Invariant**: `LastSeen` is assigned by the dashboard on accepted report, never trusted from agent wall clock for freshness decisions.

### `PerfSnapshot`

**Source**: root performance type consumed by `internal/perfmon/collector.go` and persisted via `internal/dashboard/samples.go`.

RemoteFX fields remain optional. Absence means no data; it is never reinterpreted as zero.

## 2. New durable entity: HostFreshness

Suggested persistence name: `host_freshness`.

| Field | Type | Required | Meaning |
|---|---|---:|---|
| `host` | canonical hostname text | yes | Primary key; identical canonicalization to `ServerState`. |
| `last_accepted_at` | UTC timestamp | yes after first report | Freshness epoch key. Copied from dashboard accepted-report time. |
| `state` | `fresh` \| `offline` | yes after first report | Current derived state. Hosts without this row/report are `unknown`. |
| `offline_emitted_for` | UTC timestamp nullable | no | Epoch for which `host_offline` was successfully committed for publication. |
| `recovered_emitted_for` | UTC timestamp nullable | no | Epoch reached after a prior offline state for which `host_recovered` was committed for publication. |
| `updated_at` | UTC timestamp | yes | Diagnostic/audit timestamp for transition record mutation. |

### Constraints and transitions

1. `host` is unique.
2. `last_accepted_at` is monotonically nondecreasing for a host; the report acceptance transaction assigns its value.
3. `offline_emitted_for`, when non-null, equals `last_accepted_at` for the epoch that became stale. It is not cleared merely because the host stays stale.
4. A new accepted report replaces `last_accepted_at`, sets `state=fresh`, and clears epoch-local offline state. If it follows `state=offline`, it records the new epoch in `recovered_emitted_for` exactly once.
5. `Unknown` is represented by no row or no accepted report. It must not emit `host_offline`.
6. Transition persistence precedes SSE publication. If broadcast fails after persistence, the broker's normal reconnect/reconciliation behavior may catch clients up, but a later evaluator must not publish a duplicate event for the same epoch.

### Transition transaction sketches

**Accepted report**:

```text
BEGIN
  read host_freshness FOR UPDATE / equivalent serialized update
  wasOffline := state == offline
  write last_accepted_at = dashboardNow, state = fresh, updated_at = dashboardNow
  if wasOffline: write recovered_emitted_for = dashboardNow
COMMIT
publish host_recovered once if wasOffline
publish existing server_update
```

**Freshness evaluation**:

```text
BEGIN
  read host_freshness
  if state == fresh AND now >= last_accepted_at + 3 * effectiveInterval:
      write state = offline, offline_emitted_for = last_accepted_at, updated_at = now
      transitioned = true
COMMIT
if transitioned: publish host_offline once
```

The concrete database API may use an atomic conditional update rather than a literal SQL `FOR UPDATE`; the observable compare-and-set semantics are mandatory.

## 3. SSE event additions

**Envelope source**: `internal/dashboard/broker.go` `SSEEvent`.

Existing envelope remains:

```json
{
  "type": "<event-type>",
  "host": "<canonical host>",
  "data": {},
  "timestamp": "2026-09-26T12:00:00Z"
}
```

### `host_offline`

```json
{
  "type": "host_offline",
  "host": "server-a",
  "data": {
    "last_accepted_at": "2026-09-26T11:57:00Z",
    "observed_at": "2026-09-26T12:00:00Z",
    "heartbeat_interval_ms": 60000,
    "offline_threshold_ms": 180000
  },
  "timestamp": "2026-09-26T12:00:00Z"
}
```

### `host_recovered`

```json
{
  "type": "host_recovered",
  "host": "server-a",
  "data": {
    "previous_last_accepted_at": "2026-09-26T11:57:00Z",
    "accepted_at": "2026-09-26T12:04:00Z",
    "heartbeat_interval_ms": 60000,
    "offline_threshold_ms": 180000
  },
  "timestamp": "2026-09-26T12:04:00Z"
}
```

**Event invariants**:

- `host_offline` occurs at most once per `last_accepted_at` epoch.
- `host_recovered` occurs at most once per offline period and only after a report accepted after that offline state.
- Event timestamps are dashboard-observed times.
- `server_update` continues to carry the `ServerView`; clients that ignore new types remain compatible.
- No event includes dump paths, dump bytes, WER registry values containing environment-specific paths, tokens, or credentials.

## 4. RemoteFX validity model

Normalization returns one independently validated optional value per field. It never clamps outliers and never converts absence to zero.

| Logical field | Stored counters | Accepted values | Zero semantic | Direction |
|---|---|---|---|---|
| Output FPS | `rfx_fps_out`, `rfx_fps_out_p50` | finite, `0 < value <= 240` | inactive stream → omit | higher is better; P95 label = numeric P5 |
| Frame quality | `rfx_quality_pct`, `rfx_quality_pct_p50` | finite, `0 < value <= 100` | inactive stream → omit | higher is better; P95 label = numeric P5 |
| Encoding time | `rfx_encode_ms`, `rfx_encode_ms_p50` | finite, `0 <= value <= 60000` | valid | lower is better; P95 label = numeric P95 |
| TCP RTT | `rfx_rtt_ms`, `rfx_rtt_ms_p50` | finite, `0 <= value <= 60000` | valid | lower is better; P95 label = numeric P95 |
| Loss rate | `rfx_loss_pct`, `rfx_loss_pct_p50` | finite, `0 <= value <= 100` | valid | lower is better; P95 label = numeric P95 |
| Server skips | `rfx_skip_server_sec`, `rfx_skip_server_sec_p50` | finite, `0 <= value <= 1000000` | valid | lower is better; P95 label = numeric P95 |
| Network skips | `rfx_skip_net_sec`, `rfx_skip_net_sec_p50` | finite, `0 <= value <= 1000000` | valid | lower is better; P95 label = numeric P95 |

### Value disposition

| Input | Disposition |
|---|---|
| Field missing/provider unavailable/no PDH instance | Leave field absent; chart gap. |
| FPS/quality exactly zero | Leave field absent; inactive/no stream. |
| Valid finite in-range number | Retain and aggregate/persist. |
| NaN, `+Inf`, `-Inf`, negative, or above maximum | Drop only this field; emit rate-limited `remotefx_value_dropped` diagnostic. |

**Aggregation invariant**: A percentile uses only retained valid observations. For FPS/quality, `AggregateServicePercentiles(values, true)` uses numeric P5; for all other rows it uses numeric P95. Missing samples do not participate.

## 5. Local dump configuration and inventory

WER is configured through Windows registry, not product SQLite.

### LocalDumps registry record

| Registry location | Value | Required value |
|---|---|---|
| `HKLM\Software\Microsoft\Windows\Windows Error Reporting\LocalDumps\drainctld.exe` | `DumpFolder` | Product ProgramData `dumps` directory (expandable string if required by WER). |
| Same | `DumpType` | DWORD `1` (mini dump). |
| Same | `DumpCount` | DWORD `3`. |

**Ownership**: installer/repair creates and maintains the record; WER creates and prunes dump files. The application does not create a process dump on its own crash path.

### Dump directory security contract

| Principal | Access |
|---|---|
| LocalSystem | Full control |
| Built-in Administrators | Full control |
| All other inherited/broad principals | No inherited grant to dump contents |

The implementation must use explicit ACL verification after application. Any inability to establish this DACL is a diagnostic-setup failure; it must not fall back to an unprotected location.

### Optional admin-only inventory view

The following metadata may be read by a local administrator from the protected directory:

```json
{
  "configured": true,
  "max_dumps": 3,
  "dumps": [
    {"name": "drainctld.exe.1234.dmp", "bytes": 123456, "created_at": "2026-09-26T12:00:00Z"}
  ]
}
```

It must not return file content, a download URL, stack-memory fragments, or a dump path to non-admin/dashboard clients.

## 6. Diagnostic counters and logs

| Signal | Cardinality | Persistence | Meaning |
|---|---:|---|---|
| `remotefx_value_dropped` | bounded metric-name × reason | log/counter only | Invalid optional value was removed before persistence. |
| `host_offline` / `host_recovered` | bounded by host × report epoch | durable freshness state + SSE | Freshness boundary occurred. |
| `wer_localdumps_configured` | process-wide | installer/service log | Protected local crash diagnostics setup succeeded. |
| `wer_localdumps_setup_failed` | process-wide | installer/service log | Setup was unavailable or unsafe. |
| dump inventory count | <= 3 | on-demand local inspection | Local artifacts exist; no content exposure. |

No dump-content-derived field belongs in `metrics_raw`, audit history, report payloads, or SSE.

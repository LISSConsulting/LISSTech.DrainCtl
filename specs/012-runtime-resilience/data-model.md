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

## 2. Durable entity: `host_freshness` (SQLite schema v3)

**Persistence**: the dashboard telemetry SQLite database, `PRAGMA user_version = 3`.

| Column | Type | Required | Meaning |
|---|---|---:|---|
| `host` | `TEXT PRIMARY KEY COLLATE NOCASE` | yes | Canonical hostname, using the same canonicalization as `ServerState`. |
| `report_epoch_ms` | `INTEGER NOT NULL` | yes | Dashboard-side accepted-report time in UTC Unix milliseconds; the current freshness epoch key. |
| `offline_emitted_at_ms` | `INTEGER NULL` | no | Dashboard-observed UTC Unix milliseconds when this epoch atomically crossed offline; `NULL` means the current epoch is fresh. |

There is no process-local dedupe state in the contract. `last_result` and `last_seen_ms` remain the server record's accepted-report data and are never modified by an offline transition.

### Constraints and transitions

1. `host` is unique case-insensitively and is canonicalized before every read or write.
2. `report_epoch_ms` is monotonic per host. A delayed operation for an older epoch is ignored and cannot overwrite a newer accepted report.
3. `offline_emitted_at_ms` is set once, only for the matching `report_epoch_ms`; while it is non-`NULL`, repeated sweeps emit no further offline transition for that epoch.
4. Accepting a newer report atomically replaces `report_epoch_ms` and clears `offline_emitted_at_ms`. If the prior epoch was offline, that same accepted-report transition is the single recovery transition.
5. A host without an accepted report has no freshness row and is `unknown`; it never emits `host_offline`.
6. The database transaction commits before SSE publication. A failed publication does not permit a later evaluator to duplicate the same epoch transition.

### Atomic transition sketches

**Accepted report**:

```text
BEGIN IMMEDIATE / writer transaction
  read host_freshness for canonical host
  reject if accepted epoch is older than stored epoch
  recovered := stored offline_emitted_at_ms IS NOT NULL AND accepted epoch > stored epoch
  upsert report_epoch_ms = accepted epoch, offline_emitted_at_ms = NULL
COMMIT
if recovered: publish host_recovered once
publish existing server_update
```

**Freshness evaluation**:

```text
BEGIN IMMEDIATE / writer transaction
  read host_freshness for canonical host and matching accepted-report epoch
  if offline_emitted_at_ms IS NULL AND now >= accepted epoch + 3 * effectiveInterval:
      set offline_emitted_at_ms = dashboard now
      transitioned = true
COMMIT
if transitioned: publish host_offline once
```

The implementation uses serialized SQLite writer transactions rather than relying on a read-then-write race. Updating the configured heartbeat interval wakes the worker immediately, resets its timer cadence, and makes the next sweep use the new effective interval.

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

- `host_offline` occurs at most once per canonical host and `report_epoch_ms`.
- `host_recovered` occurs once only when a newer accepted report clears an offline epoch.
- Event timestamps are dashboard-observed times. Freshness always uses dashboard acceptance time, never agent wall time.
- `server_update` continues to carry the `ServerView`; the new event types are additive so clients that ignore them remain compatible.
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

### Scheduled diagnostic inventory and opt-in artifact handling

The disabled `\LISS Technologies\DrainCtl-Diags` task writes service/WER/event evidence and a **metadata-only** dump inventory to `diags`: name, byte count, and UTC creation time. It neither reads dump content nor computes a hash by default.

With `DRAINCTL_INCLUDE_CRASH_DUMPS=1` set only for an approved local investigation, the task may hash and gzip-copy the newest `.dmp`. The gzip and SHA-256 sidecar stay in the protected `dumps` directory, inherit its ACL, are never copied to `diags`, and are never uploaded. Seven-day task retention applies only to those task-created `crash-dump-*.dmp.gz` files and sidecars; it never deletes WER-managed `.dmp` files.

The inventory and opt-in artifacts must not return file contents, a download URL, stack-memory fragments, or a dump path to non-admin/dashboard clients.

## 6. Diagnostic counters and logs

| Signal | Cardinality | Persistence | Meaning |
|---|---:|---|---|
| `remotefx_value_dropped` | bounded metric-name × reason | log/counter only | Invalid optional value was removed before persistence. |
| `host_offline` / `host_recovered` | bounded by host × report epoch | `host_freshness` + SSE | Freshness boundary occurred. |
| `wer_localdumps_configured` | process-wide | installer/service log | Protected local crash diagnostics setup succeeded. |
| `wer_localdumps_setup_failed` | process-wide | installer/service log | Setup was unavailable or unsafe. |
| dump inventory count | <= 3 WER dumps | on-demand local inspection | Local artifacts exist; no content exposure. |

No dump-content-derived field belongs in `metrics_raw`, audit history, report payloads, or SSE.

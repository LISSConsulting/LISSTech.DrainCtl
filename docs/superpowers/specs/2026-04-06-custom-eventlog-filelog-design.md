# Custom Event Log + File-Based Logging

**Date:** 2026-04-06
**Status:** Approved

## Problem

1. DrainCtl writes to the shared Application event log, spamming other sources.
2. No file-based log exists — when a server drops off the dashboard silently, there's nothing to diagnose why.

## Solution

Two changes: move to a dedicated Windows event log, and add a size-rotating file log with debug verbosity.

---

## 1. Custom Event Log

Move the event source registration from `Application\DrainCtl` to `DrainCtl\DrainCtl`. This gives DrainCtl its own log with independent `.evtx` file and retention settings, visible under "Applications and Services Logs > DrainCtl" in Event Viewer.

### Changes

- **`installer/LISSTech.DrainCtl.wxs`**: Change registry key path from
  `SYSTEM\CurrentControlSet\Services\EventLog\Application\DrainCtl` to
  `SYSTEM\CurrentControlSet\Services\EventLog\DrainCtl\DrainCtl`.
- **Go code**: No changes — `eventlog.Open("DrainCtl")` resolves the source regardless of which log it's registered under.
- **Message DLL, event IDs**: Unchanged.
- **Uninstall**: MSI removes the registry key. The `.evtx` file is left behind (standard Windows behavior).
- **Upgrade path**: The old `Application\DrainCtl` registry key should be removed by the installer to avoid orphaned source registration. Add a `RemoveRegistryKey` element for the old path.

---

## 2. File Log

### Package

New package `internal/filelog/` with a single exported type.

### Writer: `filelog.Writer`

```go
type Writer struct {
    mu       sync.Mutex
    path     string  // e.g. ...\drainctl.log
    maxBytes int64   // 10 MB
    keep     int     // 7
    file     *os.File
    size     int64
}

func New(path string) (*Writer, error)
func (w *Writer) Write(p []byte) (int, error)  // io.Writer — handles rotation
func (w *Writer) Close() error
```

- **Path**: `%ProgramData%\LISS Technologies\LISSTech DrainCtl\drainctl.log`
- **Max size**: 10 MB per file
- **Rotation**: When a write would exceed `maxBytes`, rotate:
  - Delete `drainctl.7.log` if it exists
  - Rename `drainctl.6.log` → `drainctl.7.log`, ..., `drainctl.log` → `drainctl.1.log`
  - Create new `drainctl.log`
- **Keep**: 7 old files (`.1.log` through `.7.log`)
- **Thread safety**: `sync.Mutex` around writes and rotation
- **Format**: Caller writes pre-formatted lines; the writer just handles I/O and rotation

### Log format

```
2026-04-06T10:29:00.000Z INF msg="service started" version=26.94.6
2026-04-06T10:29:01.000Z DBG msg="poll tick" mode=allow elapsed=312ms
2026-04-06T10:29:01.500Z DBG msg="dashboard heartbeat sent" url=https://dash:49470
```

Timestamp (UTC ISO8601 with milliseconds) + level + structured key=value fields.

### FileLogger

```go
func FileLogger(w *Writer) dc.LogFunc
```

Adapts `dc.LogFunc` to write formatted lines to the `Writer`. Adds timestamp prefix and level tag.

---

## 3. Log Level: LvlDBG

Add `LvlDBG Level = "DBG"` to the root package (`log.go`). This is below `LvlINF` — used for verbose operational detail.

---

## 4. MultiLogger

```go
func MultiLogger(eventLog, fileLog dc.LogFunc) dc.LogFunc
```

Fans out each log call to both sinks. The event log sink filters out `LvlDBG` (only INF/WRN/ERR). The file log sink writes everything.

| Level | File log | Event log |
|-------|----------|-----------|
| DBG   | yes      | no        |
| INF   | yes      | yes       |
| WRN   | yes      | yes       |
| ERR   | yes      | yes       |

---

## 5. Debug Instrumentation

Add `LvlDBG` log calls at these points (the troubleshooting value):

| Location | Message | Why |
|----------|---------|-----|
| `internal/svc/check.go` | `poll tick` with mode + elapsed | See if polls are running |
| `internal/svc/handler.go` | `dashboard heartbeat sent/failed/skipped` | Diagnose "server offline in dashboard" |
| `internal/svc/handler.go` | `config fetch attempt/success/failure` | Config sync issues |
| `internal/dashboard/sspi.go` | `sspi negotiate start/complete` | Auth troubleshooting |
| `internal/svc/handler.go` | `named pipe connect/disconnect` | CLI↔service communication |

---

## 6. Integration in RunService

```go
func RunService() error {
    elog, err := eventlog.Open(dc.ServiceName)
    // ...
    fw, err := filelog.New(dc.DefaultDataDir() + `\drainctl.log`)
    // ...
    defer fw.Close()

    log := MultiLogger(EventLogLogger(elog), FileLogger(fw))
    return svc.Run(dc.ServiceName, &drainService{log: log, elog: elog})
}
```

---

## 7. Testing

- `internal/filelog/`: unit tests for rotation (write past max, verify `.1.log`–`.7.log` exist, verify `.8.log` doesn't)
- `MultiLogger`: verify DBG goes to file but not event log
- `FileLogger`: verify format matches spec

---

## Non-Goals

- No config fields for log path, rotation size, or keep count (zero-config)
- No log level config (file always gets everything, event log always filters DBG)
- No ETW manifest (future v2 if SIEM structured fields are needed)

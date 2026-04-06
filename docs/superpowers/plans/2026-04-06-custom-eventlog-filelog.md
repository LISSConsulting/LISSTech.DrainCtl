# Custom Event Log + File-Based Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move event log to a dedicated "DrainCtl" log and add a size-rotating file log with debug verbosity for troubleshooting.

**Architecture:** New `internal/filelog` package provides a thread-safe, size-rotating file writer. A `MultiLogger` fans out log calls to both the event log (INF+) and the file log (all levels including DBG). The installer registry key moves from `EventLog\Application\DrainCtl` to `EventLog\DrainCtl\DrainCtl`.

**Tech Stack:** Go stdlib (`os`, `sync`, `fmt`, `path/filepath`), `golang.org/x/sys/windows/svc/eventlog`, WiX 5

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `log.go` | Modify | Add `LvlDBG` constant |
| `internal/filelog/writer.go` | Create | Size-rotating file writer |
| `internal/filelog/writer_test.go` | Create | Rotation + write tests |
| `internal/svc/handler.go` | Modify | `MultiLogger`, `FileLogger`, wire up in `RunService` |
| `internal/svc/check.go` | Modify | Add DBG log for poll tick |
| `internal/dashboard/sspi.go` | Modify | Add DBG log for SSPI negotiate |
| `installer/LISSTech.DrainCtl.wxs` | Modify | Move registry key, clean up old key |

---

### Task 1: Add LvlDBG constant

**Files:**
- Modify: `log.go:15-19`

- [ ] **Step 1: Add LvlDBG**

In `log.go`, add `LvlDBG` to the constants block:

```go
const (
	LvlDBG Level = "DBG"
	LvlINF Level = "INF"
	LvlWRN Level = "WRN"
	LvlERR Level = "ERR"
	LvlOK  Level = "OK "
)
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: success, no errors

- [ ] **Step 3: Commit**

```bash
git add log.go
git commit -m "feat: add LvlDBG log level constant"
```

---

### Task 2: File log writer with rotation

**Files:**
- Create: `internal/filelog/writer.go`
- Create: `internal/filelog/writer_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/filelog/writer_test.go`:

```go
//go:build windows

package filelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := New(path, 200, 3) // 200 bytes max, keep 3
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := strings.Repeat("A", 50) + "\n" // 51 bytes

	// Write 4 lines = 204 bytes → triggers rotation on the 4th write
	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// After rotation, test.log should exist (new file) and test.1.log (rotated)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("test.log should exist")
	}
	if _, err := os.Stat(path[:len(path)-4] + ".1.log"); err != nil {
		t.Fatal("test.1.log should exist after rotation")
	}
}

func TestRotationKeepsNFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := New(path, 100, 3) // 100 bytes max, keep 3
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := strings.Repeat("B", 99) + "\n" // 100 bytes — each write triggers rotation

	// Write 6 times → should create .1, .2, .3 but NOT .4
	for i := 0; i < 6; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	base := path[:len(path)-4]
	for i := 1; i <= 3; i++ {
		name := base + "." + itoa(i) + ".log"
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("%s should exist", filepath.Base(name))
		}
	}
	// .4 should NOT exist
	if _, err := os.Stat(base + ".4.log"); err == nil {
		t.Fatal("test.4.log should NOT exist (keep=3)")
	}
}

func itoa(i int) string {
	return string(rune('0' + i))
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := New(path, 1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Second close should not panic or error
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/filelog/ -v`
Expected: FAIL — package does not exist yet

- [ ] **Step 3: Write the implementation**

Create `internal/filelog/writer.go`:

```go
//go:build windows

package filelog

import (
	"fmt"
	"os"
	"sync"
)

// Writer is a thread-safe, size-rotating file writer.
// When the current log file exceeds maxBytes, it rotates:
// delete .{keep}.log, rename .{keep-1}.log → .{keep}.log, ..., .log → .1.log.
type Writer struct {
	mu       sync.Mutex
	path     string // e.g. C:\ProgramData\...\drainctl.log
	base     string // path without .log extension
	maxBytes int64
	keep     int
	file     *os.File
	size     int64
}

// New creates a Writer that rotates at maxBytes, keeping keep old files.
// The file is created (or opened for append) immediately.
func New(path string, maxBytes int64, keep int) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("filelog: open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("filelog: stat %s: %w", path, err)
	}

	// Derive base: strip ".log" suffix for rotation naming
	base := path
	if len(path) > 4 && path[len(path)-4:] == ".log" {
		base = path[:len(path)-4]
	}

	return &Writer{
		path:     path,
		base:     base,
		maxBytes: maxBytes,
		keep:     keep,
		file:     f,
		size:     info.Size(),
	}, nil
}

// Write implements io.Writer. It is safe for concurrent use.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, fmt.Errorf("filelog: closed")
	}

	// Check if rotation is needed before writing.
	if w.size+int64(len(p)) > w.maxBytes && w.size > 0 {
		if err := w.rotate(); err != nil {
			return 0, fmt.Errorf("filelog: rotate: %w", err)
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying file. Safe to call multiple times.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// rotate closes the current file, shifts old files, and opens a new one.
// Caller must hold w.mu.
func (w *Writer) rotate() error {
	_ = w.file.Close()

	// Delete the oldest if it exists.
	oldest := fmt.Sprintf("%s.%d.log", w.base, w.keep)
	_ = os.Remove(oldest)

	// Shift: .{n-1}.log → .{n}.log, ..., .1.log → .2.log
	for i := w.keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d.log", w.base, i)
		dst := fmt.Sprintf("%s.%d.log", w.base, i+1)
		_ = os.Rename(src, dst)
	}

	// Current → .1.log
	_ = os.Rename(w.path, fmt.Sprintf("%s.1.log", w.base))

	// Open new file.
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/filelog/ -v`
Expected: all 3 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/filelog/
git commit -m "feat: add filelog.Writer with size-based rotation"
```

---

### Task 3: FileLogger, MultiLogger, wire into RunService

**Files:**
- Modify: `internal/svc/handler.go:61-78` (EventLogLogger area) and `419-428` (RunService)

- [ ] **Step 1: Add FileLogger and MultiLogger**

In `internal/svc/handler.go`, add these functions after the `EventLogLogger` function:

```go
// FileLogger returns a LogFunc that writes timestamped structured lines to a
// filelog.Writer. All levels including LvlDBG are written.
func FileLogger(w io.Writer) dc.LogFunc {
	return func(l dc.Level, fields ...string) {
		ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		_, _ = fmt.Fprintf(w, "%s %s %s\n", ts, l, strings.Join(fields, " "))
	}
}

// MultiLogger fans out log calls to both an event log sink (INF+ only)
// and a file log sink (all levels including DBG).
func MultiLogger(eventLog, fileLog dc.LogFunc) dc.LogFunc {
	return func(l dc.Level, fields ...string) {
		fileLog(l, fields...)
		if l != dc.LvlDBG {
			eventLog(l, fields...)
		}
	}
}
```

Add `"io"` to the import block if not already present.

- [ ] **Step 2: Wire up in RunService**

Replace the `RunService` function:

```go
func RunService() error {
	elog, err := eventlog.Open(dc.ServiceName)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer func() { _ = elog.Close() }()

	fw, err := filelog.New(dc.DefaultDataDir()+`\drainctl.log`, 10<<20, 7) // 10 MB, 7 old files
	if err != nil {
		// File log failure is non-fatal — fall back to event log only.
		log := EventLogLogger(elog)
		log(dc.LvlWRN, fmt.Sprintf("msg=%q error=%q", "file log unavailable, using event log only", err))
		return svc.Run(dc.ServiceName, &drainService{log: log, elog: elog})
	}
	defer func() { _ = fw.Close() }()

	log := MultiLogger(EventLogLogger(elog), FileLogger(fw))
	return svc.Run(dc.ServiceName, &drainService{log: log, elog: elog})
}
```

Add `"github.com/LISSConsulting/LISSTech.DrainCtl/internal/filelog"` to imports.

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add internal/svc/handler.go
git commit -m "feat: FileLogger + MultiLogger, wire file log into RunService"
```

---

### Task 4: Add debug instrumentation

**Files:**
- Modify: `internal/svc/handler.go` (poll tick, heartbeat, config fetch, pipe)
- Modify: `internal/svc/check.go` (poll tick result)
- Modify: `internal/dashboard/sspi.go` (SSPI negotiate)

- [ ] **Step 1: Poll tick debug log in check.go**

At the start of `svcRunCheck` (line 19), add a debug log with timing:

```go
func svcRunCheck(st *store.MemAuditStore, cfg *dc.ServiceConfig, targets []dc.NotificationTarget, notifyState *dc.NotifyState, dashCfg *dc.DashboardConfig, evtSub *watcher.EventSubscriber, log dc.LogFunc, elog *eventlog.Log) {
	checkStart := time.Now()
	state, err := dc.ReadDrainMode()
```

After the registry read (after line 23 error check, and at the end of the function before the closing `}`), add:

After line 24 (`return`), just before the closing brace of the error block, no change needed.

At the very end of the function (before the final `}`), add:

```go
	log(dc.LvlDBG,
		"msg=\"poll tick\"",
		fmt.Sprintf("mode=%s", state.Mode),
		fmt.Sprintf("status=%s", status),
		fmt.Sprintf("elapsed=%s", time.Since(checkStart).Truncate(time.Microsecond)),
	)
```

- [ ] **Step 2: Dashboard heartbeat debug in handler.go**

In the poll ticker case (around line 345), add debug logging around `ReportState`:

Replace:
```go
	// Report to dashboard if configured.
	if dashCfg != nil && dashCfg.URL != "" {
		dashboard.ReportState(dashCfg.URL, result, log)
	}
```

With:
```go
	// Report to dashboard if configured.
	if dashCfg != nil && dashCfg.URL != "" {
		log(dc.LvlDBG, "msg=\"dashboard heartbeat sending\"", fmt.Sprintf("url=%s", dashCfg.URL))
		dashboard.ReportState(dashCfg.URL, result, log)
	}
```

- [ ] **Step 3: Dashboard registration debug in handler.go**

In `registerWithDashboard` (around line 394), add debug before the call:

```go
func registerWithDashboard(dashCfg *dc.DashboardConfig, log dc.LogFunc) bool {
	log(dc.LvlDBG, "msg=\"dashboard registration attempt\"", fmt.Sprintf("url=%s", dashCfg.URL))
	regResult, err := dashboard.Register(dashCfg.URL, log)
```

- [ ] **Step 4: Config fetch debug in handler.go**

In the config fetch block (around line 333), add debug before the call:

```go
			if dashCfg.URL != "" && time.Since(lastConfigFetch) >= backoffDuration(dashConfigFailures) {
				lastConfigFetch = time.Now()
				log(dc.LvlDBG, "msg=\"dashboard config fetch\"", fmt.Sprintf("url=%s", dashCfg.URL))
				if remote, err := dashboard.FetchNotifyConfig(dashCfg.URL, s.log); err != nil {
```

- [ ] **Step 5: SSPI negotiate debug in sspi.go**

In `NegotiateMiddleware`, add debug at the start of a successful auth (after `authDone` is confirmed true and username is retrieved, around line 93):

```go
		dc.LogMsg(log, dc.LvlDBG, "sspi: negotiate complete", fmt.Sprintf("user=%s", username))
```

- [ ] **Step 6: Verify build and lint**

Run: `go build ./... && just lint`
Expected: success, 0 issues

- [ ] **Step 7: Commit**

```bash
git add internal/svc/check.go internal/svc/handler.go internal/dashboard/sspi.go
git commit -m "feat: add DBG instrumentation for poll, dashboard, SSPI"
```

---

### Task 5: Move event log to custom log in installer

**Files:**
- Modify: `installer/LISSTech.DrainCtl.wxs:117-130`

- [ ] **Step 1: Update registry key and add cleanup for old path**

In `installer/LISSTech.DrainCtl.wxs`, replace the EventLogSource component:

```xml
        <!-- Event Log message file + source registration (custom "DrainCtl" log) -->
        <Component Id="EventLogSource" Directory="BinFolder"
            Guid="6d88631f-f074-4125-9399-28a349a45bea">
            <File Id="drainctl_msg.dll"
                Source="..\assets\drainctl-msg.dll"
                Name="drainctl-msg.dll"
                KeyPath="yes" />
            <RegistryKey Root="HKLM"
                Key="SYSTEM\CurrentControlSet\Services\EventLog\DrainCtl\DrainCtl">
                <RegistryValue Name="EventMessageFile" Type="string"
                    Value="[BinFolder]drainctl-msg.dll" />
                <RegistryValue Name="TypesSupported" Type="integer" Value="7" />
            </RegistryKey>
            <!-- Remove legacy Application log source on upgrade -->
            <RemoveRegistryKey Root="HKLM"
                Key="SYSTEM\CurrentControlSet\Services\EventLog\Application\DrainCtl"
                Action="removeOnInstall" />
        </Component>
```

- [ ] **Step 2: Build MSI**

Run: `just msi`
Expected: Build succeeded, 0 errors

- [ ] **Step 3: Commit**

```bash
git add installer/LISSTech.DrainCtl.wxs
git commit -m "feat: move event log to dedicated DrainCtl log, clean up old Application source"
```

---

### Task 6: Update docs

**Files:**
- Modify: `docs/guide.html` — update any Event Viewer references to mention "Applications and Services Logs > DrainCtl"
- Modify: `CLAUDE.md` — mention file log path in Architecture section

- [ ] **Step 1: Update guide.html**

Search for "Application" event log references and update to mention the custom log location.

- [ ] **Step 2: Update CLAUDE.md architecture section**

Add to the Architecture section:

```
Logging: dual-sink — Windows Event Log (custom "DrainCtl" log, INF+) + file log (%ProgramData%\...\drainctl.log, 10 MB rotate, 7 kept, all levels incl DBG).
```

- [ ] **Step 3: Commit**

```bash
git add docs/guide.html CLAUDE.md
git commit -m "docs: update event log location and file log in guide/CLAUDE.md"
```

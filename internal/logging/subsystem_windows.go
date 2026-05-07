//go:build windows

package logging

import (
	"context"
	"log/slog"
	"sync"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/filelog"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// Subsystem owns the dual-sink slog wiring: a manifest-based ETW handler
// and a daily-rotating file writer. It is constructed by RunService
// before svc.Run is invoked so that all subsequent slog output — from
// svc.Run, Execute, and every other LCI subsystem — flows through the
// configured sinks.
//
// Lifetime is the RunService scope, NOT the Execute scope: logging must
// outlive every other subsystem so shutdown messages still land. Stop is
// therefore wired into RunService's defer chain rather than the subsystem
// list iterated inside Execute. The type still satisfies lifecycle.Subsystem
// because Stop's idempotency contract is the only invariant that matters
// for the deferred-Close pattern.
//
// Start is a no-op: there are no background goroutines. ETW registration
// happens at New time (it can fail silently when the manifest hasn't been
// installed yet — handler degrades to drop-writes), and filelog rotation
// runs synchronously on the Write path.
type Subsystem struct {
	etw      *ETWHandler
	file     *filelog.Writer
	stopOnce sync.Once
}

// NewSubsystem constructs the dual-sink logging subsystem and installs
// itself as slog.Default. ETW is always present — if its provider isn't
// registered, the handler runs in degraded mode (writes are dropped).
// The file sink is best-effort: a filelog open failure logs a warning
// through ETW and the subsystem continues with ETW only.
//
// fileLevel and etwLevel are owned by the caller so live config reloads
// can adjust per-sink levels without re-constructing the subsystem.
func NewSubsystem(filePath string, keepDays int, fileLevel, etwLevel *slog.LevelVar) *Subsystem {
	s := &Subsystem{}
	s.etw = NewETWHandler(etwLevel)

	fw, err := filelog.New(filePath, keepDays)
	if err != nil {
		// File log unavailable — fall back to ETW only. Match the warning
		// the inline RunService path emitted before this subsystem existed.
		slog.SetDefault(slog.New(s.etw))
		slog.Warn("file log unavailable, using ETW only", "error", err)
		return s
	}
	s.file = fw
	fileH := NewFileHandler(fw, fileLevel)
	slog.SetDefault(slog.New(NewMultiHandler(fileH, s.etw)))
	return s
}

// Start satisfies lifecycle.Subsystem. The handlers are already live
// from New time, so there is nothing to launch here.
func (s *Subsystem) Start(_ context.Context) error { return nil }

// Stop closes the file writer and unregisters the ETW provider handle.
// Idempotent.
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.file != nil {
			_ = s.file.Close()
		}
		if s.etw != nil {
			s.etw.Close()
		}
	})
}

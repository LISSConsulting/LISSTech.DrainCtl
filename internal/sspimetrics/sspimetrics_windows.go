//go:build windows

// Package sspimetrics holds the live/pending counters for SSPI
// ServerContext objects allocated by the dashboard's NegotiateMiddleware.
//
// It is a leaf package by design: dashboard writes the counters,
// selfmetrics reads them. Putting the storage here breaks the
// dashboard → selfmetrics layering inversion that would exist if
// selfmetrics had to import dashboard purely to read these atomics.
//
// Live counts ServerContexts created via negotiate.NewServerContext
// that have NOT yet had Release() called on them. Pending counts the
// entries currently in NegotiateMiddleware's per-connKey pending map
// (multi-leg NTLM in flight). Both are operator-visible via the daily
// selfmetrics emission so a future leak in the SSPI handle path is
// caught by the same diag bundle that revealed the original
// orphan-on-Store leak.
package sspimetrics

import "sync/atomic"

// Live and Pending are package vars by design — incrementing an atomic
// counter from many call sites is a fundamentally global concern, and
// hiding it behind methods would force every caller to obtain a handle
// to the same singleton.
var (
	Live    atomic.Int64
	Pending atomic.Int64
)

// Snapshot returns the current counter values atomically (each load is
// individually atomic; the pair is not a consistent snapshot, which is
// fine — the consumer is selfmetrics emitting at a 60 s cadence).
func Snapshot() (live, pending int64) {
	return Live.Load(), Pending.Load()
}

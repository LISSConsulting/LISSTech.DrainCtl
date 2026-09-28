//go:build windows

// Package sessiondrop implements the central deterministic lower-tail session-drop
// detector. It consumes durable accepted observations in their private acceptance
// order and does not perform provider analysis.
package sessiondrop

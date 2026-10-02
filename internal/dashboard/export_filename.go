//go:build windows

package dashboard

import (
	"errors"
	"fmt"
	"strings"
)

// FilenameExportType is the typed enum used at the boundary of BuildFilename
// so callers can't pass a stray string. Mirrors ExportType but kept as a
// distinct type because BuildFilename is called from the handler with a
// route-level decision (not the snapshot).
type FilenameExportType string

const (
	FilenameFleet   FilenameExportType = "fleet"
	FilenamePerHost FilenameExportType = "per_host"
)

// ErrInvalidFilenameInput indicates BuildFilename received an input that
// could not be safely turned into a download filename (empty graph slug,
// Windows-reserved characters, etc.).
var ErrInvalidFilenameInput = errors.New("export filename: invalid input")

// BuildFilename constructs the download filename per FR-013:
//
//	drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>
//
// The graphSlug is sanitized to lowercase ASCII letters, digits, and
// hyphens. Host names (per_host only) are stripped of anything outside
// [A-Za-z0-9._-] and collapsed. The extension is appended as-is.
//
// Examples:
//
//	BuildFilename(FilenameFleet, "", "overview load") -> "drainctl-fleet-overview-load"
//	BuildFilename(FilenamePerHost, "sql-prod-01", "load") -> "drainctl-host-sql-prod-01-load"
//	BuildFilename(FilenameFleet, "", "HIC · TCP Retrans/sec") -> "drainctl-fleet-hic-tcp-retrans-sec"
//
// Returns ErrInvalidFilenameInput wrapped in a descriptive error when the
// inputs cannot yield a safe filename (empty slug after sanitization, etc.).
func BuildFilename(exportType FilenameExportType, hostName, graphLabel, ext string) (string, error) {
	if ext == "" {
		return "", fmt.Errorf("%w: extension is required", ErrInvalidFilenameInput)
	}
	if graphLabel == "" {
		return "", fmt.Errorf("%w: graph label is empty", ErrInvalidFilenameInput)
	}

	var b strings.Builder
	b.WriteString("drainctl-")
	b.WriteString(string(exportType))

	if exportType == FilenamePerHost {
		if hostName == "" {
			return "", fmt.Errorf("%w: per-host export requires host_name", ErrInvalidFilenameInput)
		}
		cleanHost := sanitizeHostName(hostName)
		if cleanHost == "" {
			return "", fmt.Errorf("%w: host_name has no safe characters", ErrInvalidFilenameInput)
		}
		b.WriteByte('-')
		b.WriteString(cleanHost)
	} else if exportType != FilenameFleet {
		return "", fmt.Errorf("%w: unknown export type %q", ErrInvalidFilenameInput, exportType)
	}

	slug := sanitizeGraphSlug(graphLabel)
	if slug == "" {
		return "", fmt.Errorf("%w: graph slug is empty after sanitization", ErrInvalidFilenameInput)
	}
	b.WriteByte('-')
	b.WriteString(slug)

	b.WriteByte('.')
	b.WriteString(ext)
	return b.String(), nil
}

// sanitizeGraphSlug converts a graph label to a filename-safe slug: ASCII
// letters/digits/hyphens, lowercase, with runs of non-allowed characters
// collapsed to a single hyphen and leading/trailing hyphens stripped.
func sanitizeGraphSlug(label string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevHyphen = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			prevHyphen = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := b.String()
	out = strings.Trim(out, "-")
	return out
}

// sanitizeHostName strips anything outside [A-Za-z0-9._-] and collapses runs
// to a single hyphen. Windows reserved characters (<>:"/\\|?*) and control
// codes are removed by construction. The slash direction matches the input;
// only safe ones (forward and back dot, hyphen, underscore) are preserved.
func sanitizeHostName(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := b.String()
	out = strings.Trim(out, "-._")
	return out
}

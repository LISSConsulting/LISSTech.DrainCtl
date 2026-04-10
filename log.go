//go:build windows

package drainctl

import (
	"io"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"
)

// PrintResult writes a result line to w with format: --- <msg> [field ...]\n
// Used by CLI commands to emit final status, distinct from log output.
// This is a convenience re-export of internal/logging.PrintResult.
func PrintResult(w io.Writer, msg string, fields ...string) {
	logging.PrintResult(w, msg, fields...)
}

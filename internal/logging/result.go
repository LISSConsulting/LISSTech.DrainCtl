//go:build windows

package logging

import (
	"fmt"
	"io"
	"strings"
)

// PrintResult writes a result line to w with format: --- <msg> [field ...]\n
// The result line has no timestamp and no level tag, making it visually
// distinct from log output. Optional fields are shown in brackets.
func PrintResult(w io.Writer, msg string, fields ...string) {
	if len(fields) == 0 {
		_, _ = fmt.Fprintf(w, "--- %s\n", msg)
		return
	}
	_, _ = fmt.Fprintf(w, "--- %s [%s]\n", msg, strings.Join(fields, " "))
}

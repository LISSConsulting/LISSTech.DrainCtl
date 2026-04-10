//go:build windows

package drainctl

import (
	"bytes"
	"strings"
	"testing"
)

// ── PrintResult ───────────────────────────────────────────────────────────────

func TestPrintResult_ContainsDashes(t *testing.T) {
	var buf bytes.Buffer
	PrintResult(&buf, "ok")
	if !strings.Contains(buf.String(), "---") {
		t.Errorf("PrintResult output %q does not contain '---'", buf.String())
	}
}

func TestPrintResult_ContainsMessage(t *testing.T) {
	var buf bytes.Buffer
	PrintResult(&buf, "configure=done")
	if !strings.Contains(buf.String(), "configure=done") {
		t.Errorf("PrintResult output %q does not contain message", buf.String())
	}
}

func TestPrintResult_WithFields(t *testing.T) {
	var buf bytes.Buffer
	PrintResult(&buf, "status=ok", "host=srv1", "mode=drain")
	out := buf.String()
	if !strings.Contains(out, "host=srv1") {
		t.Errorf("PrintResult output %q does not contain field 'host=srv1'", out)
	}
	if !strings.Contains(out, "mode=drain") {
		t.Errorf("PrintResult output %q does not contain field 'mode=drain'", out)
	}
}

func TestPrintResult_EndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	PrintResult(&buf, "done")
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("PrintResult output %q does not end with newline", buf.String())
	}
}

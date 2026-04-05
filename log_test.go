//go:build windows

package drainctl

import (
	"strings"
	"testing"
)

// ── DefaultLogger ─────────────────────────────────────────────────────────────

func TestDefaultLogger_WritesTimestampedLine(t *testing.T) {
	var sb strings.Builder
	log := DefaultLogger(&sb, false)
	log(LvlINF, "hello=world")
	out := sb.String()
	if !strings.Contains(out, "[INF]") {
		t.Errorf("output %q does not contain '[INF]'", out)
	}
	if !strings.Contains(out, "hello=world") {
		t.Errorf("output %q does not contain field 'hello=world'", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output %q does not end with newline", out)
	}
}

func TestDefaultLogger_AllLevels(t *testing.T) {
	cases := []struct {
		level Level
		tag   string
	}{
		{LvlINF, "[INF]"},
		{LvlWRN, "[WRN]"},
		{LvlERR, "[ERR]"},
		{LvlOK, "[OK ]"},
	}
	for _, tc := range cases {
		var sb strings.Builder
		log := DefaultLogger(&sb, false)
		log(tc.level, "x=1")
		out := sb.String()
		if !strings.Contains(out, tc.tag) {
			t.Errorf("level %s: output %q does not contain %q", tc.level, out, tc.tag)
		}
	}
}

func TestDefaultLogger_QuietSuppressesINFAndWRN(t *testing.T) {
	var sb strings.Builder
	log := DefaultLogger(&sb, true)
	log(LvlINF, "should=suppress")
	log(LvlWRN, "should=suppress")
	if sb.Len() != 0 {
		t.Errorf("quiet logger wrote %q, want empty", sb.String())
	}
}

func TestDefaultLogger_QuietAllowsOKAndERR(t *testing.T) {
	var sb strings.Builder
	log := DefaultLogger(&sb, true)
	log(LvlOK, "ok=pass")
	log(LvlERR, "err=fail")
	out := sb.String()
	if !strings.Contains(out, "ok=pass") {
		t.Errorf("quiet logger suppressed LvlOK, output = %q", out)
	}
	if !strings.Contains(out, "err=fail") {
		t.Errorf("quiet logger suppressed LvlERR, output = %q", out)
	}
}

func TestDefaultLogger_MultipleFields(t *testing.T) {
	var sb strings.Builder
	log := DefaultLogger(&sb, false)
	log(LvlINF, "a=1", "b=2", "c=3")
	out := sb.String()
	if !strings.Contains(out, "a=1 b=2 c=3") {
		t.Errorf("output %q does not contain space-joined fields", out)
	}
}

// ── DiscardLogger ─────────────────────────────────────────────────────────────

func TestDiscardLogger_DoesNotPanic(t *testing.T) {
	log := DiscardLogger()
	log(LvlINF, "should=discard")
	log(LvlERR, "should=discard", "extra=field")
}

// ── LogMsg ────────────────────────────────────────────────────────────────────

func TestLogMsg_FormatsMsgAsQuotedField(t *testing.T) {
	var sb strings.Builder
	log := DefaultLogger(&sb, false)
	LogMsg(log, LvlINF, "something happened")
	out := sb.String()
	if !strings.Contains(out, `msg="something happened"`) {
		t.Errorf("output %q does not contain quoted msg field", out)
	}
}

func TestLogMsg_IncludesExtraKVFields(t *testing.T) {
	var sb strings.Builder
	log := DefaultLogger(&sb, false)
	LogMsg(log, LvlWRN, "bad config", `key="value"`, "count=3")
	out := sb.String()
	if !strings.Contains(out, `key="value"`) {
		t.Errorf("output %q missing extra kv field", out)
	}
	if !strings.Contains(out, "count=3") {
		t.Errorf("output %q missing count field", out)
	}
}

func TestLogMsg_NoExtraFields(t *testing.T) {
	var sb strings.Builder
	log := DefaultLogger(&sb, false)
	LogMsg(log, LvlERR, "fatal error")
	out := sb.String()
	if !strings.Contains(out, `msg="fatal error"`) {
		t.Errorf("output %q does not contain msg field", out)
	}
}

// TestLogMsg_NilLogIsNoOp verifies that LogMsg with a nil LogFunc does not
// panic — nil is the documented "discard" sentinel throughout the package.
func TestLogMsg_NilLogIsNoOp(t *testing.T) {
	// Must not panic.
	LogMsg(nil, LvlINF, "this should be silently ignored", "key=value")
}

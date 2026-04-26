//go:build windows

package logging

import (
	"log/slog"
	"testing"
)

func TestETWHandlerClose(t *testing.T) {
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelInfo)
	h := NewETWHandler(lv)

	h.Close()
	if got := h.regHandle.Load(); got != 0 {
		t.Fatalf("regHandle after Close = %d, want 0", got)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Close panicked: %v", r)
		}
	}()
	h.Close()
}

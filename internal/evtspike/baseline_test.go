//go:build windows

package evtspike

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func captureSlog(t *testing.T) *capturingHandler {
	t.Helper()
	h := &capturingHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return h
}

func hasWarnWithEvtspike(h *capturingHandler, value string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != slog.LevelWarn {
			continue
		}
		var match bool
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "evtspike" && a.Value.String() == value {
				match = true
				return false
			}
			return true
		})
		if match {
			return true
		}
	}
	return false
}

func makeSampleBaseline() *BaselineFile {
	cs := ChannelState{
		Global:    GammaState{Alpha: 6.0, Beta: 60.0, N: 100},
		LastAlert: time.Date(2026, 4, 17, 10, 30, 0, 0, time.UTC),
	}
	cs.Slots[0] = GammaState{Alpha: 1.5, Beta: 10.0, N: 7}
	cs.Slots[95] = GammaState{Alpha: 2.5, Beta: 20.0, N: 9}
	return &BaselineFile{
		SchemaVersion: SchemaVersion,
		WrittenAt:     time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC),
		Host:          "test-host",
		Channels: map[string]ChannelState{
			"Application": cs,
		},
	}
}

func glob(t *testing.T, baselinePath, suffixPattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(baselinePath + suffixPattern)
	if err != nil {
		t.Fatalf("glob %q: %v", suffixPattern, err)
	}
	return matches
}

func TestWriteLoadBaseline_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evtspike-baseline.json")

	want := makeSampleBaseline()
	if err := WriteBaseline(path, want); err != nil {
		t.Fatalf("WriteBaseline: %v", err)
	}

	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got  = %#v\n want = %#v", got, want)
	}
}

func TestLoadBaseline_MissingFileReturnsFreshNoBak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	bf, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if bf == nil || bf.SchemaVersion != SchemaVersion {
		t.Fatalf("expected fresh baseline, got %#v", bf)
	}
	if len(bf.Channels) != 0 {
		t.Errorf("expected empty channels, got %d", len(bf.Channels))
	}

	if matches := glob(t, path, ".*.bak"); len(matches) > 0 {
		t.Errorf("expected no .bak file for missing-file case, found %v", matches)
	}
}

func TestLoadBaseline_UnreadableFileReturnsFreshNoBak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.json")

	original := []byte(`{"schema_version":1,"host":"before","channels":{}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	pathW, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("utf16: %v", err)
	}
	// share=0 forces a sharing violation in os.ReadFile; surfaces as an
	// error that is NOT fs.ErrNotExist, exercising the "unreadable" branch.
	h, err := windows.CreateFile(
		pathW,
		windows.GENERIC_READ,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("CreateFile (exclusive): %v", err)
	}

	bf, loadErr := LoadBaseline(path)
	_ = windows.CloseHandle(h)

	if loadErr != nil {
		t.Fatalf("LoadBaseline: %v", loadErr)
	}
	if bf == nil || bf.SchemaVersion != SchemaVersion || len(bf.Channels) != 0 {
		t.Fatalf("expected fresh baseline, got %#v", bf)
	}

	if matches := glob(t, path, ".*.bak"); len(matches) > 0 {
		t.Errorf("expected no .bak file for unreadable-file case, found %v", matches)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after unlock: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("original file contents changed\n before = %q\n after  = %q", original, after)
	}
}

func TestLoadBaseline_CorruptJSONRenamesAndRebuilds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")

	if err := os.WriteFile(path, []byte(`{not valid json`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	bf, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if bf == nil || bf.SchemaVersion != SchemaVersion || len(bf.Channels) != 0 {
		t.Fatalf("expected fresh baseline, got %#v", bf)
	}

	corruptMatches := glob(t, path, ".corrupt-*.bak")
	if len(corruptMatches) != 1 {
		t.Fatalf("expected one .corrupt-*.bak, got %v", corruptMatches)
	}
	if incompatMatches := glob(t, path, ".incompat-*.bak"); len(incompatMatches) != 0 {
		t.Errorf("did not expect .incompat-*.bak, got %v", incompatMatches)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected original path to be gone after rename, got err=%v", err)
	}
}

func TestLoadBaseline_IncompatibleVersionRenamesAndRebuilds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "futurever.json")

	payload, err := json.Marshal(map[string]any{
		"schema_version": SchemaVersion + 98,
		"host":           "future",
		"channels":       map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	bf, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if bf == nil || bf.SchemaVersion != SchemaVersion || len(bf.Channels) != 0 {
		t.Fatalf("expected fresh baseline, got %#v", bf)
	}

	incompatMatches := glob(t, path, ".incompat-*.bak")
	if len(incompatMatches) != 1 {
		t.Fatalf("expected one .incompat-*.bak, got %v", incompatMatches)
	}
	if corruptMatches := glob(t, path, ".corrupt-*.bak"); len(corruptMatches) != 0 {
		t.Errorf("did not expect .corrupt-*.bak, got %v", corruptMatches)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected original path to be gone after rename, got err=%v", err)
	}
}

// TestLoadBaseline_SchemaVersion99WarnsAndRenames covers T061 / SC-005:
// a baseline file from a future schema must not be silently misinterpreted —
// LoadBaseline must rebuild fresh, archive the original with a timestamped
// .incompat-*.bak suffix, and emit a slog warning so operators can see the
// downgrade in logs.
func TestLoadBaseline_SchemaVersion99WarnsAndRenames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v99.json")

	payload, err := json.Marshal(map[string]any{
		"schema_version": 99,
		"host":           "future",
		"channels":       map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := captureSlog(t)

	bf, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if bf == nil || bf.SchemaVersion != SchemaVersion || len(bf.Channels) != 0 {
		t.Fatalf("expected fresh baseline, got %#v", bf)
	}

	matches := glob(t, path, ".incompat-*.bak")
	if len(matches) != 1 {
		t.Fatalf("expected one .incompat-*.bak, got %v", matches)
	}
	if !regexp.MustCompile(`\.incompat-\d{8}-\d{6}\.bak$`).MatchString(matches[0]) {
		t.Errorf("rename suffix shape mismatch: %s", matches[0])
	}
	if !hasWarnWithEvtspike(h, "baseline_incompat") {
		t.Errorf("expected slog warning evtspike=baseline_incompat, got %d records", len(h.records))
	}
}

// TestLoadBaseline_TruncatedFileWarnsAndRenames covers T062: a baseline file
// truncated mid-write (crash, antivirus quarantine, disk-full) must surface as
// the corrupt-JSON branch — rebuild fresh, rename to .corrupt-*.bak, warn.
func TestLoadBaseline_TruncatedFileWarnsAndRenames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.json")

	if err := WriteBaseline(path, makeSampleBaseline()); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if len(full) < 4 {
		t.Fatalf("seed file unexpectedly small: %d bytes", len(full))
	}
	if err := os.WriteFile(path, full[:len(full)/2], 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	h := captureSlog(t)

	bf, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if bf == nil || bf.SchemaVersion != SchemaVersion || len(bf.Channels) != 0 {
		t.Fatalf("expected fresh baseline, got %#v", bf)
	}

	matches := glob(t, path, ".corrupt-*.bak")
	if len(matches) != 1 {
		t.Fatalf("expected one .corrupt-*.bak, got %v", matches)
	}
	if !regexp.MustCompile(`\.corrupt-\d{8}-\d{6}\.bak$`).MatchString(matches[0]) {
		t.Errorf("rename suffix shape mismatch: %s", matches[0])
	}
	if !hasWarnWithEvtspike(h, "baseline_corrupt") {
		t.Errorf("expected slog warning evtspike=baseline_corrupt, got %d records", len(h.records))
	}
}

// TestLoadBaseline_OversizeFileIsRenamed — T111. A 17 MB file exceeds the
// 16 MB cap; LoadBaseline must refuse, rename .oversize-*.bak, and return
// fresh baseline. Without the cap a tampered file could pressure memory.
func TestLoadBaseline_OversizeFileIsRenamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oversize.json")
	// 17 MB of garbage — just beyond the 16 MB cap.
	payload := bytes.Repeat([]byte("x"), 17<<20)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &capturingHandler{}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	bf, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if bf.SchemaVersion != SchemaVersion || len(bf.Channels) != 0 {
		t.Errorf("oversize did not return fresh baseline: %+v", bf)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.oversize-*.bak"))
	if len(matches) != 1 {
		t.Fatalf("expected one .oversize-*.bak, got %v", matches)
	}
	if !hasWarnWithEvtspike(h, "baseline_oversize") {
		t.Errorf("expected slog warning evtspike=baseline_oversize")
	}
}

// TestLoadBaseline_ZeroSchemaVersionIsRenamed — T111. Strict equality: a
// zero SchemaVersion (missing in JSON or explicitly 0) is rejected.
func TestLoadBaseline_ZeroSchemaVersionIsRenamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zero.json")
	// Valid JSON, explicitly zero schema.
	bf := &BaselineFile{SchemaVersion: 0, Channels: map[string]ChannelState{}}
	data, _ := json.Marshal(bf)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &capturingHandler{}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("zero schema was accepted: got schema=%d", got.SchemaVersion)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.incompat-*.bak"))
	if len(matches) != 1 {
		t.Fatalf("expected one .incompat-*.bak, got %v", matches)
	}
	if !hasWarnWithEvtspike(h, "baseline_incompat") {
		t.Errorf("expected slog warning evtspike=baseline_incompat")
	}
}

// TestLoadBaseline_NegativeSchemaVersionIsRenamed — T111. Strict equality
// also rejects negative schema values. A crafted -1 must land in
// .incompat-*.bak just like zero or a future version.
func TestLoadBaseline_NegativeSchemaVersionIsRenamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "negative.json")
	bf := &BaselineFile{SchemaVersion: -1, Channels: map[string]ChannelState{}}
	data, _ := json.Marshal(bf)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &capturingHandler{}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("negative schema was accepted: got schema=%d", got.SchemaVersion)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.incompat-*.bak"))
	if len(matches) != 1 {
		t.Fatalf("expected one .incompat-*.bak, got %v", matches)
	}
	if !hasWarnWithEvtspike(h, "baseline_incompat") {
		t.Errorf("expected slog warning evtspike=baseline_incompat")
	}
}

// TestLoadBaseline_ClampsOutOfBandFloats — T111. Negative Alpha/Beta and
// oversize N values must be clamped on load with a single WARN.
func TestLoadBaseline_ClampsOutOfBandFloats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clamp.json")

	bf := &BaselineFile{
		SchemaVersion: SchemaVersion,
		WrittenAt:     time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC),
		Host:          "test",
		Channels: map[string]ChannelState{
			"Application": {
				Slots:  [96]GammaState{0: {Alpha: -5, Beta: 1e20, N: -3}},
				Global: GammaState{Alpha: 1e20, Beta: -0.5, N: 1_000_000_001},
			},
		},
	}
	data, _ := json.Marshal(bf)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &capturingHandler{}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	cs := got.Channels["Application"]
	if cs.Slots[0].Alpha != 0 || cs.Slots[0].N != 0 {
		t.Errorf("slot[0] negative values not clamped to 0: %+v", cs.Slots[0])
	}
	if cs.Slots[0].Beta != gammaStateMaxAlphaBeta {
		t.Errorf("slot[0] oversize Beta not clamped: got %v want %v", cs.Slots[0].Beta, gammaStateMaxAlphaBeta)
	}
	if cs.Global.Alpha != gammaStateMaxAlphaBeta || cs.Global.Beta != 0 || cs.Global.N != gammaStateMaxN {
		t.Errorf("global out-of-band not clamped: %+v", cs.Global)
	}
	if !hasWarnWithEvtspike(h, "baseline_clamped") {
		t.Errorf("expected slog warning evtspike=baseline_clamped")
	}
}

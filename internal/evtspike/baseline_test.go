//go:build windows

package evtspike

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

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

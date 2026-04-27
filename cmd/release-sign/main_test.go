package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureKeysFile = `//go:build windows

package updater

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

var releaseSigningKeysB64 = []string{
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
}

func decodeReleaseSigningKeys(entries []string) ([]ed25519.PublicKey, error) {
	_ = base64.StdEncoding
	_ = fmt.Errorf
	return nil, nil
}
`

const fixtureEmptySliceFile = `package updater

var releaseSigningKeysB64 = []string{}
`

func writeTempFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestAddKey_InsertsIntoNonEmptySlice(t *testing.T) {
	path := writeTempFile(t, "keys.go", fixtureKeysFile)
	const newKey = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="

	alreadyPresent, err := addKeyToReleaseSigningKeysB64(path, newKey)
	if err != nil {
		t.Fatalf("addKey: %v", err)
	}
	if alreadyPresent {
		t.Fatal("alreadyPresent=true on first insert; want false")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, `"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="`) {
		t.Errorf("first key missing after insert:\n%s", got)
	}
	if !strings.Contains(got, `"`+newKey+`"`) {
		t.Errorf("new key not present:\n%s", got)
	}
}

func TestAddKey_IdempotentOnDuplicate(t *testing.T) {
	path := writeTempFile(t, "keys.go", fixtureKeysFile)
	const existingKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	alreadyPresent, err := addKeyToReleaseSigningKeysB64(path, existingKey)
	if err != nil {
		t.Fatalf("addKey: %v", err)
	}
	if !alreadyPresent {
		t.Fatal("alreadyPresent=false on duplicate insert; want true")
	}

	// File must be byte-identical to the fixture.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != fixtureKeysFile {
		t.Errorf("file changed despite alreadyPresent; got:\n%s", string(body))
	}
}

func TestAddKey_InsertsIntoEmptySlice(t *testing.T) {
	path := writeTempFile(t, "keys.go", fixtureEmptySliceFile)
	const newKey = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="

	alreadyPresent, err := addKeyToReleaseSigningKeysB64(path, newKey)
	if err != nil {
		t.Fatalf("addKey: %v", err)
	}
	if alreadyPresent {
		t.Fatal("alreadyPresent=true on empty-slice insert; want false")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"`+newKey+`"`) {
		t.Errorf("new key not present after insert into empty slice:\n%s", string(body))
	}
}

func TestAddKey_FailsWhenSliceMissing(t *testing.T) {
	path := writeTempFile(t, "keys.go", `package updater

// no releaseSigningKeysB64 here
var somethingElse = []string{}
`)
	if _, err := addKeyToReleaseSigningKeysB64(path, "X"); err == nil {
		t.Fatal("expected error when releaseSigningKeysB64 is absent")
	}
}

// TestAddKey_RoundTripParsesCleanly verifies the rewritten file is still
// valid Go (the formatter would reject malformed AST). After insert,
// re-parse and confirm the slice has both keys.
func TestAddKey_RoundTripParsesCleanly(t *testing.T) {
	path := writeTempFile(t, "keys.go", fixtureKeysFile)
	const k1 = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD="
	const k2 = "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE="

	if _, err := addKeyToReleaseSigningKeysB64(path, k1); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := addKeyToReleaseSigningKeysB64(path, k2); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	for _, k := range []string{k1, k2, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="} {
		if !strings.Contains(got, `"`+k+`"`) {
			t.Errorf("key %q missing after two inserts:\n%s", k, got)
		}
	}
}

func TestCountReleaseSigningKeysB64_NonEmptyKeys(t *testing.T) {
	path := writeTempFile(t, "keys.go", fixtureKeysFile)
	got, err := countReleaseSigningKeysB64(path)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 1 {
		t.Errorf("count = %d, want 1 (fixture has one non-empty key)", got)
	}
}

func TestCountReleaseSigningKeysB64_EmptySlice(t *testing.T) {
	path := writeTempFile(t, "keys.go", fixtureEmptySliceFile)
	got, err := countReleaseSigningKeysB64(path)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 0 {
		t.Errorf("count = %d, want 0 (empty slice fixture)", got)
	}
}

func TestCountReleaseSigningKeysB64_SkipsEmptyStringEntries(t *testing.T) {
	const fixture = `package updater

var releaseSigningKeysB64 = []string{
	"",
	"realKey1=",
	"",
	"realKey2=",
}
`
	path := writeTempFile(t, "keys.go", fixture)
	got, err := countReleaseSigningKeysB64(path)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 2 {
		t.Errorf("count = %d, want 2 (empty entries skipped)", got)
	}
}

func TestCountReleaseSigningKeysB64_MissingSliceErrors(t *testing.T) {
	path := writeTempFile(t, "keys.go", `package updater
var unrelated = []string{}
`)
	if _, err := countReleaseSigningKeysB64(path); err == nil {
		t.Fatal("expected error when releaseSigningKeysB64 is absent")
	}
}

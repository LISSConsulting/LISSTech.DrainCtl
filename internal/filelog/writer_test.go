//go:build windows

package filelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	w, err := New(path, 200, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	line := strings.Repeat("A", 50) + "\n"
	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("test.log should exist")
	}
	if _, err := os.Stat(path[:len(path)-4] + ".1.log"); err != nil {
		t.Fatal("test.1.log should exist after rotation")
	}
}

func TestRotationKeepsNFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	w, err := New(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	line := strings.Repeat("B", 99) + "\n"
	for i := 0; i < 6; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	base := path[:len(path)-4]
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("%s.%d.log", base, i)
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("%s should exist", filepath.Base(name))
		}
	}
	if _, err := os.Stat(fmt.Sprintf("%s.4.log", base)); err == nil {
		t.Fatal("test.4.log should NOT exist (keep=3)")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	w, err := New(path, 1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

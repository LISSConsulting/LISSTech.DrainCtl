//go:build windows

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeSyscall matches the LazyProc.Call signature so the WinVerifyTrust
// fakes can be assigned to `winVerifyTrust` directly.
type fakeSyscall = func(args ...uintptr) (r1, r2 uintptr, err error)

// installSeams swaps the verifier's test seams for fakes and registers a
// cleanup that restores the originals. Any field left zero retains the
// production implementation.
type seamFakes struct {
	verify  fakeSyscall
	open    func(*uint16) (uintptr, uintptr, error)
	read    func(uintptr) (string, error)
	closeFn func(uintptr, uintptr)
}

func installSeams(t *testing.T, f seamFakes) {
	t.Helper()
	origVerify := winVerifyTrust
	origOpen := openSignerStore
	origRead := readSubjectCN
	origClose := closeSignerStore
	t.Cleanup(func() {
		winVerifyTrust = origVerify
		openSignerStore = origOpen
		readSubjectCN = origRead
		closeSignerStore = origClose
	})
	if f.verify != nil {
		winVerifyTrust = f.verify
	}
	if f.open != nil {
		openSignerStore = f.open
	}
	if f.read != nil {
		readSubjectCN = f.read
	}
	if f.closeFn != nil {
		closeSignerStore = f.closeFn
	}
}

// touchFile creates a zero-byte file the verifier can os.Stat.
func touchFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "fake.msi")
	if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

// happyOpenFake satisfies the openSignerStore seam with sentinel handles.
func happyOpenFake(_ *uint16) (uintptr, uintptr, error) {
	return 0xDEADBEEF, 0xC0FFEE, nil
}

// noopCloseFake is a closeSignerStore fake that just records nothing.
func noopCloseFake(_, _ uintptr) {}

func TestVerify_NoSignature(t *testing.T) {
	path := touchFile(t)
	installSeams(t, seamFakes{
		verify: func(_ ...uintptr) (uintptr, uintptr, error) {
			return uintptr(uint32(trustENosignature)), 0, nil
		},
	})
	err := verifyAuthenticode(path)
	if !errors.Is(err, errSignatureMissing) {
		t.Fatalf("verifyAuthenticode err = %v, want errSignatureMissing", err)
	}
}

func TestVerify_TrustFailed(t *testing.T) {
	path := touchFile(t)
	installSeams(t, seamFakes{
		verify: func(_ ...uintptr) (uintptr, uintptr, error) {
			return uintptr(uint32(0x80000000)), 0, nil
		},
	})
	err := verifyAuthenticode(path)
	if !errors.Is(err, errSignatureUntrusted) {
		t.Fatalf("verifyAuthenticode err = %v, want errSignatureUntrusted", err)
	}
	// The missing-signature sentinel must NOT match a generic trust failure
	// — the caller's branch logic depends on this distinction.
	if errors.Is(err, errSignatureMissing) {
		t.Errorf("verifyAuthenticode also matched errSignatureMissing — sentinels overlap")
	}
}

func TestVerify_SubjectMismatch(t *testing.T) {
	path := touchFile(t)
	installSeams(t, seamFakes{
		verify:  func(_ ...uintptr) (uintptr, uintptr, error) { return 0, 0, nil },
		open:    happyOpenFake,
		read:    func(_ uintptr) (string, error) { return "Acme Corp", nil },
		closeFn: noopCloseFake,
	})
	err := verifyAuthenticode(path)
	if !errors.Is(err, errSignatureSubjectMismatch) {
		t.Fatalf("verifyAuthenticode err = %v, want errSignatureSubjectMismatch", err)
	}
}

func TestVerify_Success(t *testing.T) {
	path := touchFile(t)
	installSeams(t, seamFakes{
		verify:  func(_ ...uintptr) (uintptr, uintptr, error) { return 0, 0, nil },
		open:    happyOpenFake,
		read:    func(_ uintptr) (string, error) { return expectedSubjectCN, nil },
		closeFn: noopCloseFake,
	})
	if err := verifyAuthenticode(path); err != nil {
		t.Fatalf("verifyAuthenticode err = %v, want nil", err)
	}
}

func TestVerify_EmptyPath(t *testing.T) {
	// Empty path short-circuits before any syscall, so no fakes are needed.
	err := verifyAuthenticode("")
	if !errors.Is(err, errSignatureMissing) {
		t.Fatalf("verifyAuthenticode(\"\") = %v, want errSignatureMissing", err)
	}
}

func TestVerify_MissingFile(t *testing.T) {
	// A path that doesn't exist should error out before any syscall too.
	err := verifyAuthenticode(filepath.Join(t.TempDir(), "does-not-exist.msi"))
	if err == nil {
		t.Fatal("verifyAuthenticode on missing file returned nil, want error")
	}
}

//go:build windows

package dashboard

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// isElevated returns true when the current process token has the Administrators
// group enabled — i.e. it is running in an elevated context.
// writeRestrictedFile uses icacls to restrict the key file to SYSTEM +
// Administrators; a non-elevated process cannot re-read the file afterwards.
func isElevated() bool {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer func() { _ = tok.Close() }()
	return tok.IsElevated()
}

// requireElevatedOrSkip skips the test when the process is not running elevated.
func requireElevatedOrSkip(t *testing.T) {
	t.Helper()
	if !isElevated() {
		t.Skip("requires elevated administrator privileges (writeRestrictedFile restricts key to SYSTEM+Admins)")
	}
}

// ── generateSelfSigned ────────────────────────────────────────────────────────

// TestGenerateSelfSigned_CreatesValidCert verifies the happy path: cert + key
// are written to disk and the returned tls.Certificate parses cleanly.
func TestGenerateSelfSigned_CreatesValidCert(t *testing.T) {
	requireElevatedOrSkip(t)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "test.crt")
	keyPath := filepath.Join(dir, "test.key")

	cert, err := generateSelfSigned(certPath, keyPath, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("generateSelfSigned: %v", err)
	}

	if len(cert.Certificate) == 0 {
		t.Fatal("returned tls.Certificate has no DER blocks")
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse returned cert: %v", err)
	}
	if leaf.NotAfter.IsZero() {
		t.Error("cert NotAfter is zero")
	}
}

// TestGenerateSelfSigned_CertContainsDNSNames verifies the generated cert
// includes "localhost" in its SAN list.
func TestGenerateSelfSigned_CertContainsDNSNames(t *testing.T) {
	requireElevatedOrSkip(t)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "test.crt")
	keyPath := filepath.Join(dir, "test.key")

	cert, err := generateSelfSigned(certPath, keyPath, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("generateSelfSigned: %v", err)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	hasLocalhost := false
	for _, name := range leaf.DNSNames {
		if name == "localhost" {
			hasLocalhost = true
			break
		}
	}
	if !hasLocalhost {
		t.Errorf("cert DNSNames %v does not include 'localhost'", leaf.DNSNames)
	}
}

// TestGenerateSelfSigned_CertWriteError verifies that generateSelfSigned returns
// a descriptive error when the cert file path is blocked by a directory.
// This does not require elevation — it fails before reaching writeRestrictedFile.
func TestGenerateSelfSigned_CertWriteError(t *testing.T) {
	dir := t.TempDir()
	// Place a directory where the cert file should be created.
	certPath := filepath.Join(dir, "blocked-cert")
	if err := os.MkdirAll(certPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	keyPath := filepath.Join(dir, "test.key")

	_, err := generateSelfSigned(certPath, keyPath, dc.DiscardLogger())
	if err == nil {
		t.Fatal("expected error when cert path is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "write cert file") {
		t.Errorf("error = %q, want 'write cert file' in message", err.Error())
	}
}

// ── CertFingerprint ───────────────────────────────────────────────────────────

// TestCertFingerprint_MatchesGeneratedCert verifies that CertFingerprint returns
// the SHA-256 fingerprint of the cert written by generateSelfSigned.
func TestCertFingerprint_MatchesGeneratedCert(t *testing.T) {
	requireElevatedOrSkip(t)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "dashboard-tls.crt")
	keyPath := filepath.Join(dir, "dashboard-tls.key")

	cert, err := generateSelfSigned(certPath, keyPath, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("generateSelfSigned: %v", err)
	}

	fp, err := CertFingerprint(dir)
	if err != nil {
		t.Fatalf("CertFingerprint: %v", err)
	}

	if len(fp) != 64 {
		t.Errorf("fingerprint length = %d, want 64 hex chars", len(fp))
	}

	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	if expected := certFingerprint(leaf); fp != expected {
		t.Errorf("CertFingerprint = %q, want %q", fp, expected)
	}
}

// TestCertFingerprint_NoFile verifies that CertFingerprint returns an error
// when the expected cert file does not exist.
func TestCertFingerprint_NoFile(t *testing.T) {
	_, err := CertFingerprint(t.TempDir())
	if err == nil {
		t.Fatal("expected error when cert file is absent, got nil")
	}
}

// TestCertFingerprint_InvalidPEM verifies CertFingerprint returns an error
// when the cert file contains no valid PEM block.
func TestCertFingerprint_InvalidPEM(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "dashboard-tls.crt")
	if err := os.WriteFile(certPath, []byte("not a pem file"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := CertFingerprint(dir)
	if err == nil {
		t.Fatal("expected error for invalid PEM, got nil")
	}
	if !strings.Contains(err.Error(), "no PEM block") {
		t.Errorf("error = %q, want 'no PEM block' in message", err.Error())
	}
}

// TestCertFingerprint_InvalidDER verifies CertFingerprint returns an error
// when the PEM block contains invalid DER bytes (not a valid certificate).
func TestCertFingerprint_InvalidDER(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "dashboard-tls.crt")
	// Write a PEM block with garbage DER content.
	pem := "-----BEGIN CERTIFICATE-----\nYWJj\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(certPath, []byte(pem), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := CertFingerprint(dir)
	if err == nil {
		t.Fatal("expected error for invalid DER, got nil")
	}
	if !strings.Contains(err.Error(), "parse cert") {
		t.Errorf("error = %q, want 'parse cert' in message", err.Error())
	}
}

// ── loadOrGenerateTLS ─────────────────────────────────────────────────────────

// TestLoadOrGenerateTLS_GeneratesWhenNoCert verifies that loadOrGenerateTLS
// auto-generates a self-signed cert when no existing cert or user cert is present.
func TestLoadOrGenerateTLS_GeneratesWhenNoCert(t *testing.T) {
	requireElevatedOrSkip(t)

	dir := t.TempDir()
	cfg, err := loadOrGenerateTLS("", "", dir, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("loadOrGenerateTLS: %v", err)
	}
	if len(cfg.Certificates) == 0 {
		t.Fatal("tls.Config has no certificates")
	}
}

// TestLoadOrGenerateTLS_ReusesExistingCert verifies a second call reuses
// the auto-generated cert without generating a new one.
func TestLoadOrGenerateTLS_ReusesExistingCert(t *testing.T) {
	requireElevatedOrSkip(t)

	dir := t.TempDir()
	cfg1, err := loadOrGenerateTLS("", "", dir, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("first loadOrGenerateTLS: %v", err)
	}
	cfg2, err := loadOrGenerateTLS("", "", dir, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("second loadOrGenerateTLS: %v", err)
	}

	leaf1, _ := x509.ParseCertificate(cfg1.Certificates[0].Certificate[0])
	leaf2, _ := x509.ParseCertificate(cfg2.Certificates[0].Certificate[0])
	if certFingerprint(leaf1) != certFingerprint(leaf2) {
		t.Error("second call returned a different cert — expected reuse")
	}
}

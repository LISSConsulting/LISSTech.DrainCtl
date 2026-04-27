//go:build windows

package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// TestVerifyManifestRemote_NoKeysIsNoop covers the transition contract:
// when keys is empty, verifyManifestRemote returns nil regardless of
// whether the release ships a manifest. This is what lets us land the
// code change without forcing every release to carry a manifest from day
// one.
func TestVerifyManifestRemote_NoKeysIsNoop(t *testing.T) {
	if err := verifyManifestRemote(context.Background(), http.DefaultClient, "", "", "", nil); err != nil {
		t.Fatalf("no-keys mode should be a no-op, got err=%v", err)
	}
}

// TestVerifyManifestRemote_KeysButNoSidecar covers the once-keys-are-on
// contract: if keys is non-empty, every release MUST ship release.json
// and release.json.sig. Missing sidecar = refusal.
func TestVerifyManifestRemote_KeysButNoSidecar(t *testing.T) {
	pub, _ := mustGenKey(t)
	err := verifyManifestRemote(context.Background(), http.DefaultClient, "irrelevant", "", "", []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("expected error when sidecar URLs are empty and keys are configured")
	}
}

// TestVerifyManifestRemote_HappyPath drives the full remote path:
// generate a keypair, write a fake MSI, sign a manifest binding its hash,
// serve everything through httptest, and assert verification passes.
func TestVerifyManifestRemote_HappyPath(t *testing.T) {
	pub, priv := mustGenKey(t)

	dir := t.TempDir()
	msiPath := filepath.Join(dir, "x.msi")
	body := []byte("fake msi payload")
	if err := os.WriteFile(msiPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	manifest := ReleaseManifest{
		Version: "26.116.34",
		Asset:   ManifestAsset{Name: msiAssetName, SHA256: hex.EncodeToString(sum[:])},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, manifestBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			_, _ = w.Write(manifestBytes)
		case "/sig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	if err := verifyManifestRemote(context.Background(), srv.Client(), msiPath, srv.URL+"/manifest", srv.URL+"/sig", []ed25519.PublicKey{pub}); err != nil {
		t.Fatalf("verifyManifestRemote: %v", err)
	}
}

// TestVerifyManifestRemote_TamperedMSI exercises the most important
// failure mode: an attacker who can forge a CN-matching cert but cannot
// sign a manifest — they swap the MSI for something else. The hash check
// on the downloaded file catches it.
func TestVerifyManifestRemote_TamperedMSI(t *testing.T) {
	pub, priv := mustGenKey(t)

	dir := t.TempDir()
	msiPath := filepath.Join(dir, "x.msi")
	if err := os.WriteFile(msiPath, []byte("tampered payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := sha256.Sum256([]byte("the legit payload"))
	manifest := ReleaseManifest{
		Version: "1",
		Asset:   ManifestAsset{Name: msiAssetName, SHA256: hex.EncodeToString(other[:])},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, manifestBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			_, _ = w.Write(manifestBytes)
		case "/sig":
			_, _ = w.Write(sig)
		}
	}))
	t.Cleanup(srv.Close)

	err = verifyManifestRemote(context.Background(), srv.Client(), msiPath, srv.URL+"/manifest", srv.URL+"/sig", []ed25519.PublicKey{pub})
	if !errors.Is(err, ErrManifestHashMismatch) {
		t.Fatalf("got err=%v, want ErrManifestHashMismatch", err)
	}
}

// TestStart_DecodeKeysFailureBlocksGoroutine verifies the LCI contract:
// if decodeKeys returns an error, Start returns that error before any
// goroutine launches. This is the regression test for the package-init
// panic that the LCI rules forbid.
func TestStart_DecodeKeysFailureBlocksGoroutine(t *testing.T) {
	prev := decodeKeys
	t.Cleanup(func() { decodeKeys = prev })
	wantErr := errors.New("intentional decode failure")
	decodeKeys = func() ([]ed25519.PublicKey, error) {
		return nil, wantErr
	}

	s := New(dc.UpdateConfig{}, func() {})
	err := s.Start(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start: got err=%v, want wrap of %v", err, wantErr)
	}
	// Nothing to drain — Stop must still be safe per LCI Stop contract.
	s.Stop()
}

package updater

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mustGenKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

func TestVerifyReleaseManifest_RoundTrip(t *testing.T) {
	pub, priv := mustGenKey(t)

	m := ReleaseManifest{
		Version:  "26.116.33",
		Asset:    ManifestAsset{Name: "LISSTech.DrainCtl.msi", SHA256: hex.EncodeToString(make([]byte, 32))},
		SignedAt: "2026-04-27T12:00:00Z",
	}
	body := mustMarshalJSON(t, m)
	sig := ed25519.Sign(priv, body)

	got, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("VerifyReleaseManifest: %v", err)
	}
	if got.Version != m.Version || got.Asset.Name != m.Asset.Name || got.Asset.SHA256 != m.Asset.SHA256 {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, m)
	}
}

func TestVerifyReleaseManifest_TamperedBytes(t *testing.T) {
	pub, priv := mustGenKey(t)
	m := ReleaseManifest{Version: "1", Asset: ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(make([]byte, 32))}}
	body := mustMarshalJSON(t, m)
	sig := ed25519.Sign(priv, body)

	body[10] ^= 0x01 // single-bit flip anywhere in the signed bytes

	if _, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{pub}); !errors.Is(err, ErrManifestSignatureInvalid) {
		t.Fatalf("got err=%v, want ErrManifestSignatureInvalid", err)
	}
}

func TestVerifyReleaseManifest_WrongKey(t *testing.T) {
	_, priv := mustGenKey(t)
	otherPub, _ := mustGenKey(t)
	m := ReleaseManifest{Version: "1", Asset: ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(make([]byte, 32))}}
	body := mustMarshalJSON(t, m)
	sig := ed25519.Sign(priv, body)

	if _, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{otherPub}); !errors.Is(err, ErrManifestSignatureInvalid) {
		t.Fatalf("got err=%v, want ErrManifestSignatureInvalid", err)
	}
}

func TestVerifyReleaseManifest_NoKeys(t *testing.T) {
	if _, err := VerifyReleaseManifest([]byte("{}"), make([]byte, ed25519.SignatureSize), nil); !errors.Is(err, ErrManifestNoKeys) {
		t.Fatalf("got err=%v, want ErrManifestNoKeys", err)
	}
}

func TestVerifyReleaseManifest_KeyRotation(t *testing.T) {
	// Two keys configured; manifest is signed by the second one. Verifier
	// must accept it — this is the rotation contract.
	oldPub, _ := mustGenKey(t)
	newPub, newPriv := mustGenKey(t)
	m := ReleaseManifest{Version: "1", Asset: ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(make([]byte, 32))}}
	body := mustMarshalJSON(t, m)
	sig := ed25519.Sign(newPriv, body)

	if _, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{oldPub, newPub}); err != nil {
		t.Fatalf("rotation: VerifyReleaseManifest: %v", err)
	}
}

func TestVerifyReleaseManifest_BadSchema(t *testing.T) {
	_, priv := mustGenKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	cases := []struct {
		name string
		m    ReleaseManifest
	}{
		{"missing version", ReleaseManifest{Asset: ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(make([]byte, 32))}}},
		{"missing asset name", ReleaseManifest{Version: "1", Asset: ManifestAsset{SHA256: hex.EncodeToString(make([]byte, 32))}}},
		{"sha256 wrong length", ReleaseManifest{Version: "1", Asset: ManifestAsset{Name: "x.msi", SHA256: "abcd"}}},
		{"sha256 not hex", ReleaseManifest{Version: "1", Asset: ManifestAsset{Name: "x.msi", SHA256: "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := mustMarshalJSON(t, tc.m)
			sig := ed25519.Sign(priv, body)
			if _, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{pub}); !errors.Is(err, ErrManifestSchema) {
				t.Fatalf("got err=%v, want ErrManifestSchema", err)
			}
		})
	}
}

func TestVerifyAssetMatchesManifest_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.msi")
	body := []byte("hello world")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	m := ReleaseManifest{
		Version: "1",
		Asset:   ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(sum[:])},
	}
	if err := VerifyAssetMatchesManifest(path, "x.msi", m); err != nil {
		t.Fatalf("VerifyAssetMatchesManifest: %v", err)
	}
}

func TestVerifyAssetMatchesManifest_NameMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.msi")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("data"))
	m := ReleaseManifest{
		Version: "1",
		Asset:   ManifestAsset{Name: "different.msi", SHA256: hex.EncodeToString(sum[:])},
	}
	if err := VerifyAssetMatchesManifest(path, "x.msi", m); !errors.Is(err, ErrManifestAssetMismatch) {
		t.Fatalf("got err=%v, want ErrManifestAssetMismatch", err)
	}
}

func TestVerifyAssetMatchesManifest_HashMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.msi")
	if err := os.WriteFile(path, []byte("real content"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrong := sha256.Sum256([]byte("wrong content"))
	m := ReleaseManifest{
		Version: "1",
		Asset:   ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(wrong[:])},
	}
	if err := VerifyAssetMatchesManifest(path, "x.msi", m); !errors.Is(err, ErrManifestHashMismatch) {
		t.Fatalf("got err=%v, want ErrManifestHashMismatch", err)
	}
}

func TestHashFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x")
	body := []byte("the quick brown fox")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	got, err := HashFileSHA256(path)
	if err != nil {
		t.Fatalf("HashFileSHA256: %v", err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("hash: got %s, want %s", got, hex.EncodeToString(want[:]))
	}
}

func TestVerifyReleaseManifest_AcceptsLegacyZeroSchemaVersion(t *testing.T) {
	pub, priv := mustGenKey(t)
	// Field absent from JSON unmarshals to zero; that's the legacy case.
	m := ReleaseManifest{
		Version: "1",
		Asset:   ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(make([]byte, 32))},
	}
	body := mustMarshalJSON(t, m)
	sig := ed25519.Sign(priv, body)

	got, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("legacy schema_version=0 must be accepted in transition window; got err=%v", err)
	}
	if got.SchemaVersion != 0 {
		t.Errorf("got SchemaVersion=%d, want 0 (legacy)", got.SchemaVersion)
	}
}

func TestVerifyReleaseManifest_AcceptsCurrentSchemaVersion(t *testing.T) {
	pub, priv := mustGenKey(t)
	m := ReleaseManifest{
		SchemaVersion: ManifestSchemaVersion,
		Version:       "1",
		Asset:         ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(make([]byte, 32))},
	}
	body := mustMarshalJSON(t, m)
	sig := ed25519.Sign(priv, body)

	got, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("current schema_version must be accepted; got err=%v", err)
	}
	if got.SchemaVersion != ManifestSchemaVersion {
		t.Errorf("got SchemaVersion=%d, want %d", got.SchemaVersion, ManifestSchemaVersion)
	}
}

func TestVerifyReleaseManifest_RejectsUnknownSchemaVersion(t *testing.T) {
	pub, priv := mustGenKey(t)
	m := ReleaseManifest{
		SchemaVersion: 999,
		Version:       "1",
		Asset:         ManifestAsset{Name: "x.msi", SHA256: hex.EncodeToString(make([]byte, 32))},
	}
	body := mustMarshalJSON(t, m)
	sig := ed25519.Sign(priv, body)

	if _, err := VerifyReleaseManifest(body, sig, []ed25519.PublicKey{pub}); !errors.Is(err, ErrManifestSchema) {
		t.Fatalf("got err=%v, want ErrManifestSchema for unknown schema_version", err)
	}
}

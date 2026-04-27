package updater

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// ReleaseManifest binds a published release asset to its content hash.
// It is signed offline with Ed25519 (see cmd/release-sign), and the
// signature is shipped as a sidecar `release.json.sig` asset on the same
// GitHub release. The verifier downloads both, verifies the signature
// against the embedded public-key list (keys_windows.go), then hashes the
// downloaded MSI and compares to Asset.SHA256 before any Authenticode
// check runs.
//
// Schema is intentionally narrow: one asset per manifest. Future shape
// changes should add fields rather than break parsing — older verifiers
// simply ignore unknown JSON fields.
type ReleaseManifest struct {
	Version  string        `json:"version"`
	Asset    ManifestAsset `json:"asset"`
	SignedAt string        `json:"signed_at"`
}

// ManifestAsset describes the binary blob bound by the manifest.
// SHA256 is the lowercase hex SHA-256 of the asset's bytes (64 chars).
type ManifestAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

var (
	ErrManifestSignatureInvalid = errors.New("update: release manifest signature did not verify")
	ErrManifestParse            = errors.New("update: release manifest parse failed")
	ErrManifestSchema           = errors.New("update: release manifest schema invalid")
	ErrManifestAssetMismatch    = errors.New("update: downloaded asset name does not match release manifest")
	ErrManifestHashMismatch     = errors.New("update: downloaded asset SHA-256 does not match release manifest")
	ErrManifestNoKeys           = errors.New("update: no release-signing keys configured")
)

// VerifyReleaseManifest confirms sigBytes is a valid Ed25519 signature
// over the EXACT manifestBytes by one of keys, then unmarshals and
// validates the schema. The bytes-based verify (not "marshal-then-verify")
// avoids JSON canonicalization headaches: the signer signs the file as
// written to disk, the verifier verifies the bytes as they came over the
// wire — both are byte-identical end to end.
func VerifyReleaseManifest(manifestBytes, sigBytes []byte, keys []ed25519.PublicKey) (ReleaseManifest, error) {
	if len(keys) == 0 {
		return ReleaseManifest{}, ErrManifestNoKeys
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return ReleaseManifest{}, fmt.Errorf("%w: signature wrong size (got %d, want %d)",
			ErrManifestSignatureInvalid, len(sigBytes), ed25519.SignatureSize)
	}
	verified := false
	for _, k := range keys {
		if len(k) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(k, manifestBytes, sigBytes) {
			verified = true
			break
		}
	}
	if !verified {
		return ReleaseManifest{}, ErrManifestSignatureInvalid
	}

	var m ReleaseManifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return ReleaseManifest{}, fmt.Errorf("%w: %v", ErrManifestParse, err)
	}
	if m.Version == "" || m.Asset.Name == "" {
		return ReleaseManifest{}, fmt.Errorf("%w: missing version or asset.name", ErrManifestSchema)
	}
	if len(m.Asset.SHA256) != sha256HexLen {
		return ReleaseManifest{}, fmt.Errorf("%w: sha256 must be %d hex chars (got %d)",
			ErrManifestSchema, sha256HexLen, len(m.Asset.SHA256))
	}
	if _, err := hex.DecodeString(m.Asset.SHA256); err != nil {
		return ReleaseManifest{}, fmt.Errorf("%w: sha256 not valid hex: %v", ErrManifestSchema, err)
	}
	return m, nil
}

const sha256HexLen = sha256.Size * 2 // 32 bytes → 64 hex chars

// HashFileSHA256 returns the lowercase hex SHA-256 of the file at path.
// Streams via io.Copy so even large MSIs use bounded memory.
func HashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyAssetMatchesManifest compares the on-disk file at path to what
// the manifest claims. The asset name check rejects a manifest that
// described a different filename than what GitHub served — closes the
// "swap a different signed-by-the-same-key asset" attack window.
func VerifyAssetMatchesManifest(path, expectedAssetName string, m ReleaseManifest) error {
	if m.Asset.Name != expectedAssetName {
		return fmt.Errorf("%w: manifest=%q, downloaded=%q",
			ErrManifestAssetMismatch, m.Asset.Name, expectedAssetName)
	}
	got, err := HashFileSHA256(path)
	if err != nil {
		return fmt.Errorf("hash file: %w", err)
	}
	// Hex compare is case-insensitive at the byte level; we standardize
	// on lowercase but accept either to avoid false negatives if a future
	// signer emits uppercase.
	if !hexEqualFold(got, m.Asset.SHA256) {
		return fmt.Errorf("%w: manifest=%s, downloaded=%s",
			ErrManifestHashMismatch, m.Asset.SHA256, got)
	}
	return nil
}

func hexEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'F' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'F' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

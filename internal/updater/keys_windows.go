//go:build windows

package updater

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

// releaseSigningKeysB64 holds the base64-encoded Ed25519 public keys
// authorized to sign release manifests. Decoding happens at Subsystem
// startup (not package init), so a malformed entry surfaces as a Start
// error through the LCI rather than a process-init panic. See
// docs/architecture/lifecycle.md §"Errors" for the rule this preserves.
//
// Multiple keys exist to support rotation: ship binaries embedding both
// the current and next key BEFORE rotating the signing key in production,
// so that fielded binaries can verify manifests signed with either.
//
// To generate a new keypair:
//
//	go run ./cmd/release-sign keygen --out C:\path\to\release-signing.key
//
// then paste the printed public key as a string entry below.
//
// EMPTY LIST DISABLES MANIFEST VERIFICATION: the updater falls back to
// Authenticode + Subject CN only. This is the build-time default until a
// keypair is generated and embedded; once at least one key appears below,
// every release MUST ship a valid release.json + release.json.sig.
var releaseSigningKeysB64 = []string{
	"JbWbeH+NbD228Rzie3qmNlbWLaMx1tjFXr3ILAVMV6k=",
}

// decodeReleaseSigningKeys parses every entry in releaseSigningKeysB64
// into a usable ed25519.PublicKey. Empty entries are skipped (operators
// commenting out a key). Any other failure is returned, not panicked —
// the Subsystem surfaces it via Start.
func decodeReleaseSigningKeys(entries []string) ([]ed25519.PublicKey, error) {
	out := make([]ed25519.PublicKey, 0, len(entries))
	for i, s := range entries {
		if s == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("releaseSigningKeysB64[%d]: base64 decode: %w", i, err)
		}
		if len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("releaseSigningKeysB64[%d]: wrong size: got %d, want %d",
				i, len(b), ed25519.PublicKeySize)
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out, nil
}

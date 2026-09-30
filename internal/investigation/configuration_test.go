//go:build windows

package investigation

import (
	"context"
	"errors"
	"testing"
)

func readyConfigSnapshot() ConfigSnapshot {
	return ConfigSnapshot{AccessEnabled: true, AutomaticEnabled: true, PrivacyAcknowledgementVersion: "openai_responses_privacy_v1", AcknowledgementReferenceCurrent: true, CredentialCiphertext: "dpapi:credential"}
}

func TestConfigAccessorReloadsBeforeDecryption(t *testing.T) {
	snapshot := readyConfigSnapshot()
	loads := 0
	accessor := NewConfigAccessor(func(context.Context) (ConfigSnapshot, error) {
		loads++
		if loads == 2 {
			snapshot.AccessEnabled = false
		}
		return snapshot, nil
	}, func(ciphertext string) ([]byte, error) {
		t.Fatalf("decrypt called for %q after revoked config", ciphertext)
		return nil, nil
	})
	if err := accessor.ValidateForSend(context.Background()); err != nil {
		t.Fatalf("initial validation: %v", err)
	}
	if _, err := accessor.DecryptCredential(context.Background()); err == nil {
		t.Fatal("decrypted after configuration revocation")
	}
}

func TestConfigAccessorRequiresExactAcknowledgementReference(t *testing.T) {
	snapshot := readyConfigSnapshot()
	snapshot.AcknowledgementReferenceCurrent = false
	accessor := NewConfigAccessor(func(context.Context) (ConfigSnapshot, error) { return snapshot, nil }, func(string) ([]byte, error) { return []byte("secret"), nil })
	if err := accessor.ValidateForSend(context.Background()); err == nil {
		t.Fatal("accepted non-current acknowledgement reference")
	}
	if ready, err := accessor.AutomaticReady(context.Background()); err == nil || ready {
		t.Fatalf("AutomaticReady = %v, %v; want false, error", ready, err)
	}
}

func TestConfigAccessorDecryptsOnlyReadyCurrentCredential(t *testing.T) {
	accessor := NewConfigAccessor(func(context.Context) (ConfigSnapshot, error) { return readyConfigSnapshot(), nil }, func(ciphertext string) ([]byte, error) {
		if ciphertext != "dpapi:credential" {
			t.Fatalf("ciphertext = %q", ciphertext)
		}
		return []byte("secret"), nil
	})
	credential, err := accessor.DecryptCredential(context.Background())
	if err != nil || string(credential) != "secret" {
		t.Fatalf("DecryptCredential = %q, %v", credential, err)
	}
	zero(credential)

	failed := NewConfigAccessor(func(context.Context) (ConfigSnapshot, error) { return ConfigSnapshot{}, errors.New("load failed") }, nil)
	if err := failed.ValidateForSend(context.Background()); err == nil {
		t.Fatal("loader error accepted")
	}
}

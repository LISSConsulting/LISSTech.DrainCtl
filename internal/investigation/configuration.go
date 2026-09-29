//go:build windows

package investigation

import (
	"context"
	"errors"
)

// ConfigSnapshot is the minimum current provider configuration needed to
// authorize a send. CredentialCiphertext remains protected until the provider
// client is about to construct its request.
type ConfigSnapshot struct {
	AccessEnabled                   bool
	AutomaticEnabled                bool
	PrivacyAcknowledgementVersion   string
	AcknowledgementReferenceCurrent bool
	CredentialCiphertext            string
	AuditDays                       int
	CurrentAcknowledgementAuditID   int64
}

// ConfigLoader reads one atomically current configuration and verifies its
// acknowledgement reference against the immutable local audit record.
type ConfigLoader func(context.Context) (ConfigSnapshot, error)

// CredentialDecryptor decrypts only the protected root-Config credential.
type CredentialDecryptor func(string) ([]byte, error)

// ConfigAccessor deliberately retains neither configuration nor plaintext.
// Every authorization and decryption operation rereads the current snapshot,
// so a reload can revoke provider access before egress.
type ConfigAccessor struct {
	load    ConfigLoader
	decrypt CredentialDecryptor
}

func NewConfigAccessor(load ConfigLoader, decrypt CredentialDecryptor) *ConfigAccessor {
	return &ConfigAccessor{load: load, decrypt: decrypt}
}

func (a *ConfigAccessor) ValidateForSend(ctx context.Context) error {
	_, err := a.current(ctx)
	return err
}

// DecryptCredential revalidates the atomically current configuration before
// DPAPI work. The returned bytes belong solely to ProviderClient, which zeros
// them immediately after constructing the request.
func (a *ConfigAccessor) DecryptCredential(ctx context.Context) ([]byte, error) {
	snapshot, err := a.current(ctx)
	if err != nil {
		return nil, err
	}
	if a.decrypt == nil {
		return nil, errors.New("missing credential decryptor")
	}
	credential, err := a.decrypt(snapshot.CredentialCiphertext)
	if err != nil || len(credential) == 0 {
		zero(credential)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("empty credential")
	}
	return credential, nil
}

// AutomaticReady reports whether automatic roots are currently permitted.
// It does not decrypt the credential.
func (a *ConfigAccessor) AutomaticReady(ctx context.Context) (bool, error) {
	snapshot, err := a.current(ctx)
	if err != nil {
		return false, err
	}
	return snapshot.AutomaticEnabled, nil
}

func (a *ConfigAccessor) current(ctx context.Context) (ConfigSnapshot, error) {
	if a == nil || a.load == nil {
		return ConfigSnapshot{}, errors.New("missing configuration loader")
	}
	snapshot, err := a.load(ctx)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	if !snapshot.AccessEnabled ||
		snapshot.PrivacyAcknowledgementVersion != "openai_responses_privacy_v1" ||
		!snapshot.AcknowledgementReferenceCurrent ||
		snapshot.CredentialCiphertext == "" {
		return ConfigSnapshot{}, errors.New("provider configuration is not ready")
	}
	return snapshot, nil
}

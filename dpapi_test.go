//go:build windows

package drainctl

import (
	"bytes"
	"testing"
)

func TestDPAPIEncryptDecrypt_RoundTrip(t *testing.T) {
	plaintext := []byte("s3cr3t-webhook-key")
	ct, err := DPAPIEncrypt(plaintext)
	if err != nil {
		t.Fatalf("DPAPIEncrypt: %v", err)
	}
	if len(ct) == 0 {
		t.Fatal("DPAPIEncrypt returned empty ciphertext")
	}
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext should differ from plaintext")
	}

	pt, err := DPAPIDecrypt(ct)
	if err != nil {
		t.Fatalf("DPAPIDecrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("DPAPIDecrypt = %q, want %q", pt, plaintext)
	}
}

func TestDPAPIEncrypt_EmptyInput(t *testing.T) {
	ct, err := DPAPIEncrypt(nil)
	if err != nil {
		t.Fatalf("DPAPIEncrypt(nil): %v", err)
	}
	if ct != nil {
		t.Errorf("DPAPIEncrypt(nil) = %v, want nil", ct)
	}

	ct, err = DPAPIEncrypt([]byte{})
	if err != nil {
		t.Fatalf("DPAPIEncrypt(empty): %v", err)
	}
	if ct != nil {
		t.Errorf("DPAPIEncrypt(empty) = %v, want nil", ct)
	}
}

func TestDPAPIDecrypt_EmptyInput(t *testing.T) {
	pt, err := DPAPIDecrypt(nil)
	if err != nil {
		t.Fatalf("DPAPIDecrypt(nil): %v", err)
	}
	if pt != nil {
		t.Errorf("DPAPIDecrypt(nil) = %v, want nil", pt)
	}
}

func TestDPAPIDecrypt_InvalidCiphertext(t *testing.T) {
	_, err := DPAPIDecrypt([]byte("not-a-valid-dpapi-blob"))
	if err == nil {
		t.Error("DPAPIDecrypt(garbage) should fail")
	}
}

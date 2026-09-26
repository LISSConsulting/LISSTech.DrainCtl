//go:build windows

package dashboard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func writeSubsystemTestTLS(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "dashboard-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func TestSubsystem_RDCollectionResolverLifecycleAndBrokerUpdate(t *testing.T) {
	initialLoad := make(chan string, 1)
	updatedLoad := make(chan string, 1)
	loaderCanceled := make(chan struct{})
	resolver := newRDCollectionResolver(func(ctx context.Context, broker string) ([]byte, error) {
		switch broker {
		case "initial-broker.example.test":
			initialLoad <- broker
			return []byte(`[]`), nil
		case "updated-broker.example.test":
			updatedLoad <- broker
			<-ctx.Done()
			close(loaderCanceled)
			return nil, ctx.Err()
		default:
			t.Errorf("loader broker = %q", broker)
			return []byte(`[]`), nil
		}
	})

	certPath, keyPath := writeSubsystemTestTLS(t)

	s := NewSubsystem(dc.DashboardConfig{
		Port:               0,
		TLSCert:            certPath,
		TLSKey:             keyPath,
		RDConnectionBroker: "initial-broker.example.test",
	}, t.TempDir(), nil, nil, nil, nil, nil, nil, nil, false)
	s.rdCollections = resolver
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	select {
	case broker := <-initialLoad:
		if broker != "initial-broker.example.test" {
			t.Errorf("initial broker = %q", broker)
		}
	case <-time.After(time.Second):
		t.Fatal("resolver did not load the initial broker")
	}

	s.UpdateRDConnectionBroker("updated-broker.example.test")
	select {
	case broker := <-updatedLoad:
		if broker != "updated-broker.example.test" {
			t.Errorf("updated broker = %q", broker)
		}
	case <-time.After(time.Second):
		t.Fatal("resolver did not load the updated broker")
	}

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-loaderCanceled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the resolver loader")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not wait for the resolver to exit")
	}
}

func TestSubsystem_RDCollectionResolverPreservesPreStartBrokerUpdate(t *testing.T) {
	loaded := make(chan string, 2)
	resolver := newRDCollectionResolver(func(_ context.Context, broker string) ([]byte, error) {
		loaded <- broker
		return []byte(`[]`), nil
	})
	resolver.UpdateBroker("updated-broker.example.test")

	certPath, keyPath := writeSubsystemTestTLS(t)
	s := NewSubsystem(dc.DashboardConfig{
		Port:               0,
		TLSCert:            certPath,
		TLSKey:             keyPath,
		RDConnectionBroker: "initial-broker.example.test",
	}, t.TempDir(), nil, nil, nil, nil, nil, nil, nil, false)
	s.rdCollections = resolver
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)

	select {
	case broker := <-loaded:
		if broker != "updated-broker.example.test" {
			t.Fatalf("initial resolver broker = %q, want updated-broker.example.test", broker)
		}
	case <-time.After(time.Second):
		t.Fatal("resolver did not refresh")
	}
}

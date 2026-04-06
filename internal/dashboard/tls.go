//go:build windows

package dashboard

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// loadOrGenerateTLS returns a tls.Config for the dashboard.
//
// If certFile and keyFile are both set, it loads the user-provided PEM files.
// Otherwise, it auto-generates a self-signed certificate and stores it in
// dataDir for reuse across restarts.
func loadOrGenerateTLS(certFile, keyFile, dataDir string, log dc.LogFunc) (*tls.Config, error) {
	// Option B: user-provided certificate.
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS cert/key: %w", err)
		}
		log(dc.LvlINF, fmt.Sprintf("dashboard=tls cert=%s key=%s", certFile, keyFile))
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil
	}

	// Option A: auto-generated self-signed.
	autoCert := filepath.Join(dataDir, "dashboard-tls.crt")
	autoKey := filepath.Join(dataDir, "dashboard-tls.key")

	// Reuse existing auto-generated cert if it exists and is not expired.
	if cert, err := tls.LoadX509KeyPair(autoCert, autoKey); err == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			if time.Now().Before(leaf.NotAfter.Add(-24 * time.Hour)) {
				fp := certFingerprint(leaf)
				log(dc.LvlINF, fmt.Sprintf("dashboard=tls auto-cert reused (expires %s fingerprint=%s)", leaf.NotAfter.Format("2006-01-02"), fp))
				return &tls.Config{
					Certificates: []tls.Certificate{cert},
					MinVersion:   tls.VersionTLS12,
				}, nil
			}
		}
	}

	// Generate new self-signed cert.
	cert, err := generateSelfSigned(autoCert, autoKey, log)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// certFingerprint returns the SHA-256 fingerprint of a certificate.
func certFingerprint(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(h[:])
}

// CertFingerprint loads the dashboard's auto-generated cert and returns
// its SHA-256 fingerprint. Used by agents for certificate pinning.
func CertFingerprint(dataDir string) (string, error) {
	certPath := filepath.Join(dataDir, "dashboard-tls.crt")
	data, err := os.ReadFile(certPath)
	if err != nil {
		return "", fmt.Errorf("read cert: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("no PEM block in %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse cert: %w", err)
	}
	return certFingerprint(cert), nil
}

func generateSelfSigned(certPath, keyPath string, log dc.LogFunc) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate TLS key: %w", err)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"LISS Technologies"},
			CommonName:   "DrainCtl Dashboard (" + hostname + ")",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 year
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create self-signed cert: %w", err)
	}

	// Write cert PEM (world-readable is fine — it's the public cert).
	certOut, err := os.Create(certPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("write cert file: %w", err)
	}
	if encErr := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); encErr != nil {
		_ = certOut.Close()
		_ = os.Remove(certPath)
		return tls.Certificate{}, fmt.Errorf("encode cert PEM: %w", encErr)
	}
	if closeErr := certOut.Close(); closeErr != nil {
		_ = os.Remove(certPath)
		return tls.Certificate{}, fmt.Errorf("flush cert file: %w", closeErr)
	}

	// Write key PEM with restricted ACL (SYSTEM + Administrators only).
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal key: %w", err)
	}
	keyData := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := writeRestrictedFile(keyPath, keyData); err != nil {
		_ = os.Remove(certPath)
		return tls.Certificate{}, fmt.Errorf("write key file: %w", err)
	}

	leaf, _ := x509.ParseCertificate(certDER)
	fp := ""
	if leaf != nil {
		fp = certFingerprint(leaf)
	}
	log(dc.LvlINF, fmt.Sprintf("dashboard=tls auto-cert generated host=%s fingerprint=%s cert=%s", hostname, fp, certPath))

	return tls.LoadX509KeyPair(certPath, keyPath)
}

// writeRestrictedFile writes data to path with ACLs that grant access
// only to SYSTEM and the built-in Administrators group.
func writeRestrictedFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}

	// Reset ACL: disable inheritance, remove all inherited ACEs,
	// then grant only SYSTEM and Administrators full control.
	cmds := [][]string{
		{"icacls", path, "/inheritance:r"},
		{"icacls", path, "/grant", "SYSTEM:(F)"},
		{"icacls", path, "/grant", "*S-1-5-32-544:(F)"}, // Administrators by SID (locale-independent)
	}
	for _, args := range cmds {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w (%s)", args[0], err, string(out))
		}
	}

	return nil
}

//go:build windows

package dashboard

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
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
				log(dc.LvlINF, fmt.Sprintf("dashboard=tls auto-cert reused (expires %s)", leaf.NotAfter.Format("2006-01-02")))
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

	// Write cert PEM.
	certOut, err := os.Create(certPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("write cert file: %w", err)
	}
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	_ = certOut.Close()

	// Write key PEM.
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal key: %w", err)
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("write key file: %w", err)
	}
	_ = pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	_ = keyOut.Close()

	log(dc.LvlINF, fmt.Sprintf("dashboard=tls auto-cert generated host=%s cert=%s", hostname, certPath))

	return tls.LoadX509KeyPair(certPath, keyPath)
}

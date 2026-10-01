package database

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"
)

// ClickHouse publishes four names per data member, plus service, Keeper and
// public endpoint names. Keep verification bounded for every supported shape.
const MaxTLSVerificationNames = 4*MaxMembers + 16 + MaxPublicEndpointNames

// A missing policy identifies a database created before enforced TLS support.
// New databases require TLS; existing databases need an explicit migration.
type TLSConfig struct {
	Mode string `json:"mode" toml:"mode"`
}

func (s Spec) TLSRequired() bool { return s.TLS != nil && s.TLS.Mode == "required" }

// Apply this only to creation. A stored nil policy remains distinguishable so
// an existing plaintext database is never silently changed during a resize.
func (s Spec) WithSecureDefaults() Spec {
	if s.TLS == nil {
		s.TLS = &TLSConfig{Mode: "required"}
	}
	return s
}

type TLSObservation struct {
	Required          bool       `json:"required"`
	Verified          bool       `json:"verified"`
	PlaintextRejected bool       `json:"plaintext_rejected"`
	MinimumVersion    string     `json:"minimum_version,omitempty"`
	Issuer            string     `json:"issuer,omitempty"`
	DNSNames          []string   `json:"dns_names,omitempty"`
	Fingerprint       string     `json:"fingerprint,omitempty"`
	CAFingerprint     string     `json:"ca_fingerprint,omitempty"`
	NotBefore         *time.Time `json:"not_before,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	CheckedAt         *time.Time `json:"checked_at,omitempty"`
	Message           string     `json:"message,omitempty"`
}

// PublicTrust contains only a CA certificate. It must never carry a private key.
type PublicTrust struct {
	CertificatePEM string    `json:"certificate_pem"`
	Fingerprint    string    `json:"fingerprint"`
	Issuer         string    `json:"issuer"`
	NotBefore      time.Time `json:"not_before"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func ParsePublicTrust(data []byte, now time.Time) (PublicTrust, *x509.Certificate, error) {
	var result PublicTrust
	if len(data) == 0 || len(data) > 32<<10 {
		return result, nil, fmt.Errorf("database trust exceeds its size limit")
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return result, nil, fmt.Errorf("database trust must contain exactly one public CA certificate")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return result, nil, fmt.Errorf("database CA is invalid or outside its validity period")
	}
	if err = ca.CheckSignatureFrom(ca); err != nil {
		return result, nil, fmt.Errorf("database root CA signature is invalid")
	}
	hash := sha256.Sum256(ca.Raw)
	return PublicTrust{CertificatePEM: string(pem.EncodeToMemory(block)), Fingerprint: hex.EncodeToString(hash[:]), Issuer: ca.Subject.String(), NotBefore: ca.NotBefore, ExpiresAt: ca.NotAfter}, ca, nil
}

func VerifyServerCertificate(data []byte, ca *x509.Certificate, hosts []string, now time.Time) (TLSObservation, error) {
	result := TLSObservation{Required: true, MinimumVersion: "TLSv1.2"}
	if len(data) == 0 || len(data) > 32<<10 || len(hosts) == 0 || len(hosts) > MaxTLSVerificationNames || ca == nil {
		return result, fmt.Errorf("database certificate or endpoint list is invalid")
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return result, fmt.Errorf("database server certificate is invalid")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || leaf.IsCA {
		return result, fmt.Errorf("database server certificate is invalid")
	}
	result.NotBefore, result.ExpiresAt = &leaf.NotBefore, &leaf.NotAfter
	result.Issuer, result.DNSNames = leaf.Issuer.String(), leaf.DNSNames
	root := x509.NewCertPool()
	root.AddCert(ca)
	for _, host := range hosts {
		if _, err = leaf.Verify(x509.VerifyOptions{Roots: root, DNSName: host, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return result, fmt.Errorf("database server certificate does not verify for its endpoint")
		}
	}
	hash, rootHash := sha256.Sum256(leaf.Raw), sha256.Sum256(ca.Raw)
	result.Fingerprint, result.CAFingerprint = hex.EncodeToString(hash[:]), hex.EncodeToString(rootHash[:])
	// Only a runtime handshake may set Verified and PlaintextRejected.
	return result, nil
}

package database

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func databaseTLSFixture(t *testing.T, now time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-ca"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	root, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"database-rw.fixture.svc", "database-ro.fixture.svc"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Minute)}
	server, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server})
}

func TestPublicDatabaseTrustRejectsPrivateAndInvalidMaterial(t *testing.T) {
	now := time.Now()
	ca, leaf := databaseTLSFixture(t, now)
	trust, _, err := ParsePublicTrust(ca, now)
	if err != nil || trust.CertificatePEM != string(ca) || len(trust.Fingerprint) != 64 {
		t.Fatal("public CA not parsed", err)
	}
	for name, data := range map[string][]byte{"empty": nil, "leaf": leaf, "extra certificate": append(append([]byte{}, ca...), ca...), "private key": append(append([]byte{}, ca...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("private")})...), "oversize": bytes.Repeat([]byte{'x'}, (32<<10)+1)} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParsePublicTrust(data, now); err == nil {
				t.Fatal("unsafe material accepted")
			}
		})
	}
	for _, clock := range []time.Time{now.Add(-2 * time.Hour), now.Add(2 * time.Hour)} {
		if _, _, err := ParsePublicTrust(ca, clock); err == nil {
			t.Fatal("CA outside validity accepted")
		}
	}
}

func TestDatabaseCertificateRequiresIdentityIssuerAndValidity(t *testing.T) {
	now := time.Now()
	root, leaf := databaseTLSFixture(t, now)
	_, ca, _ := ParsePublicTrust(root, now)
	status, err := VerifyServerCertificate(leaf, ca, []string{"database-rw.fixture.svc", "database-ro.fixture.svc"}, now)
	if err != nil || status.Verified || status.PlaintextRejected || status.CheckedAt != nil || len(status.Fingerprint) != 64 {
		t.Fatal("certificate presence incorrectly became runtime verification", err)
	}
	other, _ := databaseTLSFixture(t, now)
	_, wrongCA, _ := ParsePublicTrust(other, now)
	for _, tc := range []struct {
		name  string
		root  *x509.Certificate
		host  string
		clock time.Time
	}{
		{"wrong CA", wrongCA, "database-rw.fixture.svc", now},
		{"wrong hostname", ca, "attacker.fixture.svc", now},
		{"expired", ca, "database-rw.fixture.svc", now.Add(2 * time.Minute)},
		{"not yet valid", ca, "database-rw.fixture.svc", now.Add(-2 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyServerCertificate(leaf, tc.root, []string{tc.host}, tc.clock); err == nil {
				t.Fatal("unverified certificate accepted")
			}
		})
	}
}

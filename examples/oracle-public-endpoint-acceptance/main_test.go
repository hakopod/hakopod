package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestReviewedDNSAddressRequiresOneExactIPv4(t *testing.T) {
	accepted, err := reviewedDNSAddress([]net.IPAddr{{IP: net.ParseIP("198.51.100.24")}}, "198.51.100.24")
	if err != nil || accepted != "198.51.100.24" {
		t.Fatalf("exact reviewed address was rejected: %q %v", accepted, err)
	}
	for name, addresses := range map[string][]net.IPAddr{
		"wrong": {{IP: net.ParseIP("198.51.100.25")}},
		"extra": {{IP: net.ParseIP("198.51.100.24")}, {IP: net.ParseIP("198.51.100.25")}},
		"ipv6":  {{IP: net.ParseIP("2001:db8::1")}},
		"zone":  {{IP: net.ParseIP("198.51.100.24"), Zone: "changed"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := reviewedDNSAddress(addresses, "198.51.100.24"); err == nil {
				t.Fatal("unreviewed DNS answer was accepted")
			}
		})
	}
}

func TestTLSConfigPinsReviewedLeafFingerprint(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "acceptance-test"},
		DNSNames:              []string{"database-15432.example.test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	leaf, err := x509.CreateCertificate(rand.Reader, &template, &template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(leaf)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(leaf)
	o := options{host: "database-15432.example.test", expectedFingerprint: hex.EncodeToString(fingerprint[:]),
		caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})}
	config, err := tlsConfig(o, o.host, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}}); err != nil {
		t.Fatalf("reviewed leaf fingerprint was rejected: %v", err)
	}
	if err := config.VerifyConnection(tls.ConnectionState{PeerCertificates: nil}); err == nil {
		t.Fatal("missing peer certificate was accepted")
	}
	changed := append([]byte(nil), leaf...)
	changed[len(changed)-1] ^= 1
	if err := config.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: changed}}}); err == nil {
		t.Fatal("changed leaf fingerprint was accepted")
	}
}

func TestProbeArgumentValidatorsRejectUnsafeInputs(t *testing.T) {
	for _, hostname := range []string{"", "UPPER.example.test", "127.0.0.1", "-bad.example.test", "bad..example.test"} {
		if validHostname(hostname) {
			t.Fatalf("unsafe hostname %q was accepted", hostname)
		}
	}
	if !validHostname("database-15432.example.test") {
		t.Fatal("valid reviewed hostname was rejected")
	}
	for _, address := range []string{"", "0.0.0.0", "224.0.0.1", "2001:db8::1"} {
		if _, err := reviewedDNSAddress([]net.IPAddr{{IP: net.ParseIP("198.51.100.24")}}, address); err == nil {
			t.Fatalf("unsafe reviewed address %q was accepted", address)
		}
	}
}

func TestBadPasswordRequiresOracleInvalidCredentialCode(t *testing.T) {
	if !oracleInvalidCredentials(errors.New("ORA-01017: invalid username/password; logon denied")) {
		t.Fatal("Oracle invalid-credential result was rejected")
	}
	for _, err := range []error{nil, errors.New("dial timeout"), errors.New("ORA-12514: listener does not know service")} {
		if oracleInvalidCredentials(err) {
			t.Fatalf("unrelated connection error was accepted as invalid credentials: %v", err)
		}
	}
}

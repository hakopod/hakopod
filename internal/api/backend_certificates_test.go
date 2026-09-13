package api

import (
	"strings"
	"testing"
)

func TestBackendCertificateInputSeparatesSourcesAndSecrets(t *testing.T) {
	valid := []backendCertificateInput{
		{Hostname: "smtp.example.com", CertificatePEM: "certificate", PrivateKeyPEM: "key"},
		{Hostname: "smtp.example.com", FromIngress: true},
	}
	for _, input := range valid {
		if err := input.validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []backendCertificateInput{
		{Hostname: "smtp.example.com", PrivateKeyPEM: "sensitive-fixture-key"},
		{Hostname: "smtp.example.com", FromIngress: true, PrivateKeyPEM: "sensitive-fixture-key"},
		{Hostname: "*.example.com", FromIngress: true},
		{Hostname: "smtp.example.com", CertificatePEM: "certificate", PrivateKeyPEM: strings.Repeat("sensitive-fixture-key", 2048)},
	} {
		err := input.validate()
		if err == nil || strings.Contains(err.Error(), "sensitive-fixture-key") {
			t.Fatal("invalid input accepted or private key leaked")
		}
	}
}

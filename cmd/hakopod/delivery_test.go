package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestCertificateInputFilesAreBoundedAndNotEchoed(t *testing.T) {
	dir := t.TempDir()
	chain, key := filepath.Join(dir, "chain.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(chain, []byte("fixture public chain"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := "private-test-material-that-must-not-be-printed"
	if err := os.WriteFile(key, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := readCertificateInput("mail.example.com", chain, key, false)
	if err != nil || input.CertificatePEM != "fixture public chain" || input.PrivateKeyPEM != secret {
		t.Fatal("file upload input failed", err)
	}
	for _, in := range []struct {
		host, chain, key string
		ingress          bool
	}{
		{"*.example.com", chain, key, false},
		{"mail.example.com", chain, "", false},
		{"mail.example.com", chain, key, true},
		{"mail.example.com", dir, key, false},
	} {
		if _, err := readCertificateInput(in.host, in.chain, in.key, in.ingress); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("invalid source accepted or private key echoed")
		}
	}
	if err := os.WriteFile(key, []byte(strings.Repeat("x", (32<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCertificateInput("mail.example.com", chain, key, false); err == nil {
		t.Fatal("oversized private key accepted")
	}
	if err := os.WriteFile(chain, []byte(strings.Repeat("x", (256<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCertificateInput("mail.example.com", chain, key, false); err == nil {
		t.Fatal("oversized certificate accepted")
	}
	input, err = readCertificateInput("mail.example.com", "", "", true)
	if err != nil || !input.FromIngress || input.PrivateKeyPEM != "" {
		t.Fatal("ingress import unexpectedly reads a local key", err)
	}
	if got, want := reorder([]string{"mail", "--from-ingress", "--service", "smtp"}), []string{"--from-ingress", "--service", "smtp", "mail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("boolean import flag consumed an argument: %v", got)
	}
}

func TestDeliveryCLIUsesServiceScopeAndReturnsOnlyMetadata(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer fixture-session" {
			t.Error("CLI authorization missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/applications/app-id/services/smtp/certificates":
			if r.Method == "POST" {
				var in certificateUpload
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Hostname != "mail.example.com" || in.PrivateKeyPEM != "private-fixture" {
					t.Error("certificate body did not arrive in scoped upload request", err)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"certificate":"hp-cert-new","hostname":"mail.example.com","ready":true,"source":"upload","private_key_pem":"never-output-this"}`))
			} else if r.Method == "GET" {
				_, _ = w.Write([]byte(`{"items":[{"certificate":"hp-cert-new","hostname":"mail.example.com","ready":true,"source":"upload","private_key_pem":"never-output-this"}]}`))
			} else {
				t.Error("unexpected certificate method")
			}
		case "/api/v1/applications/app-id/services/smtp/delivery":
			_, _ = w.Write([]byte(`{"public_tcp":[{"port":587,"target_port":1587,"status":"configured","message":"External probe required"}],"aws_identity":{"binding":"sender","role_arn":"arn:aws:iam::123456789012:role/sender","region":"ap-south-1","service_account":"hp-aws-fixture","token_audience":"sts.amazonaws.com","status":"prepared","aws_verified":false,"token":"never-output-this"},"observed_at":"2026-09-13T12:00:00Z"}`))
		default:
			t.Errorf("request escaped selected application/service: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c, err := newClient(config{URL: server.URL, Key: "fixture-session"})
	if err != nil {
		t.Fatal(err)
	}
	app := store.Application{ID: "app-id", Spec: spec.Application{Services: map[string]spec.Service{"smtp": {}}}}
	ctx := context.Background()
	for _, service := range []string{"", "other"} {
		if _, err := serviceCertificates(ctx, c, app, service); err == nil {
			t.Fatal("missing or different service accepted")
		}
	}
	if requests != 0 {
		t.Fatal("invalid service issued a request")
	}
	upload, err := uploadServiceCertificate(ctx, c, app, "smtp", certificateUpload{Hostname: "mail.example.com", CertificatePEM: "public-fixture", PrivateKeyPEM: "private-fixture"})
	if err != nil || upload.Certificate != "hp-cert-new" {
		t.Fatal("certificate upload failed", err)
	}
	certificates, err := serviceCertificates(ctx, c, app, "smtp")
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := serviceDelivery(ctx, c, app, "smtp")
	if err != nil || delivery.AWSIdentity == nil || delivery.AWSIdentity.AWSVerified || delivery.PublicTCP[0].Port != 587 {
		t.Fatal("delivery observation lost unverified status", err)
	}
	for _, value := range []any{upload, certificates, delivery} {
		out, err := json.Marshal(value)
		if err != nil || strings.Contains(string(out), "never-output-this") || strings.Contains(string(out), "private-fixture") || strings.Contains(string(out), "private_key_pem") {
			t.Fatal("CLI output included certificate/key/token material", err)
		}
	}
	if requests != 3 {
		t.Fatalf("unexpected request count %d", requests)
	}
}

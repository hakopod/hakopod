package spec

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestTemplateCanonicalConfigurationAndInputs(t *testing.T) {
	for _, template := range Templates() {
		if len(template.Sources) == 0 {
			t.Errorf("%s lacks official setup references", template.ID)
		}
		if !template.Deployable {
			continue
		}
		o := TemplateOptions{Name: "catalog", Model: "Qwen/Qwen3-0.6B", ModelRevision: strings.Repeat("a", 40)}
		if template.SiteURLSupported {
			o.SiteURL = "https://workspace.example.test"
		}
		if template.DatabaseConfig {
			o.DatabaseName = "project_data"
			o.DatabaseUser = "app_user"
		}
		o.Values = catalogTestValues(template)
		a, err := PlanTemplate(template.ID, o)
		if err != nil {
			t.Fatal(template.ID, err)
		}
		encoded, err := toml.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Parse(encoded)
		if err != nil || !reflect.DeepEqual(a, b) {
			t.Fatal("canonical template TOML changed its configuration", template.ID, err)
		}
		for _, required := range TemplateSecretNames(a) {
			if _, exists := TemplateSecretFieldByName(template.ID, required); !exists {
				t.Error("missing guided credential", template.ID, required)
			}
		}
		if template.ID == "postgresql" && a.Services["main"].Env["POSTGRES_DB"] != o.DatabaseName {
			t.Fatal("database name ignored")
		}
		if template.ID == "mysql" && a.Services["main"].Env["MYSQL_USER"] != o.DatabaseUser {
			t.Fatal("database user ignored")
		}
	}
	for _, bad := range []TemplateOptions{{Name: "db", DatabaseUser: "root"}, {Name: "db", DatabaseName: "app; DROP DATABASE app"}, {Name: "db", DatabaseUser: strings.Repeat("x", 33)}} {
		if _, err := PlanTemplate("mysql", bad); err == nil {
			t.Fatal("invalid database identity accepted")
		}
	}
	for _, id := range []string{"gitea", "metabase", "open-webui"} {
		if _, err := PlanTemplate(id, TemplateOptions{Name: "app", Model: "test-model", SiteURL: "https://credential@example.test/path"}); err == nil {
			t.Fatal("credential URL accepted", id)
		}
	}
	a, err := PlanTemplate("vllm", TemplateOptions{Name: "model", Model: "owner/model", ModelRevision: strings.Repeat("a", 40), UseModelToken: true})
	if err != nil || a.Services["main"].Secrets["HF_TOKEN"].Ref != "model-token" {
		t.Fatal("model access token reference lost", err)
	}
}

func TestTemplateCredentialFormatsAndRelationships(t *testing.T) {
	password := "a password/with@url:+characters"
	for _, tc := range []struct{ id, name, value string }{
		{"postgresql", "database-password", password},
		{"infisical", "encryption-key", strings.Repeat("a", 32)},
		{"infisical", "auth-secret", base64.StdEncoding.EncodeToString(make([]byte, 32))},
		{"flowise", "token-signing-secret", strings.Repeat("s", 32)},
		{"open-webui", "provider-key", "provider-issued-credential"},
	} {
		if err := ValidateTemplateSecret(tc.id, tc.name, tc.value); err != nil {
			t.Fatal(err)
		}
		if err := ValidateTemplateSecret(tc.id, tc.name, tc.value+"\n"); err == nil {
			t.Fatal("multiline credential accepted", tc.name)
		}
	}
	for _, tc := range []struct{ id, name, value string }{
		{"mysql", "database-password", "short"}, {"infisical", "encryption-key", strings.Repeat("z", 32)},
		{"infisical", "auth-secret", strings.Repeat("a", 32)}, {"flowise", "session-secret", "tiny"},
		{"open-webui", "provider-key", "one;two"}, {"cockroachdb", "database-node-key", "private contents"},
	} {
		if err := ValidateTemplateSecret(tc.id, tc.name, tc.value); err == nil || strings.Contains(err.Error(), tc.value) {
			t.Fatal("invalid value accepted or echoed", tc.name)
		}
	}
	u := url.URL{Scheme: "postgresql", Host: "db:5432", Path: "/app", User: url.UserPassword("hakopod", password)}
	r := url.URL{Scheme: "redis", Host: "redis:6379", User: url.UserPassword("", password)}
	values := map[string]string{"database-password": password, "redis-password": password, "database-url": u.String(), "redis-url": r.String(), "encryption-key": strings.Repeat("a", 32), "auth-secret": base64.StdEncoding.EncodeToString(make([]byte, 32))}
	required := []string{"database-password", "redis-password", "database-url", "redis-url", "encryption-key", "auth-secret"}
	if err := ValidateTemplateSecretSet("infisical", required, values); err != nil {
		t.Fatal(err)
	}
	values["database-password"] = "a different database password"
	if err := ValidateTemplateSecretSet("infisical", required, values); err == nil || strings.Contains(err.Error(), password) {
		t.Fatal("mismatched URL accepted or password echoed")
	}
	if err := ValidateTemplateSecretSet("mysql", []string{"database-password", "database-root-password"}, map[string]string{"database-password": password, "database-root-password": password}); err == nil {
		t.Fatal("shared root/application password accepted")
	}
	if err := ValidateTemplateSecretSet("cockroachdb", nil, nil); err == nil {
		t.Fatal("missing certificate bundle accepted")
	}
}

func TestTemplateCockroachCertificateBundle(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fixture CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "node"}, DNSNames: []string{"main", "localhost"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	encode := func(kind string, data []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data}))
	}
	keyDER, err := x509.MarshalECPrivateKey(nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"database-ca": encode("CERTIFICATE", caDER), "database-node-key": encode("EC PRIVATE KEY", keyDER)}
	required := []string{"database-ca", "database-node-cert", "database-node-key"}
	for _, invalid := range []string{"", "hostname", "expired", "principal", "usage"} {
		copy := *node
		switch invalid {
		case "hostname":
			copy.DNSNames = []string{"elsewhere"}
		case "expired":
			copy.NotAfter = time.Now().Add(-time.Minute)
		case "principal":
			copy.Subject.CommonName = "root"
		case "usage":
			copy.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, &copy, ca, &nodeKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		values["database-node-cert"] = encode("CERTIFICATE", der)
		err = ValidateTemplateSecretSet("cockroachdb", required, values)
		if (invalid == "") != (err == nil) {
			t.Fatal("incorrect certificate decision", invalid, err)
		}
	}
}

func catalogTestValues(template Template) map[string]string {
	values := map[string]string{}
	for _, f := range template.ConfigFields {
		if f.Default == "" && f.Required {
			values[f.Name] = "operator@example.test"
			if f.Name == "storage-endpoint" {
				values[f.Name] = "https://s3.example.test"
			}
			if f.Name == "storage-bucket" {
				values[f.Name] = "catalog-media"
			}
		}
	}
	return values
}

func TestTemplateComputeRequirementsDescribeWorkloads(t *testing.T) {
	byID := map[string]Template{}
	for _, item := range Templates() {
		if item.WorkloadRequirements == nil {
			t.Fatalf("template %s has null workload requirements", item.ID)
		}
		byID[item.ID] = item
	}
	found := false
	for _, required := range byID["postgresql"].WorkloadRequirements {
		if required == "persistent_storage" {
			found = true
		}
	}
	if !found {
		t.Fatal("PostgreSQL must disclose its persistent storage requirement before configuration")
	}
	nginx, exists := byID["nginx"]
	if !exists || len(nginx.WorkloadRequirements) != 0 {
		t.Fatalf("stateless nginx unexpectedly gated: %v", nginx.WorkloadRequirements)
	}
}

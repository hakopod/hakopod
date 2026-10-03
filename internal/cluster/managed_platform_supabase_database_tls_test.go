package cluster

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func databaseTLSFixture(t *testing.T, names []string, selfSignedLeaf bool) map[string][]byte {
	t.Helper()
	now := time.Now()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "acceptance CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	parent, signer := caTemplate, caKey
	if selfSignedLeaf {
		parent, signer = leaf, leafKey
	}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leaf, parent, &leafKey.PublicKey, signer)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(leafKey)
	return map[string][]byte{"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), "tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), "ca.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})}
}

func TestValidateSupabaseDatabaseTLSRequiresOwnedNamesAndSeparateCA(t *testing.T) {
	namespace := "managed-platform-0123456789abcdef0123456789abcdef"
	valid := databaseTLSFixture(t, []string{"db", "db." + namespace + ".svc"}, false)
	if err := validateSupabaseDatabaseTLS(valid, namespace); err != nil {
		t.Fatal(err)
	}
	chain := make(map[string][]byte, len(valid))
	for key, value := range valid {
		chain[key] = append([]byte(nil), value...)
	}
	chain["tls.crt"] = append(chain["tls.crt"], chain["ca.crt"]...)
	if err := validateSupabaseDatabaseTLS(chain, namespace); err == nil {
		t.Fatal("multi-certificate leaf bundle accepted")
	}
	if err := validateSupabaseDatabaseTLS(databaseTLSFixture(t, []string{"db"}, false), namespace); err == nil {
		t.Fatal("missing owned service FQDN accepted")
	}
	if err := validateSupabaseDatabaseTLS(databaseTLSFixture(t, []string{"db", "db." + namespace + ".svc"}, true), namespace); err == nil {
		t.Fatal("self-signed leaf accepted as database identity")
	}
}

func TestValidateSupabaseDatabaseClientURLsRequiresVerifyFull(t *testing.T) {
	spec := managedplatform.Spec{Supabase: &managedplatform.SupabaseConfig{DatabaseName: "postgres"}, Secrets: map[string]managedplatform.SecretReference{}}
	values := map[string]map[string][]byte{}
	roles := map[string]string{"auth-database-url": "supabase_auth_admin", "rest-database-url": "authenticator", "storage-database-url": "supabase_storage_admin", "supavisor-database-url": "pgbouncer"}
	for key, role := range roles {
		spec.Secrets[key] = managedplatform.SecretReference{Name: key, Revision: 1}
		database := "postgres"
		if key == "supavisor-database-url" {
			database = "_supabase"
		}
		values[key+"-r1"] = map[string][]byte{"value": []byte("postgresql://" + role + ":password@db:5432/" + database + "?sslmode=verify-full&sslrootcert=%2Fetc%2Fhakopod-database-ca%2Fca.crt")}
	}
	if err := validateSupabaseDatabaseClientURLs(values, spec); err != nil {
		t.Fatal(err)
	}
	valid := string(values["auth-database-url-r1"]["value"])
	unsafe := []string{
		"postgresql://supabase_auth_admin:password@db:5432/postgres?sslmode=require&sslrootcert=%2Fetc%2Fhakopod-database-ca%2Fca.crt",
		valid + "&sslmode=require",
		valid + "&sslrootcert=%2Fwrong",
		valid + "&host=elsewhere",
		valid + "&hostaddr=127.0.0.1",
		valid + "&port=6543",
		valid + "&service=other",
		valid + "&sslcert=%2Ftmp%2Fclient.crt",
		valid + "#fragment",
		"postgresql://wrong_role:password@db:5432/postgres?sslmode=verify-full&sslrootcert=%2Fetc%2Fhakopod-database-ca%2Fca.crt",
		"postgresql://supabase_auth_admin:password@db:5432/other?sslmode=verify-full&sslrootcert=%2Fetc%2Fhakopod-database-ca%2Fca.crt",
		"postgresql://supabase_auth_admin:password@127.0.0.1:5432/postgres?sslmode=verify-full&sslrootcert=%2Fetc%2Fhakopod-database-ca%2Fca.crt",
	}
	for _, bad := range unsafe {
		values["auth-database-url-r1"]["value"] = []byte(bad)
		if err := validateSupabaseDatabaseClientURLs(values, spec); err == nil {
			t.Fatalf("unsafe database URL accepted: %s", bad)
		}
	}
	values["auth-database-url-r1"]["value"] = []byte(valid)
	pooler := values["supavisor-database-url-r1"]["value"]
	for _, bad := range []string{
		strings.Replace(string(pooler), "/_supabase?", "/postgres?", 1),
		strings.Replace(string(pooler), "pgbouncer:", "postgres:", 1),
		strings.Replace(string(pooler), "@db:5432", "@database:5432", 1),
		strings.Replace(string(pooler), "sslmode=verify-full", "sslmode=require", 1),
	} {
		values["supavisor-database-url-r1"]["value"] = []byte(bad)
		err := validateSupabaseDatabaseClientURLs(values, spec)
		if err == nil || !strings.Contains(err.Error(), "supavisor-database-url") || strings.Contains(err.Error(), "password") {
			t.Fatalf("unsafe Supavisor URL did not produce a redacted field-specific error: %v", err)
		}
	}
}

func TestValidateSupabaseRuntimeSecretsRequiresExactRealtimeKey(t *testing.T) {
	spec := managedplatform.Spec{Secrets: map[string]managedplatform.SecretReference{"realtime-db-encryption-key": {Name: "realtime-db-encryption-key", Revision: 1}}}
	values := map[string]map[string][]byte{"realtime-db-encryption-key-r1": {"value": []byte(strings.Repeat("x", 32))}}
	if err := validateSupabaseRuntimeSecrets(values, spec); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string][]byte{
		{"value": []byte(strings.Repeat("secret-marker-", 3))},
		{"value": []byte(strings.Repeat("x", 32)), "extra": []byte("secret-marker")},
		{},
	} {
		values["realtime-db-encryption-key-r1"] = bad
		err := validateSupabaseRuntimeSecrets(values, spec)
		if err == nil || !strings.Contains(err.Error(), "realtime-db-encryption-key") || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("invalid Realtime key did not produce a redacted field-specific error: %v", err)
		}
	}
}

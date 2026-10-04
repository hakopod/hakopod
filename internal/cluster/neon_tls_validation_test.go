package cluster

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestValidateNeonTLSSecretSnapshotsRequiresOwnedNamesAndSharedBrokerTrust(t *testing.T) {
	platformID := "0123456789abcdef0123456789abcdef"
	namespace := "managed-platform-" + platformID
	names := []string{
		"neon-broker", "neon-broker." + namespace + ".svc",
		"neon-storage-controller", "neon-storage-controller." + namespace + ".svc",
		"neon-controller-database", "neon-controller-database." + namespace + ".svc",
		"neon-proxy", "neon-proxy." + namespace + ".svc",
		"neon-pageserver-0", "neon-pageserver-0." + namespace + ".svc",
		"neon-pageserver-1", "neon-pageserver-1." + namespace + ".svc",
		"neon-safekeeper-0", "neon-safekeeper-0." + namespace + ".svc",
		"neon-safekeeper-1", "neon-safekeeper-1." + namespace + ".svc",
		"neon-safekeeper-2", "neon-safekeeper-2." + namespace + ".svc",
		"neon-compute-0-control", "neon-compute-0-control." + namespace + ".svc",
	}
	identity := databaseTLSFixture(t, names, false)
	spec := managedplatform.Spec{Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3}, Secrets: map[string]managedplatform.SecretReference{}}
	values := map[string]map[string][]byte{}
	for _, logical := range []string{"broker-auth", "compute-auth", "controller-auth", "controller-database-password", "pageserver-auth", "proxy-auth", "safekeeper-auth"} {
		spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		values[logical+"-r1"] = map[string][]byte{"tls.crt": identity["tls.crt"], "tls.key": identity["tls.key"], "ca.crt": identity["ca.crt"]}
	}
	if err := normalizeNeonComputeTLSKey(values["compute-auth-r1"]); err != nil {
		t.Fatal(err)
	}
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err != nil {
		t.Fatal(err)
	}
	wrongName := databaseTLSFixture(t, []string{"wrong.internal"}, false)
	values["controller-database-password-r1"] = wrongName
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err == nil {
		t.Fatal("controller database certificate without owned service names was accepted")
	}
	values["controller-database-password-r1"] = identity
	wrongCA := databaseTLSFixture(t, names, false)
	values["pageserver-auth-r1"] = wrongCA
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err == nil {
		t.Fatal("broker certificate outside the pageserver trust bundle was accepted")
	}
	values["pageserver-auth-r1"] = identity
	values["broker-auth-r1"] = map[string][]byte{"tls.crt": identity["tls.crt"], "tls.key": wrongCA["tls.key"], "ca.crt": identity["ca.crt"]}
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err == nil {
		t.Fatal("mismatched broker private key was accepted")
	}
}

func TestNeonComputeTLSKeyEncodingPreservesIdentityAndRotatesSnapshot(t *testing.T) {
	data := databaseTLSFixture(t, []string{"neon-compute-0-control"}, false)
	priorKey := append([]byte(nil), data["tls.key"]...)
	priorCertificate := append([]byte(nil), data["tls.crt"]...)
	priorName := platformTLSSecretName("compute-auth", data)
	priorBlock, _ := pem.Decode(priorKey)
	parsed, err := x509.ParsePKCS8PrivateKey(priorBlock.Bytes)
	priorPrivate, ok := parsed.(*ecdsa.PrivateKey)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = normalizeNeonComputeTLSKey(data); err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(data["tls.key"])
	if block == nil || block.Type != "EC PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("compute key was not encoded as one SEC1 PEM block")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil || !key.PublicKey.Equal(&priorPrivate.PublicKey) {
		t.Fatal("compute key identity changed during encoding")
	}
	if !bytes.Equal(data["tls.crt"], priorCertificate) || platformTLSSecretName("compute-auth", data) == priorName {
		t.Fatal("compute snapshot conversion changed its certificate or retained its old content identity")
	}
	normalized := append([]byte(nil), data["tls.key"]...)
	if err = normalizeNeonComputeTLSKey(data); err != nil || !bytes.Equal(data["tls.key"], normalized) {
		t.Fatal("supplied SEC1 compute key was not preserved")
	}
	if _, err = tls.X509KeyPair(data["tls.crt"], data["tls.key"]); err != nil {
		t.Fatal("converted key no longer matches its certificate", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	invalid := map[string][]byte{"tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})}
	if err = normalizeNeonComputeTLSKey(invalid); err == nil {
		t.Fatal("unsupported RSA compute key was accepted")
	}
	for _, rejected := range []map[string][]byte{{"tls.key": []byte("not a PEM key")}, {"tls.key": p384PKCS8(t)}} {
		before := append([]byte(nil), rejected["tls.key"]...)
		if err = normalizeNeonComputeTLSKey(rejected); err == nil || !bytes.Equal(rejected["tls.key"], before) {
			t.Fatal("invalid compute key was accepted or changed")
		}
	}
}

func p384PKCS8(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

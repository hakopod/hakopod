package cluster

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Expiry injection is limited to a freshly created development fixture. Keys
// stay in memory and never enter a command, log or acceptance artifact.
func expireFixturePostgresCA(t *testing.T, ctx context.Context, c *Client, d database.Resource) {
	t.Helper()
	if !strings.Contains(d.Spec.Name, "development-fixture") || !d.Spec.TLSRequired() {
		t.Fatal("certificate expiry injection requires a secure development fixture")
	}
	object, err := c.dynamic.Resource(pgDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetLabels()[databaseOwner] != d.ID {
		t.Fatal("fixture certificate owner missing")
	}
	name, _, _ := unstructured.NestedString(object.Object, "status", "certificates", "serverCASecret")
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owned := false
	for _, owner := range secret.OwnerReferences {
		owned = owned || owner.UID == object.GetUID()
	}
	if !owned {
		t.Fatal("refusing to alter unowned certificate")
	}
	block, _ := pem.Decode(secret.Data["ca.crt"])
	keyBlock, _ := pem.Decode(secret.Data["ca.key"])
	if block == nil || keyBlock == nil {
		t.Fatal("fixture certificate unavailable")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("fixture certificate invalid")
	}
	var key any
	switch keyBlock.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(keyBlock.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	}
	signer, ok := key.(crypto.Signer)
	if err != nil || !ok {
		t.Fatal("fixture signer unavailable")
	}
	ca.NotBefore = time.Now().Add(-time.Hour)
	ca.NotAfter = time.Now().Add(5 * time.Minute)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal("fixture certificate signing failed")
	}
	secret.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = c.kube.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedPostgresTLSRenewalLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_TLS_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_TLS_TEST=1")
	}
	c, ctx := liveRecoveryClient(t)
	d, o := newRecoveryFixture(t, ctx, c, "postgresql", "17")
	if o.TLS == nil || !o.TLS.Verified || !o.TLS.PlaintextRejected {
		t.Fatal("TLS runtime verification missing")
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	host := o.Endpoints[0].Host
	// Keep hostaddr directed at the real service while changing only the peer
	// identity. A DNS lookup failure would not test hostname verification.
	probe := `set -eu
export LC_ALL=C PGCONNECT_TIMEOUT=3
IFS= read -r PGPASSWORD; export PGPASSWORD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
export PGSSLMODE=verify-full PGSSLROOTCERT="$work/ca.crt"
address=$(getent ahostsv4 "$1" | awk 'NR==1 {print $1}')
test -n "$address"
if PGHOST=wrong-hostname.invalid PGHOSTADDR="$address" psql -XAtw -U app -d app -c 'SELECT 1' > /dev/null 2> "$work/error"; then exit 1; fi
grep -F 'does not match host name' "$work/error" > /dev/null
printf 'hostname-rejected'
`
	input := bytes.Join([][]byte{secret.Data["password"], []byte(trust.CertificatePEM)}, []byte{'\n'})
	out := &databaseBoundedWriter{limit: 4096}
	if err = c.DatabaseExec(ctx, d, o.Members[0], []string{"sh", "-c", probe, "wrong-host-probe", host}, bytes.NewReader(input), out); err != nil || out.String() != "hostname-rejected" {
		t.Fatal("wrong hostname was not specifically rejected")
	}
	wrongCA := trustFixtureCA(t)
	probe = `set -eu
export LC_ALL=C PGCONNECT_TIMEOUT=3
IFS= read -r PGPASSWORD; export PGPASSWORD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
if PGSSLMODE=verify-full PGSSLROOTCERT="$work/ca.crt" psql -XAtw -h "$1" -U app -d app -c 'SELECT 1' > /dev/null 2> "$work/error"; then exit 1; fi
grep -F 'certificate verify failed' "$work/error" > /dev/null
printf 'issuer-rejected'
`
	out = &databaseBoundedWriter{limit: 4096}
	input = bytes.Join([][]byte{secret.Data["password"], []byte(wrongCA)}, []byte{'\n'})
	if err = c.DatabaseExec(ctx, d, o.Members[0], []string{"sh", "-c", probe, "wrong-ca-probe", host}, bytes.NewReader(input), out); err != nil || out.String() != "issuer-rejected" {
		t.Fatal("wrong CA was not specifically rejected")
	}
	expireFixturePostgresCA(t, ctx, c, d)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		next, err := c.DatabaseTrust(ctx, d)
		if err == nil && next.Fingerprint != trust.Fingerprint && next.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
			o = waitManagedDatabase(t, ctx, c, d)
			if o.TLS == nil || !o.TLS.Verified || o.TLS.CAFingerprint != next.Fingerprint {
				t.Fatal("renewed CA was not verified in use")
			}
			t.Log("Verified issuer and hostname rejection, required client TLS, plaintext rejection and automatic CA renewal in the named development cluster")
			return
		}
		if sleepContext(ctx, 2*time.Second) != nil {
			break
		}
	}
	t.Fatal("automatic database CA renewal was not observed")
}

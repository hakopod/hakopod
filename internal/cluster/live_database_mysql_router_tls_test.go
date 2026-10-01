package cluster

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This test changes only a fresh standalone fixture's in-memory TLS settings.
// Router's original client certificate and CA mount remain unchanged, so a
// refused query exercises the Router-to-server boundary rather than client TLS.
func TestManagedMySQLRouterBackendTLSLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYSQL_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYSQL_TEST=1")
	}
	if os.Getenv("HAKOPOD_MYSQL_FIXTURE_ID") != "" {
		t.Fatal("Router TLS fault acceptance requires a fresh standalone fixture")
	}
	c, ctx := liveRecoveryClient(t, 15*time.Minute)
	d, password := newMySQLFixture(t, ctx, c, "standalone")
	o := waitMySQLFixture(t, ctx, c, d, password)
	if len(o.Members) != 1 || o.Routing == nil || len(o.Routing.Members) != 1 {
		t.Fatal("Router TLS fixture is not standalone")
	}
	server, router := o.Members[0], o.Routing.Members[0]
	verifyMySQLRouterTLSConfiguration(t, ctx, c, d, router)
	if got, err := mysqlFixtureQuery(ctx, c, d, o, password, "read_write", "SELECT 1"); err != nil || got != "1" {
		t.Fatal("normal Router connection failed before the certificate faults")
	}
	paths := &databaseBoundedWriter{limit: 2048}
	if err := c.DatabaseExec(ctx, d, server, mysqlLocalCommand("SELECT JSON_OBJECT('cert',@@global.ssl_cert,'key',@@global.ssl_key,'ca',@@global.ssl_ca)"), nil, paths); err != nil {
		t.Fatal("original server certificate paths are unavailable")
	}
	var original struct{ Cert, Key, CA string }
	if json.Unmarshal(bytes.TrimSpace(paths.Bytes()), &original) != nil || original.Cert == "" || original.Key == "" || original.CA == "" {
		t.Fatal("original server certificate paths are invalid")
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	reload := func(step context.Context, cert, key, ca string) error {
		sql := "SET SESSION sql_mode='NO_BACKSLASH_ESCAPES'; SET GLOBAL ssl_cert=" + quote(cert) + "; SET GLOBAL ssl_key=" + quote(key) + "; SET GLOBAL ssl_ca=" + quote(ca) + "; ALTER INSTANCE RELOAD TLS"
		out := &databaseBoundedWriter{limit: 2048}
		command := append([]string{"sh", "-c", `"$@" 2>&1`, "tls-reload"}, mysqlLocalCommand(sql)...)
		err := c.DatabaseExec(step, d, server, command, nil, out)
		if err != nil {
			// This command contains only fixture certificate paths, never a
			// password, private key body or application query.
			t.Log("TLS reload diagnostic:", out.String())
		}
		return err
	}
	// Register after fixture cleanup so settings are restored before deletion,
	// even when an assertion fails or the main test context expires.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := reload(cleanup, original.Cert, original.Key, original.CA); err != nil {
			t.Error("could not restore the standalone fixture's original TLS settings")
		}
		if err := c.DatabaseExec(cleanup, d, server, []string{"sh", "-c", "rm -rf /tmp/hakopod-router-tls-acceptance"}, nil, io.Discard); err != nil {
			t.Error("could not remove the standalone TLS fixture files")
		}
	})
	root, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil || root.Labels[databaseOwner] != d.ID || root.Labels[managedBy] != "hakopod" {
		t.Fatal("owned fixture issuer is unavailable")
	}
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil || len(pair.Certificate) != 1 {
		t.Fatal("fixture issuer is invalid")
	}
	trustedCA, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal("fixture issuer certificate is invalid")
	}
	trustedSigner, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		t.Fatal("fixture issuer signer is invalid")
	}
	untrustedKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	untrustedCA := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Unrelated Router acceptance issuer"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	untrustedDER, err := x509.CreateCertificate(rand.Reader, untrustedCA, untrustedCA, untrustedKey.Public(), untrustedKey)
	if err != nil {
		t.Fatal(err)
	}
	untrustedCA, err = x509.ParseCertificate(untrustedDER)
	if err != nil {
		t.Fatal(err)
	}
	host := server.Name + ".database-instances." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
	for _, fault := range []struct {
		name, host string
		ca         *x509.Certificate
		signer     crypto.Signer
	}{
		{"untrusted issuer", host, untrustedCA, untrustedKey},
		{"hostname mismatch", "wrong-host.invalid", trustedCA, trustedSigner},
	} {
		t.Run(fault.name, func(t *testing.T) {
			beforeLogs, beforeRestarts := mysqlRouterFaultLogs(t, ctx, c, d, router)
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			leaf := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: []string{fault.host}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, err := x509.CreateCertificate(rand.Reader, leaf, fault.ca, key.Public(), fault.signer)
			if err != nil {
				t.Fatal(err)
			}
			private, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
			// Input carries the key; neither arguments nor test output expose it.
			stage := `set -eu; umask 077; mkdir -p /tmp/hakopod-router-tls-acceptance; awk '/BEGIN PRIVATE KEY/{key=1} {if(key) print > "/tmp/hakopod-router-tls-acceptance/server.key"; else print > "/tmp/hakopod-router-tls-acceptance/server.crt"}'`
			if err := c.DatabaseExec(ctx, d, server, []string{"sh", "-c", stage}, bytes.NewReader(append(certPEM, keyPEM...)), io.Discard); err != nil {
				t.Fatal("could not stage the standalone certificate fault")
			}
			// MySQL verifies its own configured chain while reloading TLS.
			// Give this standalone server the fault issuer so it stays available;
			// Router retains the original, trusted CA and must reject that issuer.
			faultCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fault.ca.Raw})
			if err := c.DatabaseExec(ctx, d, server, []string{"sh", "-c", "umask 077; cat > /tmp/hakopod-router-tls-acceptance/ca.crt"}, bytes.NewReader(faultCA), io.Discard); err != nil {
				t.Fatal("could not stage the standalone fault issuer")
			}
			if err := reload(ctx, "/tmp/hakopod-router-tls-acceptance/server.crt", "/tmp/hakopod-router-tls-acceptance/server.key", "/tmp/hakopod-router-tls-acceptance/ca.crt"); err != nil {
				t.Fatal("could not reload the standalone certificate fault")
			}
			// Encryption-only direct access is intentionally restricted to this
			// negative test: it proves the server and account still answer.
			direct := `set -eu; IFS= read -r MYSQL_PWD; export MYSQL_PWD; mysql --no-defaults --protocol=TCP --host="$1" --port=3306 --user=app --database=app --ssl-mode=REQUIRED --connect-timeout=3 --batch --skip-column-names --execute='SELECT 1'`
			answer := &databaseBoundedWriter{limit: 32}
			if err := c.DatabaseExec(ctx, d, server, []string{"sh", "-c", direct, "router-backend-control", host}, bytes.NewReader(append(append([]byte{}, password...), '\n')), answer); err != nil || strings.TrimSpace(answer.String()) != "1" {
				t.Fatal("certificate fault also broke direct database availability")
			}
			// Router can retain an authenticated backend connection after the
			// client disconnects. Terminate only this fixture's application
			// sessions so the next query must establish a new TLS connection.
			connections := &databaseBoundedWriter{limit: 2048}
			if err := c.DatabaseExec(ctx, d, server, mysqlLocalCommand("SELECT ID FROM information_schema.PROCESSLIST WHERE USER='app' ORDER BY ID LIMIT 101"), nil, connections); err != nil {
				t.Fatal("could not inspect fixture application sessions")
			}
			ids := strings.Fields(connections.String())
			if len(ids) > 100 {
				t.Fatal("fixture application session count exceeded its bound")
			}
			for _, value := range ids {
				id, err := strconv.ParseUint(value, 10, 64)
				if err != nil || id == 0 {
					t.Fatal("invalid fixture application session identity")
				}
				if err := c.DatabaseExec(ctx, d, server, mysqlLocalCommand("KILL CONNECTION "+strconv.FormatUint(id, 10)), nil, io.Discard); err != nil {
					t.Fatal("could not terminate a fixture application session")
				}
			}
			// Router can close its listener when metadata certificate validation
			// fails. Require both a refused query and certificate-specific evidence
			// from either the client or new logs from this exact Router process.
			trust, err := c.DatabaseTrust(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			refuse := `set -eu
IFS= read -r MYSQL_PWD; export MYSQL_PWD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
if mysql --no-defaults --protocol=TCP --host="$1" --port=6446 --user=app --database=app --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --connect-timeout=5 --execute='SELECT 1' >/dev/null 2>"$work/error"; then printf 'Unexpected query success\n'; exit 1; fi
head -c 2048 "$work/error"`
			input := bytes.Join([][]byte{password, []byte(trust.CertificatePEM)}, []byte{'\n'})
			refusal := &databaseBoundedWriter{limit: 2048}
			if err := c.DatabaseExec(ctx, d, server, []string{"sh", "-c", refuse, "router-backend-refusal", o.Endpoints[0].Host}, bytes.NewReader(input), refusal); err != nil {
				t.Log("Router certificate-refusal diagnostic:", refusal.String())
				t.Fatal("Router did not prove a backend certificate refusal")
			}
			if !mysqlRouterCertificateFailure(refusal.Bytes()) {
				// Do not accept a timeout, DNS error or authentication failure.
				if !strings.Contains(refusal.String(), "ERROR 2003") || !strings.Contains(refusal.String(), "(111)") {
					t.Fatal("Router refusal was neither a certificate error nor a closed listener")
				}
				afterLogs, afterRestarts := mysqlRouterFaultLogs(t, ctx, c, d, router)
				if beforeRestarts != afterRestarts || !bytes.HasPrefix(afterLogs, beforeLogs) {
					t.Fatal("Router restarted or its logs changed during certificate fault acceptance")
				}
				if !mysqlRouterCertificateFailure(afterLogs[len(beforeLogs):]) {
					t.Fatal("closed Router listener has no new certificate-verification failure evidence")
				}
				t.Log("Router refused the query and recorded a new certificate-verification failure before closing its listener")
			}
			if err := reload(ctx, original.Cert, original.Key, original.CA); err != nil {
				t.Fatal("could not restore original server TLS")
			}
			deadline := time.Now().Add(30 * time.Second)
			for {
				if got, err := mysqlFixtureQuery(ctx, c, d, o, password, "read_write", "SELECT 1"); err == nil && got == "1" {
					break
				}
				if time.Now().After(deadline) || sleepContext(ctx, time.Second) != nil {
					t.Fatal("Router did not recover after restoring the original certificate")
				}
			}
		})
		if t.Failed() {
			break
		}
	}
}

func mysqlRouterCertificateFailure(data []byte) bool {
	failure := regexp.MustCompile(`(?i)certificate (?:verify failed|verification failed|validation fail(?:ed|ure))|hostname mismatch|IP address mismatch|unable to get local issuer certificate|self[- ]signed certificate`)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		lower := bytes.ToLower(line)
		if (bytes.Contains(lower, []byte("ssl")) || bytes.Contains(lower, []byte("tls"))) && failure.Match(line) {
			return true
		}
	}
	return false
}

func TestMySQLRouterCertificateFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		text   string
		failed bool
	}{
		{"ERROR 2026 (HY000): SSL connection error: SSL certificate validation failure", true},
		{"metadata_cache ERROR TLS connection failed: certificate verify failed", true},
		{"SSL certificate verified successfully", false},
		{"TLS enabled; certificate verification configured", false},
		{"ERROR 2003: connection refused", false},
		{"certificate verify failed\nSSL connected", false},
	} {
		if got := mysqlRouterCertificateFailure([]byte(tc.text)); got != tc.failed {
			t.Errorf("certificate evidence %q: got %v, want %v", tc.text, got, tc.failed)
		}
	}
}

// Keep complete, bounded logs in memory without printing metadata or credentials.
// Comparing the prefix excludes errors emitted during an earlier fault.
func mysqlRouterFaultLogs(t *testing.T, ctx context.Context, c *Client, d database.Resource, member database.Member) ([]byte, int32) {
	t.Helper()
	step, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(step, member.Name, metav1.GetOptions{})
	if err != nil || string(pod.UID) != member.UID || pod.DeletionTimestamp != nil || pod.Labels[databaseOwner] != d.ID || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != mysqlRouterImage {
		t.Fatal("observed Router identity changed during certificate fault acceptance")
	}
	var restarts int32
	found := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "router" && status.State.Running != nil {
			restarts, found = status.RestartCount, true
		}
	}
	if !found {
		t.Fatal("observed Router process is not running")
	}
	limit := int64(1 << 20)
	logs, err := c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "router", LimitBytes: &limit, Timestamps: true}).DoRaw(step)
	if err != nil || len(logs) >= int(limit) {
		t.Fatal("complete bounded Router fault logs are unavailable")
	}
	return logs, restarts
}

func verifyMySQLRouterTLSConfiguration(t *testing.T, ctx context.Context, c *Client, d database.Resource, member database.Member) {
	t.Helper()
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || string(pod.UID) != member.UID || pod.DeletionTimestamp != nil || pod.Labels[databaseOwner] != d.ID || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != mysqlRouterImage {
		t.Fatal("observed Router identity changed before configuration inspection")
	}
	// The normal observation established the Deployment/ReplicaSet owner chain.
	// Only TLS keys are inspected; metadata credentials never leave the pod.
	check := `set -eu
awk -F= '
function trim(s) {gsub(/^[ \t]+|[ \t]+$/, "", s); return s}
/^[ \t]*server_ssl_mode[ \t]*=/ {if(trim($2)!="REQUIRED") exit 1; mode++}
/^[ \t]*server_ssl_verify[ \t]*=/ {if(trim($2)!="VERIFY_IDENTITY") exit 1; verify++}
/^[ \t]*server_ssl_ca[ \t]*=/ {if(trim($2)!="/router-ssl/ca/ca.crt") exit 1; ca++}
END {if(mode<1 || verify<1 || ca<1) exit 1}
' /tmp/mysqlrouter/mysqlrouter.conf
test -s /router-ssl/ca/ca.crt`
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", pod.Namespace, "exec", pod.Name, "-c", "router", "--", "sh", "-c", check)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		// Report only the public TLS policy keys. The full Router configuration
		// contains administrative metadata and must never enter test logs.
		diagnostic := `if test -r /tmp/mysqlrouter/mysqlrouter.conf; then
awk '/^\[/ || /^[ \t]*(server_ssl_mode|server_ssl_verify|server_ssl_ca)[ \t]*=/ {print}' /tmp/mysqlrouter/mysqlrouter.conf
else printf 'Router configuration is unavailable\n'; fi
if test -s /router-ssl/ca/ca.crt; then printf 'Expected CA mount exists\n'; else printf 'Expected CA mount is absent\n'; fi`
		probe := exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", pod.Namespace, "exec", pod.Name, "-c", "router", "--", "sh", "-c", diagnostic)
		out := &databaseBoundedWriter{limit: 4096}
		probe.Stdout, probe.Stderr = out, io.Discard
		if probe.Run() == nil {
			t.Log("Router TLS policy:", out.String())
		}
		t.Fatal("Router effective backend TLS policy or CA mount is invalid")
	}
}

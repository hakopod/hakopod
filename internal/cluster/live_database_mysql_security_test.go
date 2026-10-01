package cluster

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testMySQLCertificateRefusal(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation, password []byte) {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Unrelated development issuer"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, issuer, issuer, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	wrongCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	// A successful native connection was already observed with the same account.
	// These failures must be certificate errors, not an unreachable server.
	script := `set -eu
IFS= read -r MYSQL_PWD; export MYSQL_PWD
umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cat > "$work/ca.crt"
if mysql --no-defaults --protocol=TCP --host="$1" --port="$2" --user=app --database=app --connect-timeout=3 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --execute='SELECT 1' > /dev/null 2> "$work/error"; then exit 1; fi
grep -E 'ERROR 2026.*(SSL|TLS)' "$work/error" >/dev/null
`
	for _, target := range []struct {
		name, host, ca string
		port           int
	}{
		{"untrusted issuer", o.Endpoints[0].Host, wrongCA, o.Endpoints[0].Port},
		{"hostname mismatch", "127.0.0.1", trust.CertificatePEM, 3306},
	} {
		input := bytes.Join([][]byte{password, []byte(target.ca)}, []byte{'\n'})
		if err := c.DatabaseExec(ctx, d, o.Members[0], []string{"sh", "-c", script, "mysql-certificate-refusal", target.host, strconv.Itoa(target.port)}, bytes.NewReader(input), io.Discard); err != nil {
			t.Fatalf("MySQL did not prove rejection of %s", target.name)
		}
	}
	t.Log("MySQL native client rejected an unrelated issuer and an incorrect endpoint hostname")
}

func testMySQLCredentialLogs(t *testing.T, ctx context.Context, c *Client, d database.Resource) {
	t.Helper()
	ns := DatabaseNamespace(d.ID)
	secrets, err := c.kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{Limit: 40})
	if err != nil || secrets.Continue != "" {
		t.Fatal("MySQL log audit credential inventory unavailable")
	}
	var private [][]byte
	for _, secret := range secrets.Items {
		for key, value := range secret.Data {
			if len(value) >= 16 && (strings.Contains(strings.ToLower(key), "password") || strings.HasSuffix(key, ".key")) {
				private = append(private, value)
			}
		}
	}
	if len(private) < 4 {
		t.Fatal("MySQL log audit did not load expected private material")
	}
	audit := func(namespace, selector string) {
		pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 20})
		if err != nil || pods.Continue != "" || len(pods.Items) == 0 {
			t.Fatal("MySQL log audit pod inventory unavailable")
		}
		for _, pod := range pods.Items {
			for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
				limit := int64(4 << 20)
				data, err := c.kube.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name, LimitBytes: &limit}).DoRaw(ctx)
				if err != nil || len(data) >= int(limit) {
					t.Fatal("MySQL log audit could not inspect the complete bounded log")
				}
				for _, value := range private {
					if bytes.Contains(data, value) {
						t.Fatalf("MySQL private material appeared in %s/%s logs", pod.Name, container.Name)
					}
				}
			}
		}
	}
	audit(ns, "")
	audit("mysql-operator", "name=mysql-operator")
	t.Log("MySQL member, initialization, Router and controller logs contain none of this fixture's credential or private-key values")
}

func testMySQLQuorumLoss(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) {
	t.Helper()
	if len(o.Members) != 3 {
		t.Fatal("MySQL quorum-loss fixture needs exactly three voting members")
	}
	primary, err := mysqlObservedPrimary(o)
	if err != nil {
		t.Fatal(err)
	}
	// Container PID 1 ignores SIGSTOP sent inside its own PID namespace. Use
	// the development runtime boundary and verify the actual PAUSED state.
	helper := os.Getenv("HAKOPOD_MYSQL_FAULT_HELPER")
	if helper == "" {
		t.Fatal("set HAKOPOD_MYSQL_FAULT_HELPER to the owned development quorum helper")
	}
	fault := func(ctx context.Context, action string, member database.Member) error {
		command := exec.CommandContext(ctx, "python3", helper, action, d.ID, member.Name, member.UID)
		return command.Run()
	}
	var stopped []database.Member
	resume := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, server := range stopped {
			if err := fault(cleanup, "resume", server); err != nil {
				t.Error("MySQL fixture server could not resume", server.Name)
			}
		}
		stopped = nil
	}
	defer resume()
	for _, member := range o.Members {
		if member.Role != "replica" {
			continue
		}
		stopped = append(stopped, member)
		if err := fault(ctx, "pause", member); err != nil {
			t.Fatal("MySQL fixture server pause failed")
		}
	}
	if len(stopped) != 2 {
		t.Fatal("MySQL fixture has no replica majority")
	}
	// The primary must still answer a local query, but cannot acknowledge a
	// commit without a majority. Timeout means an unknown outcome, not rollback.
	if err := c.DatabaseExec(ctx, d, primary, mysqlLocalCommand("SELECT 1"), nil, io.Discard); err != nil {
		t.Fatal("MySQL primary itself became unreachable")
	}
	script := `set -eu
work=$(mktemp); trap 'rm -f "$work"' EXIT
set +e
timeout 12 mysql --no-defaults --protocol=SOCKET --socket=/var/run/mysqld/mysql.sock --user=localroot --database=app --execute='INSERT INTO scale_check VALUES(99,0x01)' > /dev/null 2> "$work"
status=$?
set -e
printf 'MySQL quorum probe exit status: %s\n' "$status"
if [ "$status" = 124 ]; then exit 0; fi
if [ "$status" != 0 ] && grep -E 'ERROR (1290|3100|3098)' "$work" >/dev/null; then exit 0; fi
exit 1`
	output := &databaseBoundedWriter{limit: 256}
	if err := c.DatabaseExec(ctx, d, primary, []string{"sh", "-c", script}, nil, output); err != nil {
		t.Fatal("MySQL primary did not prove refusal of an acknowledged commit without quorum", output.String())
	}
	resume()
	t.Log("MySQL primary stayed responsive but could not acknowledge a write while both other voting servers were paused")
}

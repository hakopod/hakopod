package cluster

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type catalogCredentialSet struct {
	values                map[string]string
	clientCert, clientKey string
}

func catalogCredentials(t *testing.T, id string) catalogCredentialSet {
	t.Helper()
	result := catalogCredentialSet{values: map[string]string{}}
	if id == "valkey" || id == "redis" || id == "clickhouse" {
		result.values["database-password"] = "catalog-test-password-only"
	}
	if id != "cockroachdb" {
		return result
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable catalog CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	result.values["database-ca"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	certificate := func(cn string, serial int64) (string, string) {
		next, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if cn == "node" {
			cert.DNSNames = []string{"main", "localhost"}
			cert.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
			cert.ExtKeyUsage = append(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
		}
		encoded, err := x509.CreateCertificate(rand.Reader, cert, ca, &next.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded})), string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(next)}))
	}
	result.values["database-node-cert"], result.values["database-node-key"] = certificate("node", 2)
	result.clientCert, result.clientKey = certificate("root", 3)
	return result
}
func verifyExpandedCatalog(t *testing.T, ctx context.Context, c *Client, path string, target *Target, credentials catalogCredentialSet) {
	t.Helper()
	id := strings.TrimPrefix(target.Spec.Name, "catalog-")
	runInput := func(input string, args ...string) (string, error) {
		base := []string{"--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "-i", "deployment/main", "--"}
		command := exec.CommandContext(ctx, "kubectl", append(base, args...)...)
		command.Stdin = strings.NewReader(input)
		out, err := command.CombinedOutput()
		message := strings.ReplaceAll(string(out), "catalog-test-password-only", "[redacted]")
		if len(message) > 4000 {
			message = message[:4000]
		}
		return message, err
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := runInput("", args...)
		if err != nil {
			t.Fatalf("catalog behavior probe failed: %v; %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	var verify func()
	switch id {
	case "redis":
		if got := run("redis-cli", "--raw", "-h", "main", "PING"); !strings.Contains(got, "NOAUTH") {
			t.Fatal("Redis accepted unauthenticated request", got)
		}
		if got := run("sh", "-ec", `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli --raw -h main SET catalog-probe survives-restart`); got != "OK" {
			t.Fatal("Redis SET failed", got)
		}
		verify = func() {
			if got := run("sh", "-ec", `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli --raw -h main GET catalog-probe`); got != "survives-restart" {
				t.Fatal("Redis data lost", got)
			}
		}
	case "clickhouse":
		// The official minimal server image has Bash but no curl. Exercise HTTP
		// through Service DNS with its shell TCP socket, without adding a client pod.
		queryHTTP := func(sql string, authenticated bool) (int, string) {
			t.Helper()
			headers := ""
			if authenticated {
				headers = `printf 'X-ClickHouse-User: default\r\nX-ClickHouse-Key: %s\r\n' "$CLICKHOUSE_PASSWORD" >&3`
			}
			script := `query=$(cat)
exec 3<>/dev/tcp/main/8123
printf 'POST / HTTP/1.0\r\nHost: main\r\n' >&3
` + headers + `
printf 'Content-Length: %d\r\n\r\n%s' "${#query}" "$query" >&3
cat <&3`
			out, err := runInput(sql, "bash", "-ec", script)
			if err != nil {
				t.Fatalf("ClickHouse HTTP probe failed: %v %s", err, out)
			}
			response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(out)), nil)
			if err != nil {
				t.Fatalf("ClickHouse HTTP response was invalid: %v", err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(io.LimitReader(response.Body, 2048))
			if err != nil {
				t.Fatal(err)
			}
			return response.StatusCode, strings.TrimSpace(string(body))
		}
		if status, _ := queryHTTP("SELECT 1", false); status != 401 && status != 403 {
			t.Fatal("ClickHouse accepted unauthenticated query", status)
		}
		query := func(sql string) string {
			t.Helper()
			status, body := queryHTTP(sql, true)
			if status != 200 {
				t.Fatalf("ClickHouse SQL returned HTTP %d: %s", status, body)
			}
			return body
		}
		query("CREATE TABLE catalog_probe(value String) ENGINE=MergeTree ORDER BY tuple()")
		query("INSERT INTO catalog_probe VALUES ('survives-restart')")
		verify = func() {
			if got := query("SELECT value FROM catalog_probe"); got != "survives-restart" {
				t.Fatal("ClickHouse data lost", got)
			}
		}
	case "cockroachdb":
		installCert := func() {
			t.Helper()
			for suffix, contents := range map[string]string{"crt": credentials.clientCert, "key": credentials.clientKey} {
				if _, err := runInput(contents, "sh", "-ec", "umask 077; cat > /tmp/hakopod-certs/client.root."+suffix); err != nil {
					t.Fatal("could not install disposable SQL client certificate")
				}
			}
		}
		installCert()
		if _, err := runInput("", "/cockroach/cockroach", "sql", "--insecure", "--host=main:26257", "--execute=SELECT 1"); err == nil {
			t.Fatal("CockroachDB accepted insecure SQL connection")
		}
		run("sh", "-ec", "umask 077; mkdir -p /tmp/catalog-no-client; cp /tmp/hakopod-certs/ca.crt /tmp/catalog-no-client/ca.crt")
		if _, err := runInput("", "/cockroach/cockroach", "sql", "--certs-dir=/tmp/catalog-no-client", "--host=main:26257", "--user=root", "--execute=SELECT 1"); err == nil {
			t.Fatal("CockroachDB accepted TLS SQL without a client credential")
		}

		query := func(sql string) string {
			t.Helper()
			return run("/cockroach/cockroach", "sql", "--certs-dir=/tmp/hakopod-certs", "--host=main:26257", "--user=root", "--format=csv", "--execute="+sql)
		}
		query("CREATE DATABASE catalog; CREATE TABLE catalog.probe(value STRING); INSERT INTO catalog.probe VALUES ('survives-restart')")
		verify = func() {
			installCert()
			if got := query("SELECT value FROM catalog.probe"); got != "value\nsurvives-restart" {
				t.Fatal("CockroachDB data lost", got)
			}
		}
	default:
		t.Fatal("unsupported verification", id)
	}
	verify()
	claim, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(target.ApplicationID)).Get(ctx, "main-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["main"]
	svc.RestartNonce = "catalog-data-restart"
	target.Spec.Services["main"] = svc
	target.Revision++
	target.OperationID = "catalog-data-restart"
	if _, err = c.Deploy(ctx, *target, nil); err != nil {
		t.Fatal("catalog restart failed", err)
	}
	verify()
	after, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(target.ApplicationID)).Get(ctx, "main-data", metav1.GetOptions{})
	if err != nil || after.UID != claim.UID {
		t.Fatal("database restart replaced PVC", err)
	}
	t.Log(fmt.Sprintf("%s rejects unauthenticated access; authenticated SQL/commands and stored data survive restart on the same PVC", id))
}

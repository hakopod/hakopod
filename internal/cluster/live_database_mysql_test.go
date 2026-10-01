package cluster

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newMySQLFixture(t *testing.T, ctx context.Context, c *Client, mode string) (database.Resource, []byte) {
	t.Helper()
	id, password := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: hex.EncodeToString(id), Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "mysql-development-fixture", Engine: "mysql", Version: "8.4", Mode: mode, Shards: 1, CPU: "1", Memory: "1Gi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}}}
	d.Spec.Placement.NodeNames = developmentRecoveryFixtureNodes(t)
	if mode == "cluster" {
		d.Spec.Replicas = 2
	}
	passwordText := []byte(hex.EncodeToString(password))
	if reuse := os.Getenv("HAKOPOD_MYSQL_FIXTURE_ID"); reuse != "" {
		if raw, err := hex.DecodeString(reuse); err != nil || len(raw) != 16 {
			t.Fatal("invalid development MySQL fixture ID")
		}
		d.ID = reuse
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
			t.Fatal("MySQL development fixture ownership changed")
		}
		passwordText = secret.Data["password"]
	}
	t.Log("Development MySQL namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		// Namespace and volume controllers can need a five-minute resync after
		// the last pod exits. Keep a bounded wait for actual volume reclamation.
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		for cleanup.Err() == nil {
			done, err := c.DeleteDatabase(cleanup, d, func() error { return nil })
			if err != nil {
				t.Error(err)
				return
			}
			if done {
				return
			}
			if sleepContext(cleanup, 2*time.Second) != nil {
				break
			}
		}
		t.Error("MySQL fixture deletion did not finish reclaiming its resources")
	})
	return d, passwordText
}

func waitMySQLFixture(t *testing.T, ctx context.Context, c *Client, d database.Resource, password []byte) database.Observation {
	t.Helper()
	var o database.Observation
	var last error
	for ctx.Err() == nil {
		step, cancel := context.WithTimeout(ctx, 25*time.Second)
		last = c.ApplyDatabase(step, d, password, func() error { return nil })
		if last == nil {
			o, last = c.ObserveDatabase(step, d)
		}
		cancel()
		if last == nil && o.Status == "ready" {
			return o
		}
		t.Log("Waiting for MySQL", o.Message, last)
		if sleepContext(ctx, 5*time.Second) != nil {
			break
		}
	}
	diagnostic, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(diagnostic, metav1.ListOptions{Limit: 16})
	if err == nil {
		for _, pod := range pods.Items {
			// Reasons are Kubernetes enums; avoid echoing arbitrary event or
			// container output into a failure report that may contain secrets.
			t.Log("MySQL readiness failure", pod.Name, "phase", pod.Status.Phase, "reason", pod.Status.Reason)
		}
	}
	t.Fatal("MySQL did not become ready", o.Message, last)
	return o
}

func mysqlFixtureQuery(ctx context.Context, c *Client, d database.Resource, o database.Observation, password []byte, purpose, query string) (string, error) {
	var endpoint database.Endpoint
	for _, e := range o.Endpoints {
		if e.Purpose == purpose {
			endpoint = e
		}
	}
	return mysqlFixtureEndpointQuery(ctx, c, d, o.Members[0], password, endpoint, query)
}

func mysqlFixtureEndpointQuery(ctx context.Context, c *Client, d database.Resource, member database.Member, password []byte, endpoint database.Endpoint, query string) (string, error) {
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		return "", err
	}
	input := bytes.Join([][]byte{password, []byte(trust.CertificatePEM)}, []byte{'\n'})
	script := `set -eu; IFS= read -r MYSQL_PWD; export MYSQL_PWD; umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT; cat > "$work/ca.crt"; mysql --no-defaults --protocol=TCP --host="$1" --port="$2" --user=app --database=app --connect-timeout=3 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --batch --raw --skip-column-names --execute="$3"`
	out := &databaseBoundedWriter{limit: 8192}
	err = c.DatabaseExec(ctx, d, member, []string{"sh", "-c", script, "mysql-fixture-query", endpoint.Host, strconv.Itoa(endpoint.Port), query}, bytes.NewReader(input), out)
	return strings.TrimSpace(out.String()), err
}

func TestManagedMySQLLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYSQL_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYSQL_TEST=1 for named development cluster acceptance")
	}
	c, ctx := liveRecoveryClient(t, 20*time.Minute)
	for _, mode := range []string{"standalone", "cluster"} {
		t.Run(mode, func(t *testing.T) {
			d, password := newMySQLFixture(t, ctx, c, mode)
			health := waitMySQLFixture(t, ctx, c, d, password)
			if health.TLS == nil || !health.TLS.Verified || !health.TLS.PlaintextRejected || health.Routing == nil || !health.Routing.Ready {
				t.Fatal("MySQL transport and routing were not verified")
			}
			if _, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "CREATE TABLE acceptance(id INT PRIMARY KEY, value VARBINARY(16)); INSERT INTO acceptance VALUES (1, 0x000AFF80)"); err != nil {
				t.Fatal(err)
			}
			if got, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "SELECT HEX(value) FROM acceptance WHERE id=1"); err != nil || got != "000AFF80" {
				t.Fatal("MySQL binary value was not retained", err)
			}
			if _, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "CREATE USER forbidden_global_user IDENTIFIED BY 'irrelevant'"); err == nil {
				t.Fatal("application account has global administrative privileges")
			}
			if mode == "cluster" {
				if _, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_only", "INSERT INTO acceptance VALUES (2, 0x01)"); err == nil {
					t.Fatal("replica route accepted a write")
				}
				for _, m := range health.Members {
					if m.Name != health.Primary {
						continue
					}
					pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, m.Name, metav1.GetOptions{})
					if err != nil || string(pod.UID) != m.UID {
						t.Fatal("primary identity changed")
					}
					if err := c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0)), Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
						t.Fatal(err)
					}
				}
				if err := sleepContext(ctx, 3*time.Second); err != nil {
					t.Fatal(err)
				}
				health = waitMySQLFixture(t, ctx, c, d, password)
				if got, err := mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "SELECT HEX(value) FROM acceptance WHERE id=1"); err != nil || got != "000AFF80" {
					t.Fatal("MySQL primary replacement lost data", err)
				}
			}
			testMySQLRenewal(t, ctx, c, d, password, health)
		})
	}
}

func testMySQLRenewal(t *testing.T, ctx context.Context, c *Client, d database.Resource, password []byte, health database.Observation) {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	root, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal("invalid fixture issuer")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal("fixture issuer expiry injection failed")
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = c.kube.CoreV1().Secrets(root.Namespace).Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RenewDatabaseIdentity(ctx, d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Minute)
	var next database.Observation
	for time.Now().Before(deadline) {
		step, cancel := context.WithTimeout(ctx, 25*time.Second)
		next, err = c.ObserveDatabase(step, d)
		cancel()
		if err == nil && next.Status == "ready" && next.TLS != nil && next.TLS.Verified && next.TLS.Fingerprint != health.TLS.Fingerprint && next.TLS.CAFingerprint != trust.Fingerprint {
			break
		}
		if sleepContext(ctx, 5*time.Second) != nil {
			break
		}
	}
	if next.TLS == nil || !next.TLS.Verified || next.TLS.Fingerprint == health.TLS.Fingerprint {
		t.Fatal("MySQL certificate renewal was not observed", err)
	}
	// Existing clients retain valid CA trust through the renewal overlap.
	out := &databaseBoundedWriter{limit: 32 << 10}
	e := next.Endpoints[0]
	if err = c.databaseExecContainer(ctx, d, next.Members[0], "sidecar", []string{"sh", "-c", mysqlCertificateProbe, "old-mysql-trust", e.Host, strconv.Itoa(e.Port)}, strings.NewReader(trust.CertificatePEM), out); err != nil {
		t.Fatal("existing MySQL CA did not trust renewed identity", err)
	}
	if got, err := mysqlFixtureQuery(ctx, c, d, next, password, "read_write", "SELECT HEX(value) FROM acceptance WHERE id=1"); err != nil || got != "000AFF80" {
		t.Fatal("renewal interrupted MySQL data access", err)
	}
	t.Log("MySQL server and Router served renewed certificates; old CA overlap verified")
}

func TestManagedMySQLRecoveryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYSQL_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYSQL_TEST=1")
	}
	if os.Getenv("HAKOPOD_MYSQL_FIXTURE_ID") != "" {
		t.Fatal("MySQL recovery acceptance requires fresh, separate source and target databases")
	}
	c, ctx := liveRecoveryClient(t, 30*time.Minute)
	d, password := newMySQLFixture(t, ctx, c, "cluster")
	// Exercise the supported minimum while retaining both three-member clusters.
	d.Spec.CPU = "500m"
	o := waitMySQLFixture(t, ctx, c, d, password)
	query := func(query string) string {
		t.Helper()
		got, err := mysqlFixtureQuery(ctx, c, d, o, password, "read_write", query)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	query("CREATE TABLE recovery(id INT PRIMARY KEY, value VARBINARY(16)); INSERT INTO recovery VALUES (1, 0x000AFF80); CREATE VIEW recovery_view AS SELECT id, HEX(value) AS value FROM recovery; CREATE PROCEDURE recovery_proc() SELECT COUNT(*) FROM recovery")
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, d, o, archive); err != nil {
		t.Fatal(err)
	}
	query("INSERT INTO recovery VALUES (2, 0x01)")
	target, targetPassword := newMySQLFixture(t, ctx, c, "cluster")
	target.Spec.CPU = "500m"
	targetHealth := waitMySQLFixture(t, ctx, c, target, targetPassword)
	target.Status, target.Recovery = "restoring", &database.Recovery{JobID: d.ID}
	if err := c.RestoreMySQLDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	targetHealth = waitMySQLFixture(t, ctx, c, target, targetPassword)
	if got, err := mysqlFixtureQuery(ctx, c, target, targetHealth, targetPassword, "read_write", "SELECT CONCAT(id, ':', value) FROM recovery_view"); err != nil || got != "1:000AFF80" {
		t.Fatal("MySQL recovery did not preserve data and view", err)
	}
	if got, err := mysqlFixtureQuery(ctx, c, target, targetHealth, targetPassword, "read_write", "CALL recovery_proc()"); err != nil || got != "1" {
		t.Fatal("MySQL recovery did not preserve stored routine", err)
	}
	for _, member := range targetHealth.Members {
		if member.Role != "replica" {
			continue
		}
		endpoint := database.Endpoint{Host: member.Name + ".database-instances." + DatabaseNamespace(target.ID) + ".svc.cluster.local", Port: 3306}
		deadline := time.Now().Add(45 * time.Second)
		for {
			got, err := mysqlFixtureEndpointQuery(ctx, c, target, targetHealth.Members[0], targetPassword, endpoint, "SELECT CONCAT(id, ':', value) FROM recovery_view")
			if err == nil && got == "1:000AFF80" {
				break
			}
			if time.Now().After(deadline) || sleepContext(ctx, time.Second) != nil {
				t.Fatal("MySQL recovery did not reach every replica", member.Name)
			}
		}
		if _, err := mysqlFixtureEndpointQuery(ctx, c, target, targetHealth.Members[0], targetPassword, endpoint, "INSERT INTO recovery VALUES(99, 0x01)"); err == nil {
			t.Fatal("MySQL recovered replica accepted a write", member.Name)
		}
	}
	if query("SELECT COUNT(*) FROM recovery") != "2" {
		t.Fatal("MySQL recovery altered the retained source")
	}
	if c.RestoreMySQLDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes())) == nil {
		t.Fatal("MySQL accepted a nonempty recovery target")
	}
	testRecoveryIngressGates(t, ctx, c, target)
	t.Log("MySQL clustered logical recovery preserved binary data, views and routines; retained source and nonempty-target refusal verified")
}

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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newMyDuckFixture(t *testing.T, ctx context.Context, c *Client) (database.Resource, []byte, database.Observation) {
	t.Helper()
	identity, password := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	d := myduckFixture()
	d.ID, d.Spec.Name = hex.EncodeToString(identity), "myduck-development-fixture"
	d.Spec.Placement.NodeNames = developmentRecoveryFixtureNodes(t)
	secret := []byte(hex.EncodeToString(password))
	t.Log("Development MyDuck namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		for cleanup.Err() == nil {
			done, err := c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if err != nil {
				t.Error("MyDuck fixture cleanup failed", err)
				return
			}
			if done {
				return
			}
			if sleepContext(cleanup, 2*time.Second) != nil {
				break
			}
		}
		t.Error("MyDuck fixture cleanup did not finish")
	})
	if err := c.ApplyDatabase(ctx, d, secret, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	return d, secret, waitMyDuckFixture(t, ctx, c, d)
}

func waitMyDuckFixture(t *testing.T, ctx context.Context, c *Client, d database.Resource) database.Observation {
	t.Helper()
	var observed database.Observation
	var err error
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for wait.Err() == nil {
		step, stop := context.WithTimeout(wait, 30*time.Second)
		observed, err = c.ObserveDatabase(step, d)
		stop()
		if err == nil && observed.Status == "ready" {
			return observed
		}
		t.Log("Waiting for MyDuck", observed.Message, err)
		if sleepContext(wait, 3*time.Second) != nil {
			break
		}
	}
	t.Fatal("MyDuck did not become ready", observed.Message, err)
	return observed
}

func testMyDuckReads(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation, expected string) {
	t.Helper()
	password, identity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	mysql, err := c.myduckMySQLClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer mysql.Close()
	var mysqlValue string
	if err := mysql.QueryRowContext(ctx, "SELECT value FROM acceptance WHERE id=1").Scan(&mysqlValue); err != nil || mysqlValue != expected {
		t.Fatal("MySQL shared data read failed", err)
	}
	postgres, err := c.myduckPostgresClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Close(ctx)
	var pgValue string
	if err := postgres.QueryRow(ctx, "SELECT value FROM acceptance WHERE id=1").Scan(&pgValue); err != nil || pgValue != expected {
		t.Fatal("PostgreSQL shared data read failed", err)
	}
}

func TestManagedMyDuckLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYDUCK_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYDUCK_TEST=1 for named development cluster acceptance")
	}
	c, ctx := liveRecoveryClient(t, 20*time.Minute)
	d, password, observed := newMyDuckFixture(t, ctx, c)
	if len(observed.Endpoints) != 2 || observed.TLS == nil || !observed.TLS.Verified || !observed.TLS.PlaintextRejected {
		t.Fatal("MyDuck did not verify both encrypted protocols")
	}
	_, identity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	mysql, err := c.myduckMySQLClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer mysql.Close()
	if _, err = mysql.ExecContext(ctx, "CREATE TABLE acceptance (id INTEGER PRIMARY KEY, value VARCHAR(255))"); err != nil {
		t.Fatal("MySQL create failed", err)
	}
	if _, err = mysql.ExecContext(ctx, "INSERT INTO acceptance VALUES (1, 'from mysql')"); err != nil {
		t.Fatal("MySQL insert failed", err)
	}
	testMyDuckReads(t, ctx, c, d, observed, "from mysql")
	pg, err := c.myduckPostgresClient(ctx, d, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, "UPDATE acceptance SET value='from postgres' WHERE id=1"); err != nil {
		t.Fatal("PostgreSQL update failed", err)
	}
	_ = pg.Close(ctx)
	testMyDuckReads(t, ctx, c, d, observed, "from postgres")
	for _, query := range []string{
		"SELECT * FROM read_text('/etc/hakopod-app/password')",
		"COPY acceptance TO '/tmp/export.csv'",
		"ATTACH '/tmp/other.db' AS other",
		"INSTALL httpfs", "LOAD httpfs",
		"CREATE USER attacker IDENTIFIED BY 'unsafe'",
		"ALTER USER root IDENTIFIED BY 'unsafe'",
		"RENAME USER root TO attacker",
		"UPDATE mysql.user SET authentication_string=''",
		"SET enable_external_access=true",
		"SET memory_limit='100TiB'",
	} {
		if _, err = mysql.ExecContext(ctx, query); err == nil {
			t.Fatalf("MySQL accepted a forbidden statement: %s", query)
		}
		pg, err := c.myduckPostgresClient(ctx, d, observed.Members[0], password, identity)
		if err != nil {
			t.Fatal(err)
		}
		_, queryErr := pg.Exec(ctx, query)
		_ = pg.Close(ctx)
		if queryErr == nil {
			t.Fatalf("PostgreSQL accepted a forbidden statement: %s", query)
		}
	}
	for _, testcase := range []struct {
		name     string
		password []byte
		tls      *tls.Config
	}{
		{"wrong password", []byte("not-the-managed-password"), identity},
		{"plaintext", password, nil},
		{"wrong hostname", password, func() *tls.Config { config := identity.Clone(); config.ServerName = "wrong.invalid"; return config }()},
		{"wrong CA", password, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: identity.ServerName}},
	} {
		step, cancel := context.WithTimeout(ctx, 12*time.Second)
		client, err := c.myduckMySQLClient(step, d, observed.Members[0], testcase.password, testcase.tls)
		if err == nil {
			err = client.PingContext(step)
			_ = client.Close()
		}
		if err == nil {
			cancel()
			t.Fatal("MySQL accepted", testcase.name)
		}
		conn, pgErr := c.myduckPostgresClient(step, d, observed.Members[0], testcase.password, testcase.tls)
		if pgErr == nil {
			_ = conn.Close(step)
			cancel()
			t.Fatal("PostgreSQL accepted", testcase.name)
		}
		cancel()
	}
	oldUID := observed.Members[0].UID
	pod, _, err := c.databaseExecTarget(ctx, d, observed.Members[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
		t.Fatal(err)
	}
	observed = waitMyDuckFixture(t, ctx, c, d)
	if observed.Members[0].UID == oldUID {
		t.Fatal("MyDuck pod replacement was not observed")
	}
	testMyDuckReads(t, ctx, c, d, observed, "from postgres")
	observed = testMyDuckRenewal(t, ctx, c, d, observed)
	testMyDuckReads(t, ctx, c, d, observed, "from postgres")
	t.Log("Both protocols, authenticated TLS, access refusal, file/network settings and persistent replacement verified")
}

func testMyDuckRenewal(t *testing.T, ctx context.Context, c *Client, d database.Resource, previous database.Observation) database.Observation {
	t.Helper()
	password, oldIdentity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	// Applications trust the database CA and hostname. The control-plane
	// observation additionally pins the current leaf, which changes on renewal.
	oldIdentity = oldIdentity.Clone()
	oldIdentity.VerifyConnection = nil
	issuer, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(issuer.Data["ca.crt"], issuer.Data["ca.key"])
	if err != nil {
		t.Fatal("MyDuck fixture issuer is invalid")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	issuer.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = c.kube.CoreV1().Secrets(issuer.Namespace).Update(ctx, issuer, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for wait.Err() == nil {
		next, observeErr := c.ObserveDatabase(wait, d)
		if observeErr == nil && next.Status == "ready" && len(next.Members) == 1 && next.Members[0].UID != previous.Members[0].UID && next.TLS != nil && next.TLS.Verified && next.TLS.Fingerprint != previous.TLS.Fingerprint && next.TLS.CAFingerprint != previous.TLS.CAFingerprint {
			mysql, err := c.myduckMySQLClient(wait, d, next.Members[0], password, oldIdentity)
			if err != nil {
				t.Fatal(err)
			}
			err = mysql.PingContext(wait)
			_ = mysql.Close()
			if err != nil {
				t.Fatal("MySQL lost existing CA trust during renewal", err)
			}
			pg, err := c.myduckPostgresClient(wait, d, next.Members[0], password, oldIdentity)
			if err != nil {
				t.Fatal("PostgreSQL lost existing CA trust during renewal", err)
			}
			_ = pg.Close(wait)
			return next
		}
		if sleepContext(wait, 3*time.Second) != nil {
			break
		}
	}
	t.Fatal("MyDuck did not serve its renewed identity on both protocols")
	return previous
}

func TestManagedMyDuckColdRecoveryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYDUCK_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYDUCK_TEST=1 for named development cluster acceptance")
	}
	c, ctx := liveRecoveryClient(t, 25*time.Minute)
	source, password, observed := newMyDuckFixture(t, ctx, c)
	_, identity, err := c.myduckClientIdentity(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	client, err := c.myduckMySQLClient(ctx, source, observed.Members[0], password, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecContext(ctx, "CREATE TABLE acceptance (id INTEGER PRIMARY KEY, value VARCHAR(255))"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecContext(ctx, "INSERT INTO acceptance VALUES (1, 'recovered')"); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	jobID := fmt.Sprintf("%032x", time.Now().UnixNano())
	before := func() error { return ctx.Err() }
	archive := &databaseBoundedWriter{limit: 32 << 20}
	if err = c.WithMyDuckColdStorage(ctx, source, observed, jobID, false, before, nil, archive); err != nil {
		t.Fatal(err)
	}
	if err = c.ReconcileMyDuckColdStorage(ctx, source, jobID, true, before); err != nil {
		t.Fatal(err)
	}
	observed = waitMyDuckFixture(t, ctx, c, source)
	testMyDuckReads(t, ctx, c, source, observed, "recovered")
	target, _, targetHealth := newMyDuckFixture(t, ctx, c)
	restoreID := fmt.Sprintf("%032x", time.Now().UnixNano())
	target.Status = "restoring"
	target.Recovery = &database.Recovery{JobID: restoreID}
	if err = c.WithMyDuckColdStorage(ctx, target, targetHealth, restoreID, true, before, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal(err)
	}
	if err = c.ReconcileMyDuckColdStorage(ctx, target, restoreID, true, before); err != nil {
		t.Fatal(err)
	}
	targetHealth = waitMyDuckFixture(t, ctx, c, target)
	testMyDuckReads(t, ctx, c, target, targetHealth, "recovered")
	if source.ID == target.ID {
		t.Fatal("recovery did not use a separate resource")
	}
	t.Log("Cold backup resumed its source; separate target restored both protocols with its own credentials")
}

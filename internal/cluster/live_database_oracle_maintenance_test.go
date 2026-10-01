package cluster

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	goora "github.com/sijms/go-ora/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func waitOracleCapture(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation, done <-chan error) {
	t.Helper()
	step, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	for step.Err() == nil {
		select {
		case err := <-done:
			t.Fatal("Oracle capture ended before concurrent-operation acceptance", err)
		default:
		}
		value, err := c.oracleLocalQuery(step, d, o.Members[0], "ALTER SESSION SET CONTAINER=FREEPDB1;\nSELECT COUNT(*) FROM v$lock WHERE type='UL' AND id1=82635001 AND lmode=6 AND EXISTS (SELECT 1 FROM dba_datapump_jobs WHERE job_name LIKE 'HP_%' AND operation='EXPORT' AND state='EXECUTING')")
		if err == nil && value == "1" {
			return
		}
		if sleepContext(step, 200*time.Millisecond) != nil {
			break
		}
	}
	t.Fatal("Oracle capture did not start a server-side job with its DDL guard")
}

func finishOracleCapture(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(35 * time.Second):
		t.Error("Oracle capture did not finish before fixture cleanup")
	}
}

func testOracleCaptureGuards(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation, client *sql.DB) []byte {
	t.Helper()
	capture, stop := context.WithCancel(ctx)
	archive := &databaseBoundedWriter{limit: 16 << 20}
	done := make(chan error, 1)
	go func(result chan error) { defer close(result); result <- c.DumpDatabase(capture, d, o, archive) }(done)
	defer finishOracleCapture(t, stop, done)
	waitOracleCapture(t, ctx, c, d, o, done)
	// Drivers can expose only the outer ORA-00604/ORA-04088 trigger error.
	// Read the server's complete stack through an application-privileged bind.
	var ddlError string
	ddl := `DECLARE error_stack VARCHAR2(4000);
BEGIN
 BEGIN
  EXECUTE IMMEDIATE 'CREATE TABLE capture_ddl_fixture (id NUMBER)';
  error_stack := 'accepted';
 EXCEPTION WHEN OTHERS THEN error_stack := DBMS_UTILITY.FORMAT_ERROR_STACK;
 END;
 :result := error_stack;
END;`
	if _, err := client.ExecContext(ctx, ddl, goora.Out{Dest: &ddlError, Size: 4000}); err != nil || !strings.Contains(ddlError, "ORA-20010") {
		t.Fatal("Oracle did not explicitly reject concurrent schema changes with its retry error", err)
	}
	if _, err := client.ExecContext(ctx, "INSERT INTO recovery_fixture VALUES (8,NULL,'source-only')"); err != nil {
		t.Fatal("Oracle SCN capture blocked an ordinary source write", err)
	}
	if err := <-done; err != nil {
		t.Fatal("native Oracle capture failed", err)
	}
	result := append([]byte(nil), archive.Bytes()...)

	// Exercise cancellation after a server-side job starts, not merely before
	// sending the request. The context alone cannot cancel a Data Pump job.
	cancelled, cancel := context.WithCancel(ctx)
	done = make(chan error, 1)
	go func(result chan error) { defer close(result); result <- c.DumpDatabase(cancelled, d, o, io.Discard) }(done)
	defer finishOracleCapture(t, cancel, done)
	waitOracleCapture(t, ctx, c, d, o, done)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled Oracle capture reported success")
		}
	case <-time.After(35 * time.Second):
		t.Fatal("cancelled Oracle capture did not complete bounded cleanup")
	}
	value, err := c.oracleLocalQuery(ctx, d, o.Members[0], "ALTER SESSION SET CONTAINER=FREEPDB1;\nSELECT COUNT(*) FROM dba_datapump_jobs WHERE job_name LIKE 'HP_%'")
	if err != nil || value != "0" {
		t.Fatal("cancelled Oracle capture retained its Data Pump job", err)
	}
	if _, err := client.ExecContext(ctx, "CREATE TABLE capture_ddl_fixture (id NUMBER)"); err != nil {
		t.Fatal("capture cancellation retained its DDL guard", err)
	}
	if _, err := client.ExecContext(ctx, "DROP TABLE capture_ddl_fixture PURGE"); err != nil {
		t.Fatal(err)
	}
	t.Log("Oracle SCN capture rejected concurrent DDL, allowed DML and cleaned a cancelled server-side job")
	return result
}

func testOracleRenewal(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation) database.Observation {
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
		t.Fatal("Oracle fixture issuer is invalid")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal("Oracle fixture expiry injection failed")
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = c.kube.CoreV1().Secrets(root.Namespace).Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, 8*time.Minute)
	defer stop()
	var next database.Observation
	for wait.Err() == nil {
		next, err = c.ObserveDatabase(wait, d)
		if err == nil && next.Status == "ready" && next.TLS != nil && next.TLS.Verified && next.TLS.Fingerprint != observed.TLS.Fingerprint && next.TLS.CAFingerprint != trust.Fingerprint && len(next.Members) == 1 && next.Members[0].UID != observed.Members[0].UID {
			break
		}
		if sleepContext(wait, 3*time.Second) != nil {
			break
		}
	}
	if next.Status != "ready" || next.TLS == nil || !next.TLS.Verified || next.TLS.Fingerprint == observed.TLS.Fingerprint || next.TLS.CAFingerprint == trust.Fingerprint {
		t.Fatal("Oracle did not serve its renewed identity", err)
	}
	if len(next.Members) != 1 || next.Members[0].Restarts != 0 {
		t.Fatal("Oracle replacement required a container crash to become ready")
	}
	config, err := redisTLSConfig(trust, oracleHost(d))
	if err != nil {
		t.Fatal(err)
	}
	step, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := c.oracleStream(step, d, next.Members[0])
	if err != nil {
		t.Fatal(err)
	}
	secured := tls.Client(raw, config)
	err = secured.HandshakeContext(step)
	_ = secured.Close()
	if err != nil {
		t.Fatal("old application CA stopped trusting Oracle during renewal", err)
	}
	client := oracleFixtureConnection(t, ctx, c, d, next)
	var count int
	if err = client.QueryRowContext(ctx, "SELECT COUNT(*) FROM recovery_fixture").Scan(&count); err != nil || count != 2 {
		t.Fatal("Oracle row data did not survive restart and certificate renewal", err)
	}
	t.Log("Oracle loaded renewed server and issuer certificates, retained data through replacement and preserved old CA overlap")
	return next
}

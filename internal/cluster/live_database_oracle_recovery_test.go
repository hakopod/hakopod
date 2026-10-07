package cluster

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func oracleFixtureConnection(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) *sql.DB {
	t.Helper()
	connection, err := c.oracleApplicationConnection(ctx, d, o.Members[0], true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func TestManagedOracleRecoveryLive(t *testing.T) {
	c, ctx := liveOracleClient(t)
	source, health := newOracleFixture(t, ctx, c, os.Getenv("HAKOPOD_ORACLE_RECOVERY_SOURCE_ID"))
	client := oracleFixtureConnection(t, ctx, c, source, health)
	if _, err := client.ExecContext(ctx, "BEGIN EXECUTE IMMEDIATE 'DROP TABLE recovery_fixture PURGE'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -942 THEN RAISE; END IF; END;"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"BEGIN EXECUTE IMMEDIATE 'DROP SEQUENCE recovery_sequence'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -2289 THEN RAISE; END IF; END;",
		"CREATE TABLE recovery_fixture (id NUMBER PRIMARY KEY, value BLOB, text CLOB)",
		"CREATE SEQUENCE recovery_sequence START WITH 42",
		"CREATE OR REPLACE VIEW recovery_view AS SELECT id FROM recovery_fixture",
		"CREATE OR REPLACE FUNCTION recovery_marker RETURN NUMBER AS BEGIN RETURN 42; END;",
		"CREATE TRIGGER recovery_trigger BEFORE INSERT ON recovery_fixture FOR EACH ROW BEGIN IF :NEW.id=9 THEN :NEW.text := 'restored-trigger'; END IF; END;",
		"INSERT INTO recovery_fixture VALUES (7,HEXTORAW('0080FF0D0A'),TO_CLOB('Unicode: नमस्ते'))",
	} {
		if _, err := client.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	// Recovery copies the schema's objects and data, not the source's account
	// policy. A different source quota detects replaying that policy on target.
	if _, err := c.oracleLocalQuery(ctx, source, health.Members[0], "ALTER SESSION SET CONTAINER=FREEPDB1;\nALTER USER APP QUOTA 1G ON USERS"); err != nil {
		t.Fatal(err)
	}
	archive := testOracleCaptureGuards(t, ctx, c, source, health, client)
	health = testOracleRenewal(t, ctx, c, source, health)
	client = oracleFixtureConnection(t, ctx, c, source, health)
	testOracleBinding(t, ctx, c, source, health)
	target, targetHealth := newOracleFixture(t, ctx, c, os.Getenv("HAKOPOD_ORACLE_RECOVERY_TARGET_ID"))
	target.Status, target.Recovery = "restoring", &database.Recovery{JobID: source.ID}
	if err := c.databaseNetworkPolicy(ctx, target, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{archive[:len(archive)/2], append(append([]byte{}, archive...), 0)} {
		if err := c.RestoreOracleDatabase(ctx, target, targetHealth, bytes.NewReader(data)); err == nil {
			t.Fatal("invalid Oracle archive was accepted")
		}
		if err := c.DatabaseEmpty(ctx, target, targetHealth); err != nil {
			t.Fatal("invalid archive changed the target", err)
		}
	}
	if recoveryApplicationIngress(t, c, target) {
		t.Fatal("invalid recovery opened client ingress")
	}
	old := oracleFixtureConnection(t, ctx, c, target, targetHealth)
	session, err := old.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.RestoreOracleDatabase(ctx, target, targetHealth, bytes.NewReader(archive)); err != nil {
		t.Fatal("native Oracle restore failed", err)
	}
	if err = session.PingContext(ctx); err == nil {
		t.Fatal("recovery did not revoke the existing application session")
	}
	targetHealth, err = c.ObserveDatabase(ctx, target)
	if err != nil || targetHealth.Status != "ready" {
		t.Fatal("restored Oracle target is not healthy", err)
	}
	restored := oracleFixtureConnection(t, ctx, c, target, targetHealth)
	var value, text string
	if err = restored.QueryRowContext(ctx, "SELECT RAWTOHEX(DBMS_LOB.SUBSTR(value,5,1)),DBMS_LOB.SUBSTR(text,100,1) FROM recovery_fixture WHERE id=7").Scan(&value, &text); err != nil || value != "0080FF0D0A" || text != "Unicode: नमस्ते" {
		t.Fatal("Oracle recovery changed binary or Unicode data", err)
	}
	var quota int64
	if err = restored.QueryRowContext(ctx, "SELECT max_bytes FROM user_ts_quotas WHERE tablespace_name='USERS'").Scan(&quota); err != nil || quota != database.OracleFreeQuotaGiB(target.Spec.StorageGiB)<<30 {
		t.Fatal("Oracle recovery replaced the target's managed schema quota", err)
	}
	for _, query := range []string{"CREATE USER recovery_attacker IDENTIFIED BY password", "SELECT * FROM sys.user$", "ALTER SYSTEM SET sessions=500 SCOPE=SPFILE"} {
		if _, err = restored.ExecContext(ctx, query); err == nil {
			t.Fatal("Oracle recovery expanded application privileges")
		}
	}
	var sequence, marker, rows int
	if err = restored.QueryRowContext(ctx, "SELECT recovery_sequence.NEXTVAL,recovery_marker FROM dual").Scan(&sequence, &marker); err != nil || sequence != 42 || marker != 42 {
		t.Fatal("Oracle recovery did not preserve the sequence and stored function", err)
	}
	if err = restored.QueryRowContext(ctx, "SELECT COUNT(*) FROM recovery_view").Scan(&rows); err != nil || rows != 1 {
		t.Fatal("Oracle recovery did not preserve the view at its captured point", err)
	}
	if err = c.RestoreOracleDatabase(ctx, target, targetHealth, bytes.NewReader(archive)); err == nil {
		t.Fatal("nonempty Oracle target was accepted")
	}
	if _, err = restored.ExecContext(ctx, "INSERT INTO recovery_fixture VALUES (9,NULL,'target-only')"); err != nil {
		t.Fatal(err)
	}
	if err = restored.QueryRowContext(ctx, "SELECT DBMS_LOB.SUBSTR(text,100,1) FROM recovery_fixture WHERE id=9").Scan(&text); err != nil || text != "restored-trigger" {
		t.Fatal("Oracle recovery did not preserve the application trigger", err)
	}
	for connection, want := range map[*sql.DB]int{client: 78, restored: 79} {
		var got int
		if err = connection.QueryRowContext(ctx, "SELECT SUM(id * CASE WHEN id=7 THEN 10 ELSE 1 END) FROM recovery_fixture").Scan(&got); err != nil || got != want {
			t.Fatal("Oracle source and target did not retain independent writes", err)
		}
	}
	for _, d := range []database.Resource{source, target} {
		// Each observation verifies the controller, pod, credentials and TCPS.
		// Give each resource its own bound before checking Data Pump cleanup.
		check, stop := context.WithTimeout(ctx, 45*time.Second)
		o, e := c.ObserveDatabase(check, d)
		if e != nil {
			stop()
			t.Fatal("Oracle recovery cleanup health check failed", e)
		}
		got, e := c.oracleLocalQuery(check, d, o.Members[0], "ALTER SESSION SET CONTAINER=FREEPDB1;\nSELECT (SELECT COUNT(*) FROM dba_datapump_jobs WHERE job_name LIKE 'HP_%')+(SELECT COUNT(*) FROM dba_tab_privs WHERE grantee='APP' AND table_name='HAKOPOD_BACKUP') FROM dual")
		stop()
		if e != nil || got != "0" {
			t.Fatal("Oracle recovery retained jobs or directory grants", e)
		}
	}
	testRecoveryIngressGates(t, ctx, c, target)
	t.Log("Oracle native schema recovery preserved binary/Unicode data, views, sequences, functions and triggers; retained target quota and restricted permissions; rejected incomplete/nonempty targets; revoked sessions; cleaned jobs/grants; and retained independent writes")
}

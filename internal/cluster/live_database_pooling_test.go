package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestManagedPostgresPoolingLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_POOLING_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_POOLING_TEST=1 for named development acceptance")
	}
	for _, mode := range []string{"transaction", "session"} {
		t.Run(mode, func(t *testing.T) { testManagedPostgresPoolingLive(t, mode) })
	}
}

func testManagedPostgresPoolingLive(t *testing.T, mode string) {
	c, ctx := liveRecoveryClient(t)
	id, password := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: hex.EncodeToString(id), Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "pooling-development-fixture", Engine: "postgresql", Version: "17", Mode: "cluster", Shards: 1, Replicas: 1, CPU: "500m", Memory: "512Mi", StorageGiB: 1, Placement: database.Placement{Spread: "nodes"}, TLS: &database.TLSConfig{Mode: "required"}, Pooling: &database.Pooling{Mode: mode, Instances: 2, MaxClientConnections: 200, DefaultPoolSize: 10, ReadOnly: true}}}
	t.Log("Development pooling namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		_, _ = c.DeleteDatabase(cleanup, d, func() error { return nil })
	})
	passwordText := []byte(hex.EncodeToString(password))
	if err := c.ApplyDatabase(ctx, d, passwordText, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	health := waitManagedDatabase(t, ctx, c, d)
	if health.Pooling == nil || !health.Pooling.Ready || len(health.Pooling.Members) != 4 || len(health.Endpoints) != 4 {
		t.Fatal("pooling topology incomplete")
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	query := func(purpose, sql string) (string, error) {
		var endpoint database.Endpoint
		for _, e := range health.Endpoints {
			if e.Purpose == purpose {
				endpoint = e
			}
		}
		output := &databaseBoundedWriter{limit: 4096}
		input := bytes.Join([][]byte{passwordText, []byte(trust.CertificatePEM)}, []byte{'\n'})
		script := `set -eu; IFS= read -r PGPASSWORD; export PGPASSWORD PGCONNECT_TIMEOUT=3; umask 077; trust=$(mktemp); trap 'rm -f "$trust"' EXIT; cat > "$trust"; PGSSLMODE=verify-full PGSSLROOTCERT="$trust" psql -XAtw -h "$1" -p "$2" -U app -d app -v ON_ERROR_STOP=1 -c "$3"`
		err := c.DatabaseExec(ctx, d, health.Members[0], []string{"sh", "-c", script, "pooled-fixture", endpoint.Host, strconv.Itoa(endpoint.Port), sql}, bytes.NewReader(input), output)
		return strings.TrimSpace(output.String()), err
	}
	if _, err = query("pooled_read_write", "CREATE TABLE pooled_acceptance(value text); INSERT INTO pooled_acceptance VALUES ('retained')"); err != nil {
		t.Fatal(err)
	}
	if got, err := query("pooled_read_only", "SELECT pg_is_in_recovery()"); err != nil || got != "t" {
		t.Fatal("pooled read route did not reach a replica", err)
	}
	if _, err = query("pooled_read_only", "CREATE TABLE forbidden_pooled_write(id int)"); err == nil {
		t.Fatal("pooled replica accepted a write")
	}
	// Force a pooler replacement independently of the database primary.
	lost := health.Pooling.Members[0]
	if err = c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Delete(ctx, lost.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr(types.UID(lost.UID))}}); err != nil {
		t.Fatal(err)
	}
	if err = sleepContext(ctx, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	health = waitManagedDatabase(t, ctx, c, d)
	for _, m := range health.Pooling.Members {
		if m.UID == lost.UID {
			t.Fatal("deleted pooler remains in the ready topology")
		}
	}
	if got, err := query("pooled_read_write", "SELECT value FROM pooled_acceptance"); err != nil || got != "retained" {
		t.Fatal("pooler replacement did not preserve connectivity", err)
	}
	var primary database.Member
	for _, m := range health.Members {
		if m.Role == "primary" {
			primary = m
		}
	}
	if err = c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Delete(ctx, primary.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr(types.UID(primary.UID))}}); err != nil {
		t.Fatal(err)
	}
	if err = sleepContext(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	health = waitManagedDatabase(t, ctx, c, d)
	if got, err := query("pooled_read_write", "SELECT value FROM pooled_acceptance"); err != nil || got != "retained" {
		t.Fatal("pooled endpoint did not recover retained data", err)
	}
	if got, err := query("pooled_read_only", "SELECT pg_is_in_recovery()"); err != nil || got != "t" {
		t.Fatal("pooled replica route did not recover after promotion", err)
	}
	if _, err = query("pooled_read_only", "INSERT INTO pooled_acceptance VALUES ('forbidden')"); err == nil {
		t.Fatal("pooled replica route accepted a write after failover")
	}
	expireFixturePostgresCA(t, ctx, c, d)
	deadline := time.Now().Add(3 * time.Minute)
	renewed := false
	for time.Now().Before(deadline) {
		next, e := c.DatabaseTrust(ctx, d)
		if e == nil && next.Fingerprint != trust.Fingerprint && next.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
			trust = next
			renewed = true
			break
		}
		if sleepContext(ctx, 2*time.Second) != nil {
			break
		}
	}
	if !renewed {
		t.Fatal("pooler CA renewal was not observed")
	}
	health = waitManagedDatabase(t, ctx, c, d)
	for _, route := range []string{"pooled_read_write", "pooled_read_only"} {
		if got, err := query(route, "SELECT value FROM pooled_acceptance"); err != nil || got != "retained" {
			t.Fatal("renewed pooler TLS failed", route, err)
		}
	}
	if health.TLS == nil || !health.TLS.Verified || !health.TLS.PlaintextRejected {
		t.Fatal("pooled TLS was not verified")
	}
	t.Log("Verified separate TLS PgBouncer primary/replica routes, replica write rejection, four owned pooler instances, primary-loss recovery, pooler replacement and CA renewal")
}

func TestManagedPostgresPooledRecoveryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_POOLING_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_POOLING_TEST=1")
	}
	c, ctx := liveRecoveryClient(t)
	configure := func(s *database.Spec) {
		s.CPU = "500m"
		s.Memory = "512Mi"
		s.TLS = &database.TLSConfig{Mode: "required"}
		s.Pooling = &database.Pooling{Mode: "transaction", Instances: 2, MaxClientConnections: 200, DefaultPoolSize: 10}
	}
	source, sourceHealth := newRecoveryFixtureConfigured(t, ctx, c, "postgresql", "17", configure)
	query := func(d database.Resource, o database.Observation, sql string) string {
		t.Helper()
		credentials, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if err != nil {
			t.Fatal("fixture credentials unavailable")
		}
		trust, err := c.DatabaseTrust(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		host := ""
		for _, endpoint := range o.Endpoints {
			if endpoint.Purpose == "pooled_read_write" {
				host = endpoint.Host
			}
		}
		if host == "" {
			t.Fatal("pooled recovery endpoint missing")
		}
		input := bytes.Join([][]byte{credentials.Data["password"], []byte(trust.CertificatePEM)}, []byte{'\n'})
		out := &databaseBoundedWriter{limit: 4096}
		script := `set -eu; IFS= read -r PGPASSWORD; export PGPASSWORD PGCONNECT_TIMEOUT=3; umask 077; trust=$(mktemp); trap 'rm -f "$trust"' EXIT; cat > "$trust"; PGSSLMODE=verify-full PGSSLROOTCERT="$trust" psql -XAtw -h "$1" -U app -d app -v ON_ERROR_STOP=1 -c "$2"`
		if err = c.DatabaseExec(ctx, d, o.Members[0], []string{"sh", "-c", script, "pooled-recovery-fixture", host, sql}, bytes.NewReader(input), out); err != nil {
			t.Fatal("pooled recovery query failed", err)
		}
		return strings.TrimSpace(out.String())
	}
	query(source, sourceHealth, "CREATE TABLE pooled_recovery(id integer PRIMARY KEY, value text); INSERT INTO pooled_recovery VALUES (1,'captured')")
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, source, sourceHealth, archive); err != nil {
		t.Fatal(err)
	}
	query(source, sourceHealth, "INSERT INTO pooled_recovery VALUES (2,'after capture')")
	target, health := newRecoveryFixtureConfigured(t, ctx, c, "postgresql", "17", configure)
	if err := c.RestorePostgresDatabase(ctx, target, health, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got := query(target, health, "SELECT id || ':' || value FROM pooled_recovery ORDER BY id"); got != "1:captured" {
		t.Fatal("pooled recovery data differs")
	}
	if err := c.RestorePostgresDatabase(ctx, target, health, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("pooled recovery overwrote a nonempty target")
	}
	if got := query(source, sourceHealth, "SELECT count(*) FROM pooled_recovery"); got != "2" {
		t.Fatal("pooled recovery changed the source")
	}
	t.Log("Verified authenticated backup, separate empty target restore, retained source and TLS access through the target pooler")
}

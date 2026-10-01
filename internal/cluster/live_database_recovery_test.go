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
	"k8s.io/client-go/tools/clientcmd"
)

func liveRecoveryClient(t *testing.T, lifetime ...time.Duration) (*Client, context.Context) {
	t.Helper()
	if os.Getenv("HAKOPOD_DATABASE_RECOVERY_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_RECOVERY_TEST=1 for named development cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("database recovery acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	duration := 15 * time.Minute
	if len(lifetime) > 0 {
		duration = lifetime[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	t.Cleanup(cancel)
	return c, ctx
}

func TestManagedRedisRecoveryLive(t *testing.T) {
	c, ctx := liveRecoveryClient(t)
	source, sourceHealth := newRecoveryFixture(t, ctx, c, "redis", "8")
	connect := func(d database.Resource, o database.Observation) *database.RedisWire {
		t.Helper()
		member := o.Members[0]
		conn, closeForward, err := c.databaseRedisMemberConnection(ctx, d, member)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeForward)
		t.Cleanup(func() { _ = conn.Close() })
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Command([]byte("AUTH"), secret.Data["password"]); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	command := func(conn *database.RedisWire, args ...[]byte) any {
		t.Helper()
		v, err := conn.Command(args...)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	src := connect(source, sourceHealth)
	key := []byte{'b', 0, 255, '\r', '\n'}
	value := []byte{0, 128, 255, '\r', '\n'}
	expires := time.Now().Add(10 * time.Minute).UnixMilli()
	command(src, []byte("SET"), key, value, []byte("PXAT"), []byte(strconv.FormatInt(expires, 10)))
	command(src, []byte("HSET"), []byte("fixture-hash"), []byte("field"), value)
	command(src, []byte("RPUSH"), []byte("fixture-list"), []byte("one"), value)
	command(src, []byte("XADD"), []byte("fixture-stream"), []byte("1-0"), []byte("field"), value)
	command(src, []byte("SELECT"), []byte("2"))
	command(src, []byte("SET"), []byte("fixture-other-db"), value)
	command(src, []byte("SELECT"), []byte("0"))
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, source, sourceHealth, archive); err != nil {
		t.Fatal(err)
	}
	command(src, []byte("SET"), []byte("after-capture"), []byte("source-only"))
	target, health := newRecoveryFixture(t, ctx, c, "redis", "8")
	target.Status = "restoring"
	target.Recovery = &database.Recovery{JobID: source.ID}
	if err := c.RestoreRedisDatabase(ctx, target, health, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	dst := connect(target, health)
	if got := command(dst, []byte("GET"), key); !bytes.Equal(got.([]byte), value) {
		t.Fatal("binary value changed")
	}
	if got := command(dst, []byte("PEXPIRETIME"), key); got != expires {
		t.Fatal("absolute expiry changed")
	}
	if got := command(dst, []byte("HGET"), []byte("fixture-hash"), []byte("field")); !bytes.Equal(got.([]byte), value) {
		t.Fatal("hash value changed")
	}
	if got := command(dst, []byte("LINDEX"), []byte("fixture-list"), []byte("1")); !bytes.Equal(got.([]byte), value) {
		t.Fatal("list value changed")
	}
	if got := command(dst, []byte("DUMP"), []byte("fixture-stream")); !bytes.Equal(got.([]byte), command(src, []byte("DUMP"), []byte("fixture-stream")).([]byte)) {
		t.Fatal("stream changed")
	}
	if got := command(dst, []byte("EXISTS"), []byte("after-capture")); got != int64(0) {
		t.Fatal("copy exceeded recovery point")
	}
	command(dst, []byte("SELECT"), []byte("2"))
	if got := command(dst, []byte("GET"), []byte("fixture-other-db")); !bytes.Equal(got.([]byte), value) {
		t.Fatal("logical database was lost")
	}
	if got := command(src, []byte("GET"), []byte("after-capture")); string(got.([]byte)) != "source-only" {
		t.Fatal("source changed")
	}
	testRecoveryIngressGates(t, ctx, c, target)
}

// This checks the applied Kubernetes policy at each durable recovery gate.
// Engine data checks and client transport checks remain separate assertions.
func testRecoveryIngressGates(t *testing.T, ctx context.Context, c *Client, d database.Resource) {
	t.Helper()
	check := func(want bool) {
		t.Helper()
		policy, err := c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		open := false
		for _, rule := range policy.Spec.Ingress {
			for _, peer := range rule.From {
				open = open || peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["hakopod.io/database-access-"+d.ID] == "true"
			}
		}
		if open != want {
			t.Fatal("database application ingress disagrees with recovery completion and inspection")
		}
	}
	if d.Recovery == nil {
		t.Fatal("recovery acceptance is missing its durable job identity")
	}
	check(false)
	stamp := time.Now().UTC()
	d.Status, d.Recovery.RestoredAt = "ready", &stamp
	if err := c.databaseNetworkPolicy(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileDatabaseRecoveryAccess(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	check(false)
	d.Recovery.InspectedAt = &stamp
	if err := c.ReconcileDatabaseRecoveryAccess(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	check(true)
}

func newRecoveryFixture(t *testing.T, ctx context.Context, c *Client, engine, version string, clustered ...bool) (database.Resource, database.Observation) {
	return newRecoveryFixtureConfigured(t, ctx, c, engine, version, nil, clustered...)
}

func developmentRecoveryFixtureNodes(t *testing.T) []string {
	t.Helper()
	value := os.Getenv("HAKOPOD_DATABASE_FIXTURE_NODES")
	if value == "" {
		return nil
	}
	nodes := strings.Split(value, ",")
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if (node != "k3d-hakopod-dev-server-0" && node != "k3d-hakopod-database-worker-0") || seen[node] {
			t.Fatal("recovery fixtures require distinct dedicated database development nodes")
		}
		seen[node] = true
	}
	return nodes
}

func newRecoveryFixtureConfigured(t *testing.T, ctx context.Context, c *Client, engine, version string, configure func(*database.Spec), clustered ...bool) (database.Resource, database.Observation) {
	t.Helper()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: hex.EncodeToString(random[:16]), Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "database-recovery-development-fixture", Engine: engine, Version: version, Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}}
	if os.Getenv("HAKOPOD_DATABASE_TLS_TEST") == "1" {
		d.Spec.TLS = &database.TLSConfig{Mode: "required"}
	}
	if len(clustered) > 0 && clustered[0] {
		d.Spec.Mode = "cluster"
		d.Spec.Replicas = 1
		if engine == "redis" {
			d.Spec.Shards = 3
			d.Spec.Memory = "128Mi"
		}
	}
	if configure != nil {
		configure(&d.Spec)
	}
	if nodes := developmentRecoveryFixtureNodes(t); len(nodes) > 0 {
		d.Spec.Placement.NodeNames = nodes
	}
	t.Logf("development fixture namespace %s", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Minute)
		defer stop()
		for cleanup.Err() == nil {
			done, err := c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
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
		t.Error("recovery fixture deletion did not finish reclaiming its resources")
	})
	if err := c.ApplyDatabase(ctx, d, []byte(hex.EncodeToString(random)), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	return d, waitManagedDatabase(t, ctx, c, d)
}

func TestManagedPostgresRecoveryAndUpgradeLive(t *testing.T) {
	c, ctx := liveRecoveryClient(t)
	source, sourceHealth := newRecoveryFixture(t, ctx, c, "postgresql", "17")
	query := func(d database.Resource, o database.Observation, sql string) string {
		t.Helper()
		out := &databaseBoundedWriter{limit: 4096}
		for _, m := range o.Members {
			if m.Name == o.Primary {
				if err := c.DatabaseExec(ctx, d, m, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", sql}, nil, out); err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out.String())
			}
		}
		t.Fatal("fixture primary is unavailable")
		return ""
	}
	query(source, sourceHealth, "CREATE TABLE recovery_acceptance(id bigint PRIMARY KEY, value text); INSERT INTO recovery_acceptance VALUES (1, 'captured');")
	query(source, sourceHealth, `CREATE FUNCTION restore_owner_probe(text) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$ BEGIN IF session_user <> 'app' AND current_user = 'app' THEN RAISE EXCEPTION 'restore must authenticate as app'; END IF; RETURN $1; END $$; CREATE INDEX recovery_owner_probe ON recovery_acceptance(restore_owner_probe(value));`)
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, source, sourceHealth, archive); err != nil {
		t.Fatal(err)
	}
	query(source, sourceHealth, "INSERT INTO recovery_acceptance VALUES (2, 'after capture')")
	for _, version := range []string{"17", "18"} {
		t.Run("restore-17-to-"+version, func(t *testing.T) {
			target, health := newRecoveryFixture(t, ctx, c, "postgresql", version)
			target.Status, target.Recovery = "restoring", &database.Recovery{JobID: source.ID}
			if err := c.RestorePostgresDatabase(ctx, target, health, bytes.NewReader(archive.Bytes())); err != nil {
				t.Fatal(err)
			}
			if got := query(target, health, "SELECT id || ':' || value FROM recovery_acceptance ORDER BY id"); got != "1:captured" {
				t.Fatalf("unexpected recovered data: %q", got)
			}
			if got := query(target, health, "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE relname='recovery_acceptance'"); got != "app" {
				t.Fatalf("recovered table owner: %q", got)
			}
			if err := c.RestorePostgresDatabase(ctx, target, health, bytes.NewReader(archive.Bytes())); err == nil {
				t.Fatal("recovery reused a nonempty target")
			}
			if got := query(source, sourceHealth, "SELECT count(*) FROM recovery_acceptance"); got != "2" {
				t.Fatal("source changed during separate-target recovery")
			}
			testRecoveryIngressGates(t, ctx, c, target)
		})
	}
}

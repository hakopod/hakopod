package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestManagedDatabaseFifteenApplicationsSixReplicasLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_DENSE_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_DENSE_TEST=1 for the named development workload test")
	}
	// Seven members bootstrap sequentially on the resource-bounded development
	// VM. Reserve time for application deployment and primary recovery afterward.
	c, parent := liveRecoveryClient(t, 24*time.Minute)
	ctx, cancel := context.WithTimeout(parent, 24*time.Minute)
	defer cancel()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: hex.EncodeToString(id), Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "dense-topology-development-fixture", Engine: "postgresql", Version: "17", Mode: "cluster", Shards: 1, Replicas: 6, CPU: "500m", Memory: "512Mi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}}}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	t.Log("Development fixture namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		_, _ = c.DeleteDatabase(cleanup, d, func() error { return nil })
	})
	if err := c.ApplyDatabase(ctx, d, []byte(hex.EncodeToString(password)), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	health := waitManagedDatabase(t, ctx, c, d)
	primary := func(o database.Observation) database.Member {
		for _, m := range o.Members {
			if m.Name == o.Primary {
				return m
			}
		}
		return database.Member{}
	}
	query := func(o database.Observation, sql string) (string, error) {
		out := &databaseBoundedWriter{limit: 4096}
		err := c.DatabaseExec(ctx, d, primary(o), []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", sql}, nil, out)
		return strings.TrimSpace(out.String()), err
	}
	if _, err := query(health, "SET ROLE app; CREATE TABLE topology_acceptance(client text PRIMARY KEY,writes bigint NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "postgres", Host: health.Endpoints[0].Host + ":5432", Path: "/app", User: url.UserPassword("app", hex.EncodeToString(password))}
	values := url.Values{"sslmode": {"verify-full"}, "sslrootcert": {DatabaseTrustPath(d.ID)}}
	u.RawQuery = values.Encode()
	c.SetDatabaseBindingResolver(func(_ context.Context, project, environment string, a spec.Application) (map[string]map[string]DatabaseConnection, error) {
		if project != d.Project || environment != d.Environment {
			return nil, fmt.Errorf("fixture scope mismatch")
		}
		return map[string]map[string]DatabaseConnection{"writer": {"DATABASE_URL": {URL: u.String(), CA: trust.CertificatePEM, Port: 5432}}}, nil
	})
	targets := make([]Target, 15)
	for i := range targets {
		name := fmt.Sprintf("connected-app-%02d", i+1)
		sql := fmt.Sprintf("INSERT INTO topology_acceptance VALUES ('%s',1) ON CONFLICT(client) DO UPDATE SET writes=topology_acceptance.writes+1", name)
		// Data is artificial acceptance workload. Failures retry through the stable
		// write endpoint, exercising routing during a primary replacement.
		// Pass the fixed query as a positional argument to avoid shell quoting it.
		script := `while :; do PGCONNECT_TIMEOUT=3 psql "$DATABASE_URL" -XAt -v ON_ERROR_STOP=1 -c "$1" >/dev/null 2>&1 || true; sleep 2; done`
		a, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: name, Services: map[string]spec.Service{"writer": {Image: databaseImages["postgresql:17"], Command: []string{"sh", "-c", script, "acceptance-writer", sql}, ReadOnlyRootFilesystem: true, Resources: &spec.Resources{CPURequest: "20m", CPULimit: "100m", MemoryRequest: "32Mi", MemoryLimit: "96Mi"}, Bindings: map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "postgres", Endpoint: "read_write"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		targets[i] = Target{ApplicationID: fmt.Sprintf("dense-%s-%02d", d.ID[:12], i), Project: d.Project, Environment: d.Environment, Revision: 1, OperationID: "dense-fixture-create", Spec: a}
		target := targets[i]
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
			if e == nil && owned(ns, target) == nil {
				_ = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns))
			}
		})
	}
	group, deployCtx := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for _, target := range targets {
		group.Go(func() error { _, err := c.Deploy(deployCtx, target, nil); return err })
	}
	if err = group.Wait(); err != nil {
		t.Fatal(err)
	}
	// Each client must make progress after failover; a growing aggregate could
	// otherwise hide fourteen disconnected applications.
	waitWrites := func(previous map[string]int64) map[string]int64 {
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			value, e := query(health, "SELECT client||'|'||writes FROM topology_acceptance ORDER BY client")
			counts := map[string]int64{}
			if e == nil {
				for _, line := range strings.Split(value, "\n") {
					parts := strings.Split(line, "|")
					if len(parts) == 2 {
						n, err := strconv.ParseInt(parts[1], 10, 64)
						if err == nil {
							counts[parts[0]] = n
						}
					}
				}
			}
			complete := len(counts) == 15
			for _, target := range targets {
				if counts[target.Spec.Name] <= max(int64(1), previous[target.Spec.Name]) {
					complete = false
				}
			}
			if complete {
				return counts
			}
			if sleepContext(ctx, 2*time.Second) != nil {
				break
			}
		}
		t.Fatal("all 15 applications did not make progress")
		return nil
	}
	before := waitWrites(nil)
	lost := primary(health)
	if err = c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Delete(ctx, lost.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr(types.UID(lost.UID))}}); err != nil {
		t.Fatal(err)
	}
	if err = sleepContext(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	health = waitManagedDatabase(t, ctx, c, d)
	after := waitWrites(before)
	if len(health.Members) != 7 || health.TLS == nil || !health.TLS.Verified {
		t.Fatal("seven-member secure topology was not recovered")
	}
	if health.EngineMetrics == nil || !health.EngineMetrics.Available {
		t.Fatal("real primary activity statistics unavailable")
	}
	// Trust volumes must remain public and service-scoped in every application.
	for _, target := range targets {
		secret, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "writer-environment", metav1.GetOptions{})
		if err != nil || len(secret.Data) != 1 || !bytes.Equal(secret.Data["DATABASE_URL"], []byte(u.String())) {
			t.Fatal("application binding did not remain scoped")
		}
	}
	t.Logf("Verified all %d real TLS application writers resumed after primary replacement, with one primary plus six streaming replicas at 500m CPU/512Mi per member; two development nodes are not real availability zones", len(after))
}

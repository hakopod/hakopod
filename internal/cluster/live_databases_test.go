package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestManagedDatabasesLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_TEST=1 for named development cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("managed database acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"standalone", "cluster"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
				defer cancel()
				id := make([]byte, 16)
				if _, err := rand.Read(id); err != nil {
					t.Fatal(err)
				}
				d := database.Resource{ID: hex.EncodeToString(id), Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "managed-database-development-fixture", Engine: engine, Mode: mode, Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}}
				d.Spec.Version = "17"
				if os.Getenv("HAKOPOD_DATABASE_TLS_TEST") == "1" {
					d.Spec.TLS = &database.TLSConfig{Mode: "required"}
				}
				if engine == "redis" {
					d.Spec.Version = "8"
					d.Spec.Memory = "128Mi"
				}
				if mode == "cluster" {
					d.Spec.Replicas = 1
					if engine == "redis" {
						d.Spec.Shards = 3
					}
				}
				if engine == "postgresql" && mode == "cluster" && os.Getenv("HAKOPOD_DATABASE_PLACEMENT_TEST") == "1" {
					d.Spec.Placement = database.Placement{Spread: "zones", NodeNames: []string{"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0"}}
					if err := c.ValidateDatabasePlacement(ctx, d); err != nil {
						t.Fatal(err)
					}
				}
				password := make([]byte, 32)
				if _, err := rand.Read(password); err != nil {
					t.Fatal(err)
				}
				t.Logf("development fixture namespace %s", DatabaseNamespace(d.ID))
				t.Cleanup(func() {
					if os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" && t.Failed() {
						return
					}
					cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
					defer stop()
					_, _ = c.DeleteDatabase(cleanup, d, func() error { return nil })
				})
				if err = c.ApplyDatabase(ctx, d, []byte(hex.EncodeToString(password)), func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				observed := waitManagedDatabase(t, ctx, c, d)
				if os.Getenv("HAKOPOD_DATABASE_ENGINE_METRICS_TEST") == "1" && (observed.EngineMetrics == nil || !observed.EngineMetrics.Available || observed.EngineMetrics.DataBytes == nil) {
					t.Fatal("database engine metrics are not observed")
				}
				if d.Spec.TLSRequired() && (observed.TLS == nil || !observed.TLS.Verified || !observed.TLS.PlaintextRejected) {
					t.Fatal("database TLS enforcement was not verified")
				}
				if d.Spec.Placement.Spread != "" && (observed.Placement == nil || !observed.Placement.Verified || observed.Placement.Nodes != 2 || observed.Placement.Zones != 2) {
					t.Fatal("requested development node/zone separation was not observed")
				}
				if os.Getenv("HAKOPOD_DATABASE_METRICS_TEST") == "1" {
					deadline := time.Now().Add(90 * time.Second)
					for (observed.Metrics == nil || !observed.Metrics.Available) && time.Now().Before(deadline) {
						if sleepContext(ctx, 5*time.Second) != nil {
							break
						}
						step, stop := context.WithTimeout(ctx, 10*time.Second)
						observed, err = c.ObserveDatabase(step, d)
						stop()
						if err != nil {
							t.Fatal(err)
						}
					}
					if observed.Metrics == nil || !observed.Metrics.Available || observed.Metrics.PodsSampled != d.Spec.Members() || observed.Metrics.Memory == nil || *observed.Metrics.Memory <= 0 {
						t.Fatalf("complete real member samples unavailable: %+v", observed.Metrics)
					}
					for _, member := range observed.Members {
						if member.Metrics == nil || !member.Metrics.Available || member.Image == "" || member.CreatedAt == nil {
							t.Fatalf("member telemetry incomplete: %s", member.Name)
						}
					}
					t.Logf("verified real resource samples: %d members, %.2f mCPU, %d bytes memory", observed.Metrics.PodsSampled, *observed.Metrics.CPU, *observed.Metrics.Memory)
				}
				if engine == "postgresql" && mode == "cluster" {
					var primary database.Member
					for _, m := range observed.Members {
						if m.Name == observed.Primary {
							primary = m
						}
					}
					out := &databaseBoundedWriter{limit: 4096}
					if err = c.DatabaseExec(ctx, d, primary, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", "CREATE TABLE acceptance(value text); INSERT INTO acceptance VALUES ('replicated');"}, nil, out); err != nil {
						t.Fatal(err)
					}
					// A fixture-only primary loss must be recovered by CNPG without changing
					// application replica counts or letting Hakopod select a new primary.
					if err = c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Delete(ctx, primary.Name, metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
					// Provisioning and recovery have separate bounded deadlines. A slow
					// sandbox bootstrap must not consume the primary recovery window.
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 8*time.Minute)
					defer cancel()
					if err = sleepContext(ctx, 5*time.Second); err != nil {
						t.Fatal(err)
					}
					observed = waitManagedDatabase(t, ctx, c, d)
					for _, m := range observed.Members {
						if m.Name == observed.Primary {
							primary = m
						}
					}
					out = &databaseBoundedWriter{limit: 4096}
					if err = c.DatabaseExec(ctx, d, primary, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", "SELECT value FROM acceptance"}, nil, out); err != nil {
						t.Fatal(err)
					}
					if strings.TrimSpace(out.String()) != "replicated" {
						t.Fatal("recovered primary did not retain the fixture row")
					}
				}
			})
		}
	}
}
func waitManagedDatabase(t *testing.T, ctx context.Context, c *Client, d database.Resource) database.Observation {
	t.Helper()
	last := ""
	for ctx.Err() == nil {
		step, stop := context.WithTimeout(ctx, 25*time.Second)
		observed, err := c.ObserveDatabase(step, d)
		stop()
		if err == nil && observed.Status == "ready" {
			t.Logf("verified %s %s with %d members", d.Spec.Engine, d.Spec.Mode, len(observed.Members))
			return observed
		}
		message := observed.Message
		if err != nil {
			message = err.Error()
		}
		if message != last {
			t.Log(message)
			last = message
		}
		if sleepContext(ctx, 3*time.Second) != nil {
			break
		}
	}
	t.Fatalf("database did not become ready: %s", last)
	return database.Observation{}
}

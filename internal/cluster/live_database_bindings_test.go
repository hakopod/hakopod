package cluster

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedDatabaseApplicationBindingLive(t *testing.T) {
	testManagedDatabaseApplicationBindingLive(t, false)
}

func TestManagedPostgresPooledBindingLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_POOLING_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_POOLING_TEST=1")
	}
	testManagedDatabaseApplicationBindingLive(t, true)
}

func testManagedDatabaseApplicationBindingLive(t *testing.T, pooled bool) {
	c, ctx := liveRecoveryClient(t)
	d, observed := newRecoveryFixtureConfigured(t, ctx, c, "postgresql", "17", func(s *database.Spec) {
		if pooled {
			s.TLS = &database.TLSConfig{Mode: "required"}
			s.CPU = "500m"
			s.Memory = "512Mi"
			s.Pooling = &database.Pooling{Mode: "session", Instances: 2, MaxClientConnections: 200, DefaultPoolSize: 10}
		}
	})
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := observed.Endpoints[0]
	if pooled {
		for _, e := range observed.Endpoints {
			if e.Purpose == "pooled_read_write" {
				endpoint = e
			}
		}
		if endpoint.Purpose != "pooled_read_write" {
			t.Fatal("pooled endpoint missing")
		}
	}
	connection := url.URL{Scheme: "postgres", Host: fmt.Sprintf("%s:%d", endpoint.Host, endpoint.Port), Path: "/app", User: url.UserPassword("app", string(secret.Data["password"]))}
	ca := ""
	if d.Spec.TLSRequired() {
		trust, err := c.DatabaseTrust(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		ca = trust.CertificatePEM
		q := connection.Query()
		q.Set("sslmode", "verify-full")
		q.Set("sslrootcert", DatabaseTrustPath(d.ID))
		connection.RawQuery = q.Encode()
	}
	c.SetDatabaseBindingResolver(func(_ context.Context, project, env string, a spec.Application) (map[string]map[string]DatabaseConnection, error) {
		if project != d.Project || env != d.Environment {
			return nil, fmt.Errorf("wrong scope")
		}
		return map[string]map[string]DatabaseConnection{"allowed": {"DATABASE_URL": {URL: connection.String(), Port: 5432, CA: ca}}}, nil
	})
	service := spec.Service{Image: databaseImages["postgresql:17"], Command: []string{"sleep", "900"}, ReadOnlyRootFilesystem: true, Resources: &spec.Resources{CPURequest: "20m", CPULimit: "200m", MemoryRequest: "32Mi", MemoryLimit: "96Mi"}}
	allowed := service
	allowed.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "postgres", Endpoint: endpoint.Purpose}}
	app, err := spec.Normalize(spec.Application{Name: "database-binding-development-fixture", Services: map[string]spec.Service{"allowed": allowed, "unbound": service}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "database-binding-fixture-" + d.ID, Project: d.Project, Environment: d.Environment, Spec: app, Revision: 1, OperationID: "binding-fixture-create"}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			_ = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns))
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe := func(name, script string, want string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "deployment/"+name, "--", "sh", "-c", script).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("%s probe expected %s; command failed=%v", name, want, err != nil)
		}
	}
	probe("allowed", `PGCONNECT_TIMEOUT=3 psql "$DATABASE_URL" -Atc 'SELECT current_user'`, "app")
	if d.Spec.TLSRequired() {
		oldCA := ca
		expireFixturePostgresCA(t, ctx, c, d)
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			trust, e := c.DatabaseTrust(ctx, d)
			if e == nil && trust.CertificatePEM != oldCA && trust.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
				ca = trust.CertificatePEM
				break
			}
			if sleepContext(ctx, 2*time.Second) != nil {
				break
			}
		}
		if ca == oldCA {
			t.Fatal("renewed public CA was not available for the application")
		}
		if err = c.RenewDatabaseTrust(ctx, target, nil, "allowed"); err != nil {
			t.Fatal(err)
		}
		if err = exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "rollout", "status", "deployment/allowed", "--timeout=120s").Run(); err != nil {
			t.Fatal("application did not roll out renewed database trust")
		}
		probe("allowed", "cat "+DatabaseTrustPath(d.ID), strings.TrimSpace(ca))
		probe("allowed", `PGCONNECT_TIMEOUT=3 psql "$DATABASE_URL" -Atc 'SELECT current_user'`, "app")
		t.Log("Bound application received renewed public trust, rolled out and connected with hostname verification")
	}
	probe("unbound", fmt.Sprintf(`if PGCONNECT_TIMEOUT=3 pg_isready -h %s -p 5432 >/dev/null 2>&1; then echo allowed; else echo blocked; fi`, endpoint.Host), "blocked")
	stored, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "allowed-environment", metav1.GetOptions{})
	if err != nil || string(stored.Data["DATABASE_URL"]) != connection.String() {
		t.Fatal("application database credential did not resolve")
	}
	if len(stored.Data) != 1 {
		t.Fatal("unexpected credentials in application secret")
	}
	// Removing the binding also removes the namespace grant and service egress.
	target.Previous = &app
	target.Spec.Services["allowed"] = service
	target.Revision = 2
	target.OperationID = "binding-fixture-remove"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/database-access-"+d.ID] != "" {
		t.Fatal("removed database grant remained")
	}
	probe("allowed", fmt.Sprintf(`if PGCONNECT_TIMEOUT=3 pg_isready -h %s -p 5432 >/dev/null 2>&1; then echo allowed; else echo blocked; fi`, endpoint.Host), "blocked")
	t.Log("Private authenticated PostgreSQL connection, per-service isolation, scoped secret and connection removal verified")
}

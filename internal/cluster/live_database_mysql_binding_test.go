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

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedMySQLBindingLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYSQL_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MYSQL_TEST=1")
	}
	c, ctx := liveRecoveryClient(t, 15*time.Minute)
	d, password := newMySQLFixture(t, ctx, c, "standalone")
	health := waitMySQLFixture(t, ctx, c, d, password)
	testMySQLCertificateRefusal(t, ctx, c, d, health, password)
	endpoint := health.Endpoints[0]
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	ca := trust.CertificatePEM
	connection := url.URL{Scheme: "mysql", Host: fmt.Sprintf("%s:%d", endpoint.Host, endpoint.Port), Path: "/app", User: url.UserPassword("app", string(password))}
	c.SetDatabaseBindingResolver(func(_ context.Context, project, environment string, _ spec.Application) (map[string]map[string]DatabaseConnection, error) {
		if project != d.Project || environment != d.Environment {
			return nil, fmt.Errorf("wrong binding scope")
		}
		return map[string]map[string]DatabaseConnection{"allowed": {"DATABASE_URL": {URL: connection.String(), Port: int32(endpoint.Port), CA: ca}}}, nil
	})
	service := spec.Service{Image: databaseImages["mysql:8.4"], Command: []string{"sleep", "900"}, ReadOnlyRootFilesystem: true, Resources: &spec.Resources{CPURequest: "20m", CPULimit: "200m", MemoryRequest: "32Mi", MemoryLimit: "96Mi"}}
	allowed := service
	allowed.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "mysql", Endpoint: "read_write"}}
	app, err := spec.Normalize(spec.Application{Name: "mysql-binding-development-fixture", Services: map[string]spec.Service{"allowed": allowed, "unbound": service}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "mysql-binding-fixture-" + d.ID, Project: d.Project, Environment: d.Environment, Spec: app, Revision: 1, OperationID: "mysql-binding-create"}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		ns, err := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if err == nil && owned(ns, target) == nil {
			_ = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns))
		}
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe := func(service, command, want string) {
		t.Helper()
		step, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(step, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "deployment/"+service, "--", "sh", "-c", command).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("MySQL %s binding probe failed; expected %s", service, want)
		}
	}
	// Fixture passwords are hexadecimal. Keep them in the environment, never
	// in a process argument or an assertion failure.
	query := fmt.Sprintf(`MYSQL_PWD=${DATABASE_URL#mysql://app:}; MYSQL_PWD=${MYSQL_PWD%%%%@*}; export MYSQL_PWD; mysql --no-defaults --protocol=TCP --host=%s --port=%d --user=app --database=app --connect-timeout=3 --ssl-mode=VERIFY_IDENTITY --ssl-ca=%s --batch --skip-column-names --execute='SELECT CURRENT_USER()'`, endpoint.Host, endpoint.Port, DatabaseTrustPath(d.ID))
	probe("allowed", query, "app@%")
	blocked := fmt.Sprintf(`if timeout 4 bash -c 'exec 3<>/dev/tcp/%s/%d' >/dev/null 2>&1; then echo allowed; else echo blocked; fi`, endpoint.Host, endpoint.Port)
	probe("allowed", strings.Replace(blocked, "echo allowed", "echo reachable", 1), "reachable")
	probe("unbound", blocked, "blocked")
	secret, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "allowed-environment", metav1.GetOptions{})
	if err != nil || len(secret.Data) != 1 || string(secret.Data["DATABASE_URL"]) != connection.String() {
		t.Fatal("MySQL binding secret scope changed")
	}
	if _, err = mysqlFixtureQuery(ctx, c, d, health, password, "read_write", "CREATE TABLE acceptance(id INT PRIMARY KEY,value VARBINARY(16)); INSERT INTO acceptance VALUES(1,0x000AFF80)"); err != nil {
		t.Fatal(err)
	}
	testMySQLRenewal(t, ctx, c, d, password, health)
	trust, err = c.DatabaseTrust(ctx, d)
	if err != nil || trust.CertificatePEM == ca {
		t.Fatal("MySQL binding trust did not renew")
	}
	ca = trust.CertificatePEM
	if err = c.RenewDatabaseTrust(ctx, target, nil, "allowed"); err != nil {
		t.Fatal(err)
	}
	if err = exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "rollout", "status", "deployment/allowed", "--timeout=120s").Run(); err != nil {
		t.Fatal("MySQL application trust rollout failed")
	}
	probe("allowed", query, "app@%")
	target.Previous = &app
	next := app
	next.Services = map[string]spec.Service{"allowed": service, "unbound": service}
	target.Spec = next
	target.Revision, target.OperationID = 2, "mysql-binding-remove"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe("allowed", blocked, "blocked")
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/database-access-"+d.ID] != "" {
		t.Fatal("MySQL binding revocation retained its network grant")
	}
	testMySQLCredentialLogs(t, ctx, c, d)
	t.Log("MySQL binding verified native TLS, public trust renewal, scoped credentials, service isolation and network revocation")
}

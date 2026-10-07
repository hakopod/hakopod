package cluster

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testOracleBinding(t *testing.T, ctx context.Context, c *Client, d database.Resource, health database.Observation) {
	t.Helper()
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := health.Endpoints[0]
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	connection := url.URL{Scheme: "oracle", Host: net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), Path: "/FREEPDB1", User: url.UserPassword("APP", string(secret.Data["password"])), RawQuery: "SSL=enable&SSL+VERIFY=true&FAST+LOGIN=false"}
	c.SetDatabaseBindingResolver(func(_ context.Context, project, environment string, _ spec.Application) (map[string]map[string]DatabaseConnection, error) {
		if project != d.Project || environment != d.Environment {
			return nil, fmt.Errorf("wrong Oracle binding scope")
		}
		return map[string]map[string]DatabaseConnection{"allowed": {"DATABASE_URL": {URL: connection.String(), Port: 2484, CA: trust.CertificatePEM}}}, nil
	})
	service := spec.Service{Image: database.OracleFreeImage, NodeName: "k3d-hakopod-dev-server-0", Command: []string{"sleep", "1800"}, ReadOnlyRootFilesystem: true, TemporaryMounts: []spec.TemporaryMount{{MountPath: "/tmp", SizeMiB: 32, Memory: true}}, Resources: &spec.Resources{CPURequest: "20m", CPULimit: "300m", MemoryRequest: "64Mi", MemoryLimit: "512Mi"}}
	allowed := service
	allowed.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "oracle", Endpoint: "read_write"}}
	app, err := spec.Normalize(spec.Application{Name: "oracle-binding-development-fixture", Services: map[string]spec.Service{"allowed": allowed, "unbound": service}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "oracle-binding-fixture-" + d.ID, Project: d.Project, Environment: d.Environment, Spec: app, Revision: 1, OperationID: "oracle-binding-create"}
	var namespaceUID types.UID
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ns, err := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if err != nil || owned(ns, target) != nil || (namespaceUID != "" && ns.UID != namespaceUID) {
			t.Error("Oracle binding fixture namespace ownership changed; cleanup refused")
			return
		}
		namespaceUID = ns.UID
		if err = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &namespaceUID}}); err != nil && !apierrors.IsNotFound(err) {
			t.Error("delete Oracle binding fixture namespace", err)
			return
		}
		for cleanup.Err() == nil {
			current, e := c.kube.CoreV1().Namespaces().Get(cleanup, ns.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return
			}
			if e != nil {
				t.Error("wait for Oracle binding fixture namespace deletion", e)
				return
			}
			if current.UID != namespaceUID || owned(current, target) != nil {
				t.Error("Oracle binding fixture namespace was replaced or its ownership changed")
				return
			}
			time.Sleep(time.Second)
		}
		t.Error("Oracle binding fixture namespace deletion timed out")
	})
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
	if err != nil || namespace.UID == "" || owned(namespace, target) != nil {
		t.Fatal("Oracle binding fixture namespace identity is unavailable", err)
	}
	namespaceUID = namespace.UID
	kubectlCache := t.TempDir()
	probe := func(service, script, want string) {
		t.Helper()
		step, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		out, err := exec.CommandContext(step, "kubectl", "--cache-dir", kubectlCache, "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "deployment/"+service, "--", "bash", "-c", script).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("Oracle %s binding probe failed (%v)", service, err)
		}
	}
	// The application wallet contains only its scoped public CA. SQL*Plus gets
	// the generated password through stdin, never arguments or emitted output.
	native := `set -eu
umask 077
uri=${DATABASE_URL#oracle://APP:}; password=${uri%%@*}
[[ "$password" =~ ^[a-f0-9]{64}$ ]]
address=${uri#*@}; host=${address%%:*}
export TNS_ADMIN=$(mktemp -d)
trap 'rm -rf "$TNS_ADMIN"' EXIT
mkdir "$TNS_ADMIN/wallet"
printf '%s\n%s\n' "$password" "$password" | orapki wallet create -wallet "$TNS_ADMIN/wallet" -auto_login >/dev/null 2>&1
printf '%s\n' "$password" | orapki wallet add -wallet "$TNS_ADMIN/wallet" -trusted_cert -cert ` + DatabaseTrustPath(d.ID) + ` >/dev/null 2>&1
cat > "$TNS_ADMIN/sqlnet.ora" <<EOF
WALLET_LOCATION=(SOURCE=(METHOD=FILE)(METHOD_DATA=(DIRECTORY=$TNS_ADMIN/wallet)))
SSL_SERVER_DN_MATCH=YES
SSL_CLIENT_AUTHENTICATION=FALSE
SSL_VERSION=1.2
SQLNET.OUTBOUND_CONNECT_TIMEOUT=5
SQLNET.RECV_TIMEOUT=10
EOF
{ printf 'whenever sqlerror exit failure\nwhenever oserror exit failure\nset echo off verify off feedback off heading off pagesize 0\n';
  printf 'connect APP/"%s"@"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=%s)(PORT=2484))(CONNECT_DATA=(SERVICE_NAME=FREEPDB1)))"\n' "$password" "$host";
  printf "SELECT SYS_CONTEXT('USERENV','SESSION_USER') FROM dual;\nexit\n";
} | sqlplus -s /nolog 2>/dev/null`
	probe("allowed", native, "APP")
	blocked := fmt.Sprintf(`if timeout 4 bash -c 'exec 3<>/dev/tcp/%s/2484' >/dev/null 2>&1; then echo reachable; else echo blocked; fi`, endpoint.Host)
	probe("unbound", blocked, "blocked")
	stored, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "allowed-environment", metav1.GetOptions{})
	if err != nil || len(stored.Data) != 1 || string(stored.Data["DATABASE_URL"]) != connection.String() {
		t.Fatal("Oracle binding received unexpected credentials")
	}
	target.Previous = &app
	next := app
	next.Services = map[string]spec.Service{"allowed": service, "unbound": service}
	target.Spec, target.Revision, target.OperationID = next, 2, "oracle-binding-remove"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe("allowed", blocked, "blocked")
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/database-access-"+d.ID] != "" {
		t.Fatal("Oracle binding revocation retained its network grant")
	}
	t.Log("Oracle bound application authenticated with a public-CA-only wallet; unbound services and revoked bindings were isolated")
}

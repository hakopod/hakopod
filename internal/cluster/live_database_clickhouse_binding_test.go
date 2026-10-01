package cluster

import (
	"bytes"
	"context"
	"encoding/xml"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedClickHouseBindingLive(t *testing.T) {
	c, ctx := liveClickHouseClient(t)
	d, health := newClickHouseFixture(t, ctx, c, "cluster", 1, os.Getenv("HAKOPOD_CLICKHOUSE_BINDING_ID"))
	if health.EngineMetrics == nil || !health.EngineMetrics.Available {
		t.Fatal("ClickHouse native monitoring was unavailable")
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := health.Endpoints[0]
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	ca := trust.CertificatePEM
	connection := url.URL{Scheme: "clickhouse", Host: net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), Path: "/app", User: url.UserPassword("app", string(secret.Data["password"])), RawQuery: "secure=true&skip_verify=false"}
	c.SetDatabaseBindingResolver(func(_ context.Context, project, environment string, _ spec.Application) (map[string]map[string]DatabaseConnection, error) {
		if project != d.Project || environment != d.Environment {
			return nil, fmt.Errorf("wrong ClickHouse binding scope")
		}
		return map[string]map[string]DatabaseConnection{"allowed": {"DATABASE_URL": {URL: connection.String(), Port: 9440, CA: ca}}}, nil
	})
	service := spec.Service{Image: clickhouseServerImage, Command: []string{"sleep", "1200"}, ReadOnlyRootFilesystem: true, TemporaryMounts: []spec.TemporaryMount{{MountPath: "/tmp", SizeMiB: 4, Memory: true}}, Resources: &spec.Resources{CPURequest: "20m", CPULimit: "300m", MemoryRequest: "32Mi", MemoryLimit: "256Mi"}}
	allowed := service
	allowed.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: d.ID, Protocol: "clickhouse", Endpoint: "cluster", ClusterAware: true}}
	app, err := spec.Normalize(spec.Application{Name: "clickhouse-binding-development-fixture", Services: map[string]spec.Service{"allowed": allowed, "unbound": service}})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: "clickhouse-binding-fixture-" + d.ID, Project: d.Project, Environment: d.Environment, Spec: app, Revision: 1, OperationID: "clickhouse-binding-create"}
	t.Log("Development ClickHouse application namespace", Namespace(target.ApplicationID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
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
	probe := func(service, script, want string) {
		t.Helper()
		step, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		out, err := exec.CommandContext(step, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "deployment/"+service, "--", "bash", "-c", script).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("ClickHouse %s binding probe failed (%v)", service, err)
		}
	}
	native := func(query string) string {
		// Only generated hex credentials enter the temporary client config. The
		// password and connection URI never become process arguments or log text.
		return `set -eu
umask 077
uri=${DATABASE_URL#clickhouse://app:}
password=${uri%%@*}
[[ "$password" =~ ^[a-f0-9]{64}$ ]]
address=${uri#*@}; host=${address%%:*}
config=$(mktemp)
trap 'rm -f "$config"' EXIT
cat > "$config" <<EOF
<config><host>$host</host><port>9440</port><user>app</user><password>$password</password><secure>true</secure><connect_timeout>5</connect_timeout><receive_timeout>10</receive_timeout><send_logs_level>none</send_logs_level><openSSL><client><caConfig>` + DatabaseTrustPath(d.ID) + `</caConfig><verificationMode>strict</verificationMode><extendedVerification>true</extendedVerification><loadDefaultCAFile>false</loadDefaultCAFile><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL></config>
EOF
clickhouse-client --config-file="$config" --query '` + query + `' 2>/dev/null`
	}
	probe("allowed", native("SELECT currentUser()"), "app")
	probe("allowed", native("DROP TABLE IF EXISTS app.binding_fixture SYNC")+" >/dev/null", "")
	probe("allowed", native("CREATE TABLE app.binding_fixture (id UInt64) ORDER BY id")+" >/dev/null", "")
	probe("allowed", native("INSERT INTO app.binding_fixture VALUES (47)")+" >/dev/null", "")
	waitClickHouseData(t, ctx, c, d, health, "SELECT id FROM app.binding_fixture FORMAT TSV", "47")
	blocked := fmt.Sprintf(`if timeout 4 bash -c 'exec 3<>/dev/tcp/%s/9440' >/dev/null 2>&1; then echo reachable; else echo blocked; fi`, endpoint.Host)
	probe("unbound", blocked, "blocked")
	stored, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "allowed-environment", metav1.GetOptions{})
	if err != nil || len(stored.Data) != 1 || string(stored.Data["DATABASE_URL"]) != connection.String() {
		t.Fatal("ClickHouse binding received unexpected credentials")
	}
	health = testClickHouseRenewal(t, ctx, c, d, health)
	trust, err = c.DatabaseTrust(ctx, d)
	if err != nil || trust.CertificatePEM == ca {
		t.Fatal("ClickHouse binding trust did not renew")
	}
	ca = trust.CertificatePEM
	if err = c.RenewDatabaseTrust(ctx, target, nil, "allowed"); err != nil {
		t.Fatal(err)
	}
	if err = exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("HAKOPOD_TEST_KUBECONFIG"), "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "rollout", "status", "deployment/allowed", "--timeout=120s").Run(); err != nil {
		t.Fatal("ClickHouse application trust rollout failed")
	}
	probe("allowed", native("SELECT id FROM app.binding_fixture FORMAT TSV"), "47")
	target.Previous = &app
	next := app
	next.Services = map[string]spec.Service{"allowed": service, "unbound": service}
	target.Spec = next
	target.Revision = 2
	target.OperationID = "clickhouse-binding-remove"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	probe("allowed", blocked, "blocked")
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(target.ApplicationID), metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/database-access-"+d.ID] != "" {
		t.Fatal("ClickHouse binding revocation retained its network grant")
	}
	testClickHouseCredentialLogs(t, ctx, c, d)
	t.Log("Native application credentials, verified TLS, replica data, CA rollout, unbound-service isolation and binding revocation passed")
}

func testClickHouseCredentialLogs(t *testing.T, ctx context.Context, c *Client, d database.Resource) {
	t.Helper()
	ns := DatabaseNamespace(d.ID)
	secrets, err := c.kube.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{Limit: 20})
	if err != nil || secrets.Continue != "" {
		t.Fatal("ClickHouse credential audit inventory unavailable")
	}
	var private [][]byte
	for _, secret := range secrets.Items {
		for key, value := range secret.Data {
			if len(value) < 16 {
				continue
			}
			if key == "password" || key == "interserver" || strings.HasSuffix(key, ".key") || strings.HasSuffix(key, "-hash") {
				private = append(private, value)
			}
			if strings.HasSuffix(key, ".xml") {
				var config struct {
					Password string `xml:"password"`
				}
				if xml.Unmarshal(value, &config) != nil || len(config.Password) < 32 {
					t.Fatal("ClickHouse audit credential XML invalid")
				}
				private = append(private, []byte(config.Password))
			}
		}
	}
	if len(private) < 10 {
		t.Fatal("ClickHouse audit did not load its expected private values")
	}
	for namespace, selector := range map[string]string{ns: "", "clickhouse-operator": "app=clickhouse-operator"} {
		pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 55})
		if err != nil || pods.Continue != "" || len(pods.Items) == 0 {
			t.Fatal("ClickHouse log pod inventory unavailable")
		}
		for _, pod := range pods.Items {
			for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
				limit := int64(4 << 20)
				data, err := c.kube.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name, LimitBytes: &limit}).DoRaw(ctx)
				if err != nil || len(data) >= int(limit) {
					t.Fatal("ClickHouse audit cannot inspect its complete bounded log")
				}
				for _, value := range private {
					if bytes.Contains(data, value) {
						t.Fatal("ClickHouse private material appeared in runtime logs")
					}
				}
			}
		}
	}
}

package cluster

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestOracleDevelopmentTransportDiagnostic(t *testing.T) {
	id := os.Getenv("HAKOPOD_ORACLE_FIXTURE_ID")
	if id == "" || os.Getenv("HAKOPOD_ORACLE_TEST") != "1" {
		t.Skip("no explicit development Oracle fixture")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("development context required")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d := oracleFixture()
	d.ID = id
	d.Spec.Placement.NodeNames = developmentRecoveryFixtureNodes(t)
	object, err := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(id)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Revision, err = strconv.ParseInt(object.GetAnnotations()["hakopod.io/database-revision"], 10, 64)
	if err != nil || d.Revision < 1 {
		t.Fatal("invalid Oracle diagnostic revision")
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(id)).List(ctx, metav1.ListOptions{LabelSelector: oracleEnterpriseMemberLabel + "=true", Limit: 2})
	if err != nil || pods.Continue != "" || len(pods.Items) != 1 {
		t.Fatal("Oracle diagnostic requires one owned SIDB member", err)
	}
	pod := &pods.Items[0]
	member := database.Member{Name: pod.Name, UID: string(pod.UID)}
	secret, err := c.kube.CoreV1().Secrets(pod.Namespace).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mounted := &databaseBoundedWriter{limit: 256}
	if err = c.DatabaseExec(ctx, d, member, []string{"cat", "/etc/hakopod-app/password"}, nil, mounted); err != nil {
		t.Fatal(err)
	}
	if mounted.String() != string(secret.Data["password"]) {
		t.Fatal("mounted application credential differs from its owned Secret")
	}
	conn, err := c.oracleApplicationConnection(ctx, d, member, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var value string
	err = conn.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV','SESSION_USER')||'|'||SYS_CONTEXT('USERENV','CON_NAME') FROM dual").Scan(&value)
	if err != nil {
		t.Fatal(strings.ReplaceAll(err.Error(), string(secret.Data["password"]), "[redacted]"))
	}
	t.Log("Oracle application identity", value)
}

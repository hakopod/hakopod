package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLivePersistentDatabase(t *testing.T) {
	if os.Getenv("HAKOPOD_STORAGE_TEST") != "1" {
		t.Skip("set HAKOPOD_STORAGE_TEST=1 with the optional named development storage installed")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing persistence test outside named development cluster")
	}
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 150 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	application, err := spec.FromTemplate("postgresql", "persistent-fixture", false, 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	application, err = c.Resolve(ctx, application)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Project: "storage-test", Environment: "test", ApplicationID: "storage-live-fixture-v1", OperationID: "persistence-initial", Revision: 1, Spec: application}
	if err = c.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "database-password", "dedicated-test-only-password"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		_ = c.DeleteWorkloadSecret(cleanup, target.Project, target.Environment, target.Spec.Name, "database-password")
		ns, e := c.kube.CoreV1().Namespaces().Get(cleanup, Namespace(target.ApplicationID), metav1.GetOptions{})
		if e == nil && owned(ns, target) == nil {
			_ = c.kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, deleteOptions(ns))
		}
	})
	if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
		t.Fatal(err)
	}
	execute := func(query string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", Namespace(target.ApplicationID), "exec", "deployment/main", "--", "psql", "-U", "hakopod", "-d", "app", "-Atc", query)
		out, e := command.CombinedOutput()
		if e != nil {
			t.Fatalf("fixture SQL failed: %v %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	execute("CREATE TABLE persistence_probe(value text); INSERT INTO persistence_probe VALUES('survives-restart')")
	pvc, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(target.ApplicationID)).Get(ctx, "main-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service := target.Spec.Services["main"]
	service.RestartNonce = "persistence-live-restart"
	target.Spec.Services["main"] = service
	target.Revision++
	target.OperationID = "persistence-restart"
	if _, err = c.Deploy(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	if got := execute("SELECT value FROM persistence_probe"); got != "survives-restart" {
		t.Fatalf("persistent data lost: %s", got)
	}
	next, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(target.ApplicationID)).Get(ctx, "main-data", metav1.GetOptions{})
	if err != nil || next.UID != pvc.UID {
		t.Fatal("restart replaced the persistent claim")
	}
	// Removing the service through ordinary cleanup keeps the data claim.
	empty := target
	empty.Spec.Services = map[string]spec.Service{}
	empty.Previous = &target.Spec
	if err = c.cleanup(ctx, empty); err != nil {
		t.Fatal(err)
	}
	if _, err = c.kube.CoreV1().PersistentVolumeClaims(Namespace(target.ApplicationID)).Get(ctx, "main-data", metav1.GetOptions{}); err != nil {
		t.Fatal("ordinary service removal deleted data")
	}
	fmt.Println("Verified real PostgreSQL data survives service restart; removing a service retains its PVC.")
}

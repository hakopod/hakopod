package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Explicit opt-in: use only disposable fixtures in the named development cluster.
func TestLiveSharedCatalog(t *testing.T) {
	if os.Getenv("HAKOPOD_SHARED_CATALOG_TEST") != "1" {
		t.Skip("opt-in shared catalog runtime acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	client, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 180 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"browserless", "bentopdf", "nginx", "grafana", "qdrant", "rabbitmq", "pocketbase", "vaultwarden"} {
		if selected := os.Getenv("HAKOPOD_CATALOG_TEMPLATE"); selected != "" && selected != id {
			continue
		}
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			if id == "browserless" {
				nodes, err := client.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Fatal(err)
				}
				for _, node := range nodes.Items {
					data, err := client.kube.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/" + node.Name + "/proxy/stats/summary").DoRaw(ctx)
					if err != nil {
						t.Fatal("cannot verify disk headroom", err)
					}
					var summary struct {
						Node struct {
							Fs struct {
								AvailableBytes uint64 `json:"availableBytes"`
							} `json:"fs"`
						} `json:"node"`
					}
					if json.Unmarshal(data, &summary) != nil || summary.Node.Fs.AvailableBytes < 8<<30 {
						t.Fatal("Browserless acceptance requires at least 8 GiB free node disk before image resolution")
					}
				}
			}
			app, err := spec.PlanTemplate(id, spec.TemplateOptions{Name: "catalog-" + id, StorageGiB: 2, SiteURL: func() string {
				if id == "vaultwarden" {
					return "https://catalog.example.test"
				}
				return ""
			}(), Values: func() map[string]string {
				if id == "pocketbase" {
					return map[string]string{"admin-email": "fixture@example.test"}
				}
				return nil
			}()})
			if err != nil {
				t.Fatal(err)
			}
			app, err = client.Resolve(ctx, app)
			if err != nil {
				t.Fatal(err)
			}
			target := Target{Project: "catalog-test", Environment: "test", ApplicationID: fmt.Sprintf("shared-catalog-%s-%d", id, time.Now().UnixNano()), OperationID: "catalog-initial", Revision: 1, Spec: app}
			labels := labelsFor(target, "")
			labels["hakopod.io/acceptance"] = "shared-catalog"
			ns, err := client.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				clean, done := context.WithTimeout(context.Background(), 90*time.Second)
				defer done()
				current, err := client.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
				if err != nil || current.UID != ns.UID || owned(current, target) != nil || current.Labels["hakopod.io/acceptance"] != "shared-catalog" {
					t.Errorf("could not verify fixture ownership; retaining namespace: %v", err)
					return
				}
				for _, ref := range spec.TemplateSecretNames(app) {
					if err := client.DeleteWorkloadSecret(clean, target.Project, target.Environment, app.Name, ref); err != nil {
						t.Error(err)
					}
				}
				if err := client.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(current)); err != nil {
					t.Error(err)
				}
			}()
			for _, ref := range spec.TemplateSecretNames(app) {
				if err := client.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, ref, strings.Repeat("fixture-only-", 6)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := client.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
				t.Fatal(err)
			}
			pods, err := client.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(pods.Items) == 0 {
				t.Fatal("no runtime pods")
			}
			for _, pod := range pods.Items {
				for _, container := range pod.Status.ContainerStatuses {
					if !container.Ready {
						t.Fatal("container not ready", container.Name)
					}
				}
			}
			t.Log("immutable native preset reached Kubernetes readiness under Hakopod security and network policies")
		})
	}
}

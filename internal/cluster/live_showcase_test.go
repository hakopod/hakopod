package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveShowcasePrivateCatalogAndSafeRemoval(t *testing.T) {
	if os.Getenv("HAKOPOD_SHOWCASE_TEST") != "1" {
		t.Skip("set HAKOPOD_SHOWCASE_TEST=1 for disposable sample shop acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("showcase test requires named k3d-hakopod-dev context")
	}
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Showcase()
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ApplicationID: fmt.Sprintf("showcase-fixture-%d", time.Now().UnixNano()), Project: "demo", Environment: "development", OperationID: "showcase-fixture", Revision: 1, Spec: app}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	labels := labelsFor(target, "")
	labels["hakopod.io/acceptance"] = "showcase"
	namespace, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 40*time.Second)
		defer done()
		current, e := c.kube.CoreV1().Namespaces().Get(clean, namespace.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return
		}
		if e != nil || current.UID != namespace.UID || owned(current, target) != nil || current.Labels["hakopod.io/acceptance"] != "showcase" {
			t.Error("showcase fixture ownership changed; cleanup refused")
			return
		}
		if e = c.RemoveShowcase(clean, target.ApplicationID); e != nil {
			t.Error("showcase fixture cleanup", e)
		}
	})
	if _, err = c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
		t.Fatal(err)
	}
	code := `import json,urllib.request
base='http://web:8080'
with urllib.request.urlopen(base+'/',timeout=4) as r:
 page=r.read(20000).decode();assert 'HAKOPOD SAMPLE APPLICATION' in page and 'No payments' in page
with urllib.request.urlopen(base+'/api/catalog',timeout=4) as r: catalog=json.load(r)
assert catalog['sample'] and len(catalog['products'])==3 and catalog['pod'].startswith('api-')
request=urllib.request.Request(base+'/api/checkout',data=b'{"lamp":2,"cup":1}',headers={'Content-Type':'application/json'})
with urllib.request.urlopen(request,timeout=4) as r: checkout=json.load(r)
assert checkout['sample'] and checkout['total']==11600 and checkout['order_created'] is False
print('real private catalog and sample checkout verified')`
	out, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", namespace.Name, "exec", "deployment/web", "--", "python", "-c", code).CombinedOutput()
	if err != nil {
		t.Fatalf("sample behavior failed: %v %s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
	if _, err = c.kube.NetworkingV1().Ingresses(namespace.Name).Get(ctx, "api", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("private API unexpectedly has an ingress", err)
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(namespace.Name).List(ctx, metav1.ListOptions{Limit: 2})
	if err != nil || len(claims.Items) != 0 {
		t.Fatal("stateless sample allocated persistent storage", err)
	}
	if err = c.RemoveShowcase(ctx, target.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.kube.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("sample namespace remains after successful removal", err)
	}
	t.Log("owned sample namespace fully removed; no persistent volumes allocated")
}

package cluster

import (
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLiveProxyConfiguration(t *testing.T) {
	if os.Getenv("HAKOPOD_PROXY_TEST") != "1" {
		t.Skip("requires explicit named-development-cluster proxy test")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing proxy test outside named development cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c, err := New(path, Options{ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := c.proxyConfigMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		current, e := c.proxyConfigMap(cleanup)
		if e != nil {
			t.Error(e)
			return
		}
		if current.Annotations["hakopod.io/proxy-revision"] != "99117" {
			t.Error("proxy changed outside test; preserving operator state")
			return
		}
		current.Data = original.Data
		current.Annotations = original.Annotations
		if _, e = c.kube.CoreV1().ConfigMaps(current.Namespace).Update(cleanup, current, metav1.UpdateOptions{}); e != nil {
			t.Error(e)
		}
	})
	value := "31s"
	if original.Data["timeout-client"] == value {
		value = "32s"
	}
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"timeout-client": value}, original.ResourceVersion, 99117); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ApplyProxyConfiguration(ctx, map[string]string{"timeout-client": value}, original.ResourceVersion, 99117); err != nil {
		t.Fatal("replay failed", err)
	}
	time.Sleep(5 * time.Second)
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", "haproxy-controller", "exec", "deployment/hakopod-ingress-kubernetes-ingress", "--", "haproxy", "-c", "-f", "/etc/haproxy/haproxy.cfg")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated HAProxy configuration did not validate: %v %s", err, out)
	}
	t.Log("Real HAProxy configuration updated, duplicate apply resumed, and generated configuration passed haproxy -c; original settings restored.")
}

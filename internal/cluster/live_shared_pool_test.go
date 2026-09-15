package cluster

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveSandboxedSharedPool(t *testing.T) {
	if os.Getenv("HAKOPOD_SHARED_POOL_TEST") != "1" {
		t.Skip("requires explicitly configured shared sandbox in named dev cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named development cluster")
	}
	policy := func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: "k3d-shared-free-test-0", Pool: "free", RuntimeClass: "runsc", MemoryRequest: "256Mi", EgressPorts: []int32{80, 443}, Quota: map[string]string{"pods": "2", "requests.memory": "768Mi", "limits.memory": "768Mi"}}, nil
	}
	c, err := New(path, Options{DeploymentMode: DeploymentManagedCloud, OperatorNodeLimit: 2, WorkloadPolicy: policy, AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", PublicPort: 18080, RolloutTimeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err = c.CheckWorkloadPool(ctx, "k3d-shared-free-test-0", "free", "runsc"); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse([]byte("name='shared-test'\n[services.web]\nimage='python:3.13-alpine'\npublic=true\nport=8080\ncommand=['python','-m','http.server','8080']"))
	if err != nil {
		t.Fatal(err)
	}
	app, err = c.Resolve(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	targets := []Target{{ApplicationID: "shared-pool-fixture-a", Project: "free-a", Environment: "production", Revision: 1, Spec: app}, {ApplicationID: "shared-pool-fixture-b", Project: "free-b", Environment: "production", Revision: 1, Spec: app}}
	for _, target := range targets {
		t.Cleanup(func() {
			clean, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			ns, err := c.kube.CoreV1().Namespaces().Get(clean, Namespace(target.ApplicationID), metav1.GetOptions{})
			if err == nil && owned(ns, target) == nil {
				_ = c.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(ns))
			}
		})
		if observed, err := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil || observed.Status != "healthy" {
			t.Fatal(observed, err)
		}
	}
	podsA, err := c.kube.CoreV1().Pods(Namespace(targets[0].ApplicationID)).List(ctx, metav1.ListOptions{})
	if err != nil || len(podsA.Items) != 1 {
		t.Fatal(err)
	}
	pod := podsA.Items[0]
	if pod.Spec.NodeName != "k3d-shared-free-test-0" || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "runsc" {
		t.Fatal("escaped shared sandbox", pod.Spec.NodeName)
	}
	run := func(script string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "exec", "-n", pod.Namespace, pod.Name, "--", "python", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("probe failed: %v %s", err, out)
		}
		return out
	}
	t.Log(string(run("print(open('/proc/version').read())")))
	podsB, err := c.kube.CoreV1().Pods(Namespace(targets[1].ApplicationID)).List(ctx, metav1.ListOptions{})
	if err != nil || len(podsB.Items) != 1 {
		t.Fatal(err)
	}
	run("import socket\nfor host,port in [(" + "'" + podsB.Items[0].Status.PodIP + "'" + ",8080),('169.254.169.254',80),('kubernetes.default.svc',443)]:\n try:\n  s=socket.create_connection((host,port),timeout=2)\n except OSError:\n  continue\n raise RuntimeError('isolation failed for '+host)\nprint('cross-tenant, metadata and control API denied')")
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:18080/", nil)
	req.Host = c.hostname(targets[0], "web")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	deployment, err := c.kube.AppsV1().Deployments(pod.Namespace).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Memory().String()) != "256Mi" {
		t.Fatal("memory reservation missing")
	}
}

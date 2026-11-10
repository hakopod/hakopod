package cluster

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func withEgressProxy(app *spec.Application) {
	app.Services["proxy"] = spec.Service{Image: "example/proxy:1", Port: 3128}
	worker := app.Services["worker"]
	worker.Session.EgressProxy = "proxy"
	worker.Env = map[string]string{"HTTP_PROXY": "http://evil:1", "NO_PROXY": "*", "KEEP": "1"}
	worker.TemporaryMounts = []spec.TemporaryMount{{MountPath: "/workspace", SizeMiB: 2048}}
	app.Services["worker"] = worker
}

func createProxyService(t *testing.T, c *Client, r sandbox.Record, clusterIP string) {
	t.Helper()
	target, _, err := sandboxTarget(r)
	if err != nil {
		t.Fatal(err)
	}
	service := serviceObject(target, "proxy", target.Spec.Services["proxy"])
	service.Spec.ClusterIP = clusterIP
	if _, err = c.kube.CoreV1().Services(service.Namespace).Create(context.Background(), service, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxEgressProxyRequiresReadyService(t *testing.T) {
	for name, ip := range map[string]string{"missing": "", "headless": corev1.ClusterIPNone} {
		c, r := sandboxFixtureWith(t, withEgressProxy)
		if ip != "" {
			createProxyService(t, c, r, ip)
		}
		_, err := c.StartSession(context.Background(), r, func(context.Context) error { return nil })
		if err == nil || err.Error() != "session egress proxy service is not ready" {
			t.Fatalf("%s: %v", name, err)
		}
		if pods, _ := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).List(context.Background(), metav1.ListOptions{}); len(pods.Items) != 0 {
			t.Fatalf("%s: pod created", name)
		}
	}
}

func TestSandboxWithoutEgressProxyIsUnchanged(t *testing.T) {
	c, r := sandboxFixture(t)
	r = startSandbox(t, c, r)
	ctx := context.Background()
	p, err := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(ctx, sandboxPodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	app := p.Spec.Containers[0]
	if p.Spec.HostAliases != nil {
		t.Fatal("hostAliases without egress_proxy")
	}
	if !reflect.DeepEqual(app.Command, []string{"/run/hakopod-session/guard", "run", "--", "python", "worker.py"}) {
		t.Fatalf("command changed: %v", app.Command)
	}
	for _, e := range app.Env {
		if e.Name == "HAKOPOD_EGRESS_PROXY" || e.Name == "HTTP_PROXY" {
			t.Fatal("proxy env without egress_proxy")
		}
	}
	policy, err := c.kube.NetworkingV1().NetworkPolicies(SandboxNamespace(r.ID)).Get(ctx, "deny-all", metav1.GetOptions{})
	if err != nil || policy.Spec.Egress != nil || policy.Spec.Ingress != nil {
		t.Fatal("deny-all policy changed", err)
	}
	target, _, _ := sandboxTarget(r)
	for _, np := range policies(target) {
		if len(np.Spec.Ingress) != 0 && np.Name == "hakopod-service-worker" {
			t.Fatal("session service gained ingress")
		}
	}
}

func TestSandboxEgressProxy(t *testing.T) {
	c, r := sandboxFixtureWith(t, withEgressProxy)
	createProxyService(t, c, r, "10.43.7.9")
	r = startSandbox(t, c, r)
	ctx := context.Background()
	p, err := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(ctx, sandboxPodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	app := p.Spec.Containers[0]
	if !reflect.DeepEqual(app.Command, []string{"/run/hakopod-session/guard", "run", "--allow-ip-sockets", "--", "python", "worker.py"}) {
		t.Fatalf("worker command: %v", app.Command)
	}
	if slices.Contains(app.StartupProbe.Exec.Command, "--allow-ip-sockets") {
		t.Fatal("startup probe may open IP sockets")
	}
	url := "http://proxy." + Namespace("sandbox-app") + ".svc.cluster.local:3128"
	env := map[string][]string{}
	for _, e := range app.Env {
		env[e.Name] = append(env[e.Name], e.Value)
	}
	for _, name := range []string{"HAKOPOD_EGRESS_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if !reflect.DeepEqual(env[name], []string{url}) {
			t.Fatalf("%s = %v", name, env[name])
		}
	}
	if !reflect.DeepEqual(env["NO_PROXY"], []string{""}) || !reflect.DeepEqual(env["no_proxy"], []string{""}) || !reflect.DeepEqual(env["KEEP"], []string{"1"}) {
		t.Fatalf("env: %v", env)
	}
	if !reflect.DeepEqual(p.Spec.HostAliases, []corev1.HostAlias{{IP: "10.43.7.9", Hostnames: []string{"proxy." + Namespace("sandbox-app") + ".svc.cluster.local"}}}) || p.Spec.DNSPolicy != corev1.DNSNone || p.Spec.DNSConfig.Nameservers[0] != "127.0.0.1" {
		t.Fatalf("hostAliases/DNS: %+v %v", p.Spec.HostAliases, p.Spec.DNSConfig)
	}
	var disk *corev1.Volume
	for i := range p.Spec.Volumes {
		if p.Spec.Volumes[i].Name == "temporary-0" {
			disk = &p.Spec.Volumes[i]
		}
	}
	if disk == nil || disk.EmptyDir.Medium != corev1.StorageMediumDefault || disk.EmptyDir.SizeLimit.Cmp(resource.MustParse("2048Mi")) != 0 {
		t.Fatalf("disk scratch: %+v", disk)
	}
	policy, err := c.kube.NetworkingV1().NetworkPolicies(SandboxNamespace(r.ID)).Get(ctx, "deny-all", metav1.GetOptions{})
	if err != nil || len(policy.Spec.Egress) != 1 || len(policy.Spec.Ingress) != 0 || len(policy.Spec.Egress[0].Ports) != 1 || policy.Spec.Egress[0].Ports[0].Port.IntValue() != 3128 {
		t.Fatalf("session policy: %+v %v", policy.Spec, err)
	}
	if peer := policy.Spec.Egress[0].To[0]; peer.PodSelector.MatchLabels[serviceKey] != "proxy" || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != Namespace("sandbox-app") {
		t.Fatalf("session egress peer: %+v", peer)
	}
	if to := policy.Spec.Egress[0].To; len(to) != 2 || to[1].IPBlock == nil || to[1].IPBlock.CIDR != "10.43.7.9/32" || len(to[1].IPBlock.Except) != 0 {
		t.Fatalf("session egress ipBlock: %+v", to)
	}
	if _, err = c.ObserveSession(ctx, r); err != nil {
		t.Fatal("observe rejected the egress policy", err)
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, SandboxNamespace(r.ID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target, _, _ := sandboxTarget(r)
	found := false
	for _, np := range policies(target) {
		if np.Name != "hakopod-service-proxy" {
			continue
		}
		for _, rule := range np.Spec.Ingress {
			peer := rule.From[0]
			if peer.NamespaceSelector == nil || len(peer.NamespaceSelector.MatchExpressions) == 0 {
				continue
			}
			nsSel, _ := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
			podSel, _ := metav1.LabelSelectorAsSelector(peer.PodSelector)
			found = nsSel.Matches(labels.Set(ns.Labels)) && podSel.Matches(labels.Set(p.Labels)) && rule.Ports[0].Port.IntValue() == 3128
			out, _ := json.MarshalIndent(map[string]any{"command": app.Command, "hostAliases": p.Spec.HostAliases, "env": app.Env[len(app.Env)-9:], "session_policy": policy.Spec, "proxy_ingress": rule}, "", "  ")
			t.Logf("%s", out)
		}
	}
	if !found {
		t.Fatal("proxy does not admit the session pod")
	}
}

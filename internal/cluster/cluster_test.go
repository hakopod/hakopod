package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func testTarget(t *testing.T) Target {
	t.Helper()
	app, err := spec.Normalize(spec.Application{Name: "demo", Services: map[string]spec.Service{
		"web":    {Image: "docker.io/library/python@sha256:" + strings.Repeat("a", 64), Port: 8080, Public: true},
		"api":    {Image: "docker.io/library/python@sha256:" + strings.Repeat("a", 64), Port: 8080},
		"worker": {Image: "docker.io/library/python@sha256:" + strings.Repeat("a", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return Target{ApplicationID: "opaque-app-id", Project: "demo", Environment: "test", OperationID: "op-1", Revision: 1, Spec: app}
}

func TestPodSecurityBudgetsAndWorker(t *testing.T) {
	target := testTarget(t)
	for _, name := range spec.Names(target.Spec) {
		d := deployment(target, name, target.Spec.Services[name], time.Minute)
		pod := d.Spec.Template.Spec
		container := pod.Containers[0]
		if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
			t.Fatal("pod receives cluster credentials")
		}
		if !*pod.SecurityContext.RunAsNonRoot || *container.SecurityContext.AllowPrivilegeEscalation || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
			t.Fatal("missing container confinement")
		}
		if container.Resources.Requests.Cpu().IsZero() || container.Resources.Limits.Memory().IsZero() {
			t.Fatal("unbounded container resources")
		}
		if name == "worker" && (len(container.Ports) > 0 || container.ReadinessProbe != nil) {
			t.Fatal("worker gets a fictional listening port")
		}
		if name != "worker" && container.ReadinessProbe == nil {
			t.Fatal("network traffic not readiness gated")
		}
		if container.LivenessProbe != nil {
			t.Fatal("readiness incorrectly reused for liveness")
		}
	}
}

func TestPolicyNetworkMembershipAndInternalEgress(t *testing.T) {
	target := testTarget(t)
	target.Spec.Networks = map[string]spec.Network{"frontend": {}, "backend": {Internal: true}, "default": {}}
	web := target.Spec.Services["web"]
	web.Networks = []string{"frontend", "backend"}
	target.Spec.Services["web"] = web
	api := target.Spec.Services["api"]
	api.Networks = []string{"backend"}
	target.Spec.Services["api"] = api
	worker := target.Spec.Services["worker"]
	worker.Networks = []string{"default"}
	target.Spec.Services["worker"] = worker
	items := policies(target)
	if len(items[0].Spec.Ingress) != 0 || len(items[0].Spec.Egress) != 0 || len(items[0].Spec.PolicyTypes) != 2 {
		t.Fatal("missing deny-all baseline")
	}
	byService := map[string]*networkingv1.NetworkPolicy{}
	for _, p := range items {
		byService[p.Labels[serviceKey]] = p
	}
	for name, p := range byService {
		if name == "" {
			continue
		}
		external := false
		dns := false
		for _, rule := range p.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock != nil {
					external = true
					if len(peer.IPBlock.Except) < 8 {
						t.Fatal("unrestricted access to private or metadata networks")
					}
				}
				if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "kube-system" {
					dns = true
				}
			}
		}
		if !dns {
			t.Fatalf("%s lacks DNS exception", name)
		}
		if (name == "api") == external {
			t.Fatalf("wrong external egress for %s: %v", name, external)
		}
	}
	if len(byService["worker"].Spec.Ingress) != 0 {
		t.Fatal("worker has inbound policy exceptions")
	}
	for _, rule := range byService["api"].Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil || peer.PodSelector == nil || !spec.AllowsPeer(target.Spec, peer.PodSelector.MatchLabels[serviceKey], "api") {
				t.Fatal("private API accepts traffic outside its shared network")
			}
		}
	}
	for _, rule := range byService["api"].Spec.Egress {
		for _, peer := range rule.To {
			if peer.PodSelector != nil && peer.PodSelector.MatchLabels[serviceKey] == "worker" {
				t.Fatal("disjoint networks can communicate")
			}
		}
	}
}

func TestHPAAndReloadAnnotationOwnership(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.Autoscaling = &spec.Autoscaling{MinReplicas: 1, MaxReplicas: 5, TargetCPU: 70}
	current := deployment(target, "api", svc, time.Minute)
	current.Spec.Replicas = ptr(int32(4))
	current.Spec.Template.Annotations = map[string]string{"reloader.example/restartedAt": "now"}
	current.Annotations["other-controller/key"] = "value"
	kube := fake.NewClientset(current)
	c := &Client{kube: kube, options: Options{RolloutTimeout: time.Minute}}
	if _, err := c.applyDeployment(context.Background(), target, "api", svc); err != nil {
		t.Fatal(err)
	}
	after, _ := kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(context.Background(), "api", metav1.GetOptions{})
	if *after.Spec.Replicas != 4 {
		t.Fatal("reconciler reset HPA-owned replica count")
	}
	if !reflect.DeepEqual(current.Spec.Template, after.Spec.Template) {
		t.Fatal("unchanged deployment alters pod template or reload annotations")
	}
	if after.Annotations["other-controller/key"] != "value" {
		t.Fatal("another controller annotation was removed")
	}
}

func TestRefusesUnownedWorkload(t *testing.T) {
	target := testTarget(t)
	current := deployment(target, "api", target.Spec.Services["api"], time.Minute)
	current.Labels = nil
	c := &Client{kube: fake.NewClientset(current), options: Options{RolloutTimeout: time.Minute}}
	if _, err := c.applyDeployment(context.Background(), target, "api", target.Spec.Services["api"]); err == nil || !strings.Contains(err.Error(), "unowned") {
		t.Fatalf("overwrote another controller's resource: %v", err)
	}
}

func TestDeployBaselineBeforeWorkloadsAndNoWorkerService(t *testing.T) {
	target := testTarget(t)
	kube := fake.NewClientset()
	kube.PrependReactor("create", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		d := action.(ktesting.CreateAction).GetObject().(*appsv1.Deployment)
		d.Generation = 1
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: *d.Spec.Replicas, ReadyReplicas: *d.Spec.Replicas, AvailableReplicas: *d.Spec.Replicas, UpdatedReplicas: *d.Spec.Replicas}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: d.Name + "-test", Namespace: d.Namespace, Labels: d.Spec.Template.Labels}, Spec: d.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
		if err := kube.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, d.Namespace); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})
	c := &Client{kube: kube, options: Options{AppDomain: "apps.example.com", IngressClass: "haproxy", RolloutTimeout: time.Second}}
	observation, err := c.Deploy(context.Background(), target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != "healthy" {
		t.Fatalf("unexpected actual observation: %+v", observation)
	}
	policy, quota := false, false
	for _, action := range kube.Actions() {
		if action.GetVerb() != "create" {
			continue
		}
		switch action.GetResource().Resource {
		case "networkpolicies":
			policy = true
		case "resourcequotas":
			quota = true
		case "deployments":
			if !policy || !quota {
				t.Fatal("workload created before baseline policy and quota")
			}
		}
	}
	services, _ := kube.CoreV1().Services(Namespace(target.ApplicationID)).List(context.Background(), metav1.ListOptions{})
	if len(services.Items) != 2 {
		t.Fatalf("worker acquired Service: %v", services.Items)
	}
	ingresses, _ := kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).List(context.Background(), metav1.ListOptions{})
	if len(ingresses.Items) != 1 || ingresses.Items[0].Name != "web" {
		t.Fatal("private service exposed through ingress")
	}
}

func TestNewGenerationDoesNotReuseOldPodReadiness(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.Healthcheck = "/readyz"
	old := deployment(target, "api", svc, time.Minute)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-old", Namespace: old.Namespace, Labels: old.Spec.Template.Labels}, Spec: old.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	svc.Healthcheck = "/not-ready"
	current := deployment(target, "api", svc, time.Minute)
	current.Generation = 2
	current.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if !readyDeployment(current) {
		t.Fatal("test must exercise transient aggregate-complete counters")
	}
	c := &Client{kube: fake.NewClientset(pod)}
	ready, err := c.readyPods(context.Background(), target, "api", current)
	if err != nil || ready {
		t.Fatalf("old ready pod satisfied new failing readiness configuration: %v,%v", ready, err)
	}
	current.Status.ObservedGeneration = 1
	current.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}
	if deploymentFailure(current) != "" {
		t.Fatal("recovery inherited previous generation's deadline failure")
	}
}

func TestRevokedStepCannotCreateNamespace(t *testing.T) {
	target := testTarget(t)
	target.BeforeStep = func(context.Context) error { return fmt.Errorf("revoked") }
	kube := fake.NewClientset()
	c := &Client{kube: kube, options: Options{AppDomain: "apps.example.com"}}
	_, err := c.Deploy(context.Background(), target, nil)
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("missing authorization boundary: %v", err)
	}
	if len(kube.Actions()) != 0 {
		t.Fatal("Kubernetes effect occurred after authority was revoked")
	}
}

func TestCleanupFindsCancelledFirstReleaseWithoutDeletingUnownedObjects(t *testing.T) {
	target := testTarget(t)
	stale := deployment(target, "abandoned", target.Spec.Services["api"], time.Minute)
	unowned := deployment(target, "operator-owned", target.Spec.Services["api"], time.Minute)
	unowned.Labels = map[string]string{"app": "operator"}
	kube := fake.NewClientset(stale, unowned)
	c := &Client{kube: kube}
	if target.Previous != nil {
		t.Fatal("test should exercise no previous healthy release")
	}
	if err := c.cleanup(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := kube.AppsV1().Deployments(stale.Namespace).Get(context.Background(), stale.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("cancelled partial resource leaked")
	}
	if _, err := kube.AppsV1().Deployments(unowned.Namespace).Get(context.Background(), unowned.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("unowned resource was removed")
	}
}

func TestImageReferenceParsing(t *testing.T) {
	for _, test := range []struct{ in, registry, repository, reference string }{
		{"python:3.13", "registry-1.docker.io", "library/python", "3.13"},
		{"ghcr.io/example/app:v1", "ghcr.io", "example/app", "v1"},
		{"registry.example.com:443/team/app", "registry.example.com:443", "team/app", "latest"},
		{"python:tag@sha256:" + strings.Repeat("a", 64), "registry-1.docker.io", "library/python", "sha256:" + strings.Repeat("a", 64)},
	} {
		ref, err := parseReference(test.in)
		if err != nil || ref.registry != test.registry || ref.repository != test.repository || ref.reference != test.reference {
			t.Fatalf("%q => %+v,%v", test.in, ref, err)
		}
	}
}

func TestPublicRegistryAddressGate(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "172.16.0.1", "192.168.1.1", "100.64.0.1", "::1", "fc00::1", "::ffff:127.0.0.1", "224.0.0.1", "198.18.0.1"} {
		if publicAddress(netip.MustParseAddr(address)) {
			t.Fatalf("unsafe registry address accepted: %s", address)
		}
	}
	if !publicAddress(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public endpoint rejected")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveDigestAndArchitecture(t *testing.T) {
	manifest := `{"schemaVersion":2,"manifests":[{"platform":{"os":"linux","architecture":"arm64"}}]}`
	c := &Client{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(manifest)), Header: make(http.Header)}, nil
	})}}
	value, err := c.resolveImage(context.Background(), "python:tag", []string{"arm64"})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(manifest))
	if value != fmt.Sprintf("docker.io/library/python@sha256:%x", hash) {
		t.Fatalf("wrong immutable digest: %s", value)
	}
	if _, err := c.resolveImage(context.Background(), "python:tag", []string{"amd64"}); err == nil || !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("unsupported node architecture accepted: %v", err)
	}
	if _, err := c.resolveImage(context.Background(), "python@sha256:"+strings.Repeat("a", 64), []string{"arm64"}); err == nil {
		t.Fatal("manifest digest mismatch accepted")
	}
}

func TestNodesReportActualReadiness(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "arm64", KubeletVersion: "v1.35.8"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "demo"}, Spec: corev1.PodSpec{NodeName: "worker"}}
	c := &Client{kube: fake.NewClientset(node, pod)}
	nodes, err := c.Nodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Ready || nodes[0].Pods != 1 {
		t.Fatalf("invented or missing node state: %+v %v", nodes, err)
	}
}

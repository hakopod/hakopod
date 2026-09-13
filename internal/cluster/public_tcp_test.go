package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func publicTCPTestClient(t *testing.T) (*Client, Target) {
	t.Helper()
	target := Target{ApplicationID: "tcp-fixture", Project: "test", Environment: "test", Revision: 1, Spec: spec.Application{Name: "smtp", SchemaVersion: 1, Services: map[string]spec.Service{"mail": {Image: "example.org/mail:latest", Port: 2525, PublicTCP: []spec.PublicTCPListener{{Port: 587, TargetPort: 2525, SourceCIDRs: []string{"192.0.2.0/24"}}}}}}}
	var err error
	target.Spec, err = spec.Normalize(target.Spec)
	if err != nil {
		t.Fatal(err)
	}
	meta := metav1.ObjectMeta{Name: "hakopod-ingress-kubernetes-ingress", Namespace: "haproxy-controller", Labels: map[string]string{"app.kubernetes.io/instance": "hakopod-ingress", "app.kubernetes.io/name": "kubernetes-ingress"}, Annotations: map[string]string{"meta.helm.sh/release-name": "hakopod-ingress", "meta.helm.sh/release-namespace": "haproxy-controller"}}
	cm := &corev1.ConfigMap{ObjectMeta: meta}
	dep := &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(1)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kubernetes-ingress-controller", Args: []string{"--ingress.class=haproxy"}, Ports: []corev1.ContainerPort{{Name: "smtp", ContainerPort: 587, HostPort: 587, Protocol: corev1.ProtocolTCP}}}}}}}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1, Replicas: 1}}
	svc := &corev1.Service{ObjectMeta: meta, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: meta.Labels, Ports: []corev1.ServicePort{{Name: "smtp", Port: 587, TargetPort: intstr.FromInt32(587), Protocol: corev1.ProtocolTCP}}}}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{publicTCPResource: "TCPList", {Group: "ingress.v1.haproxy.org", Version: "v1", Resource: "tcps"}: "TCPList"})
	return &Client{publicTCPAck: func(context.Context, Target, []any, map[string]string) error { return nil }, kube: fake.NewClientset(cm, dep, svc), dynamic: dynamic, options: Options{ProxyNamespace: meta.Namespace, ProxyConfigMap: meta.Name, ProxyRelease: "hakopod-ingress", IngressClass: "haproxy", PublicTCPPorts: []int32{587}}}, target
}

func TestPublicTCPReservationAndRouting(t *testing.T) {
	c, target := publicTCPTestClient(t)
	ctx := context.Background()
	if err := c.ValidatePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	claims, _ := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{LabelSelector: publicTCPClaimLabel + "=true"})
	if len(claims.Items) != 0 {
		t.Fatal("plan created a claim")
	}
	if err := c.PreparePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcilePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	other := target
	other.ApplicationID = "other"
	if err := c.PreparePublicTCP(ctx, other); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatal("second app could claim SMTP port", err)
	}
	got, err := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(target.ApplicationID)).Get(ctx, publicTCPName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	models, _, _ := unstructured.NestedSlice(got.Object, "spec")
	frontend := models[0].(map[string]any)["frontend"].(map[string]any)
	if frontend["tcp_request_rule_list"].([]any)[0].(map[string]any)["action"] != "reject" {
		t.Fatal("source restrictions not enforced")
	}
	if _, exists := frontend["ssl"]; exists {
		t.Fatal("TLS would terminate at ingress")
	}
	statuses, err := c.ObservePublicTCP(ctx, target, "mail")
	if err != nil || len(statuses) != 1 || statuses[0].Status != "configured" {
		t.Fatal(statuses, err)
	}
	old := target.Spec
	svc := target.Spec.Services["mail"]
	svc.PublicTCP = nil
	target.Spec.Services["mail"] = svc
	target.Previous = &old
	if err := c.PreparePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	removed, err := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(target.ApplicationID)).Get(ctx, publicTCPName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tombstone, _, _ := unstructured.NestedSlice(removed.Object, "spec")
	if len(tombstone) != 0 {
		t.Fatal("removed public listener retained")
	}
	claims, _ = c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{LabelSelector: publicTCPClaimLabel + "=true"})
	if len(claims.Items) != 1 {
		t.Fatal("rollback port reservation disappeared")
	}
}
func TestPublicTCPRejectsUnsupportedIngressAndConflicts(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Client, Target)
	}{
		{"operator-disabled", func(c *Client, _ Target) { c.options.PublicTCPPorts = nil }},
		{"not-provisioned", func(c *Client, _ Target) {
			d, _ := c.kube.AppsV1().Deployments(c.options.ProxyNamespace).Get(context.Background(), c.options.ProxyConfigMap, metav1.GetOptions{})
			d.Spec.Template.Spec.Containers[0].Ports = nil
			c.kube.AppsV1().Deployments(d.Namespace).Update(context.Background(), d, metav1.UpdateOptions{})
		}},
		{"unowned", func(c *Client, _ Target) {
			s, _ := c.kube.CoreV1().Services(c.options.ProxyNamespace).Get(context.Background(), c.options.ProxyConfigMap, metav1.GetOptions{})
			s.Annotations = nil
			c.kube.CoreV1().Services(s.Namespace).Update(context.Background(), s, metav1.UpdateOptions{})
		}},
		{"TCP-configmap", func(c *Client, _ Target) {
			d, _ := c.kube.AppsV1().Deployments(c.options.ProxyNamespace).Get(context.Background(), c.options.ProxyConfigMap, metav1.GetOptions{})
			d.Spec.Template.Spec.Containers[0].Args = append(d.Spec.Template.Spec.Containers[0].Args, "--configmap-tcp-services=system/tcp")
			c.kube.AppsV1().Deployments(d.Namespace).Update(context.Background(), d, metav1.UpdateOptions{})
		}},
		{"unowned-route", func(c *Client, target Target) {
			o := publicTCPObject(target, "haproxy")
			o.SetNamespace("operator")
			o.SetName("operator-route")
			o.SetLabels(nil)
			c.dynamic.Resource(publicTCPResource).Namespace(o.GetNamespace()).Create(context.Background(), o, metav1.CreateOptions{})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, target := publicTCPTestClient(t)
			test.change(c, target)
			if err := c.ValidatePublicTCP(context.Background(), target); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
func TestPublicTCPRestrictedChangeClosesPreviousListener(t *testing.T) {
	c, target := publicTCPTestClient(t)
	ctx := context.Background()
	if err := c.PreparePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcilePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	target.Spec.Services["mail"].PublicTCP[0].SourceCIDRs = []string{"198.51.100.0/24"}
	if err := c.PreparePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	removed, err := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(target.ApplicationID)).Get(ctx, publicTCPName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tombstone, _, _ := unstructured.NestedSlice(removed.Object, "spec")
	if len(tombstone) != 0 {
		t.Fatal("old broader source ACL retained during rollout")
	}
	target.BeforeStep = func(context.Context) error { return context.Canceled }
	if err := c.ReconcilePublicTCP(ctx, target); err != context.Canceled {
		t.Fatal("revoked operation changed ingress", err)
	}
}
func TestPublicTCPPrivatePolicyRemainsPrivate(t *testing.T) {
	_, target := publicTCPTestClient(t)
	for _, policy := range policies(target) {
		if policy.Name != "hakopod-service-mail" {
			continue
		}
		before := len(policy.Spec.Ingress)
		publicTCPPolicy(target.Spec.Services["mail"], policy)
		if len(policy.Spec.Ingress) != before+1 {
			t.Fatal("TCP ingress rule missing")
		}
		rule := policy.Spec.Ingress[len(policy.Spec.Ingress)-1]
		if rule.From[0].PodSelector == nil || rule.Ports[0].Port.IntVal != 2525 {
			t.Fatal("TCP policy broadened beyond ingress and declared target")
		}
	}
}

func TestPublicTCPRuntimeAcknowledgement(t *testing.T) {
	_, target := publicTCPTestClient(t)
	models := publicTCPModels(target)
	name := "tcpcr_" + Namespace(target.ApplicationID) + "_" + models[0].(map[string]any)["name"].(string)
	config := "frontend " + name + "\n  mode tcp\n  maxconn 256\n  timeout client 300000\n  bind 0.0.0.0:587 name v4\n  acl allowed_source src 192.0.2.0/24\n  tcp-request connection reject unless allowed_source\n  default_backend " + Namespace(target.ApplicationID) + "_svc_mail_service\n"
	state, err := parsePublicTCPRuntime("PID\n52\nCONFIG\n" + config + "\nACTIVE\n" + name + "\nEND\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = validatePublicTCPRuntime(target, models, state); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ old, new string }{{"192.0.2.0/24", "0.0.0.0/0"}, {"bind 0.0.0.0:587", "bind 0.0.0.0:2587"}, {"_svc_mail_service", "_svc_other_service"}, {"unless allowed_source", "if allowed_source"}} {
		bad := state
		bad.configuration = strings.ReplaceAll(config, change.old, change.new)
		if err = validatePublicTCPRuntime(target, models, bad); err == nil {
			t.Fatal("unreviewed active route acknowledged", change)
		}
	}
	inactive := state
	inactive.active = nil
	if err = validatePublicTCPRuntime(target, models, inactive); err == nil {
		t.Fatal("disk-only route acknowledged")
	}
	if err = validatePublicTCPRuntime(target, nil, state); err == nil {
		t.Fatal("removed route still active")
	}
	if _, err = parsePublicTCPRuntime("PID\n52\nCONFIG\nACTIVE\n"); err == nil {
		t.Fatal("truncated response accepted")
	}
}

func TestPublicTCPEmptyRuntimeAcknowledgement(t *testing.T) {
	state, err := parsePublicTCPRuntime("PID\n52\nCONFIG\nACTIVE\nEND\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = validatePublicTCPRuntime(Target{}, nil, state); err != nil {
		t.Fatal(err)
	}
}

func TestPublicTCPFailedAcknowledgementRemainsPendingOnRetry(t *testing.T) {
	c, target := publicTCPTestClient(t)
	ctx := context.Background()
	if err := c.PreparePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	c.publicTCPAck = func(context.Context, Target, []any, map[string]string) error { return fmt.Errorf("reload rejected") }
	for i := 0; i < 2; i++ {
		if err := c.ReconcilePublicTCP(ctx, target); err == nil {
			t.Fatal("unacknowledged reload accepted on retry")
		}
	}
	statuses, err := c.ObservePublicTCP(ctx, target, "mail")
	if err != nil || statuses[0].Status != "pending" {
		t.Fatal("unacknowledged spec appeared configured", statuses, err)
	}
	c.publicTCPAck = func(context.Context, Target, []any, map[string]string) error { return nil }
	if err = c.ReconcilePublicTCP(ctx, target); err != nil {
		t.Fatal(err)
	}
	// A failed removal retains a tombstone and retries acknowledgement even
	// without a previous-revision specification.
	svc := target.Spec.Services["mail"]
	svc.PublicTCP = nil
	target.Spec.Services["mail"] = svc
	c.publicTCPAck = func(context.Context, Target, []any, map[string]string) error {
		return fmt.Errorf("old worker still serving")
	}
	for i := 0; i < 2; i++ {
		if err = c.PreparePublicTCP(ctx, target); err == nil {
			t.Fatal("unacknowledged deletion accepted on retry")
		}
	}
	obj, err := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(target.ApplicationID)).Get(ctx, publicTCPName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAnnotations()["hakopod.io/tcp-acknowledged"] != "" {
		t.Fatal("stale success marker survived restrictive change")
	}
}

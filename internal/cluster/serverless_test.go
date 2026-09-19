package cluster

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestServerlessOwnedActivationAndCleanup(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.Serverless = &spec.Serverless{StartupTimeoutSeconds: 60, RequestTimeoutSeconds: 60}
	target.Spec.Services["web"] = svc
	ctx := context.Background()
	c := &Client{kube: fake.NewClientset(), options: Options{ServerlessAddress: "192.0.2.10:8082", AppDomain: "apps.example.test"}}
	if err := c.applyService(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if err := c.applyService(ctx, target, "web", svc); err != nil {
		t.Fatal("activation update", err)
	}
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	ing, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if err != nil || ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != ActivationServiceName("web") || ing.Annotations["haproxy.org/timeout-server"] != "130s" {
		t.Fatal(ing, err)
	}
	ep, err := c.kube.DiscoveryV1().EndpointSlices(Namespace(target.ApplicationID)).Get(ctx, ActivationServiceName("web"), metav1.GetOptions{})
	if err != nil || ep.Endpoints[0].Addresses[0] != "192.0.2.10" || *ep.Ports[0].Port != 8082 {
		t.Fatal(ep, err)
	}
	svc.Serverless = nil
	if err = c.applyService(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if err = c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if _, err = c.kube.CoreV1().Services(Namespace(target.ApplicationID)).Get(ctx, ActivationServiceName("web"), metav1.GetOptions{}); err == nil {
		t.Fatal("activation retained after opting out")
	}
	ing, _ = c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	if ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != "web" || ing.Annotations["haproxy.org/timeout-server"] != "" {
		t.Fatal("ordinary routing was not restored")
	}
}

func TestServerlessGatewayAllowsOnlyManagementNodeHostAddresses(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.Serverless = &spec.Serverless{}
	target.Spec.Services["web"] = svc
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "manager", Annotations: map[string]string{"flannel.alpha.coreos.com/backend-type": "vxlan"}}, Spec: corev1.NodeSpec{PodCIDR: "10.42.8.0/24"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.10"}}}}
	c := &Client{kube: fake.NewClientset(node), options: Options{ServerlessAddress: "192.0.2.10:8082"}}
	ips, err := c.serverlessGatewaySources(context.Background(), target)
	if err != nil || !reflect.DeepEqual(ips, []string{"192.0.2.10", "10.42.8.0", "10.42.8.1"}) {
		t.Fatal(ips, err)
	}
	target.serverlessGatewayIPs = ips
	want := map[string]bool{"192.0.2.10/32": false, "10.42.8.0/32": false, "10.42.8.1/32": false}
	for _, policy := range policies(target) {
		for _, rule := range policy.Spec.Ingress {
			for _, peer := range rule.From {
				if peer.IPBlock != nil {
					if _, ok := want[peer.IPBlock.CIDR]; !ok || len(rule.Ports) != 1 || rule.Ports[0].Port.IntVal != svc.Port {
						t.Fatal("gateway policy escaped exact host IPs and service port", rule)
					}
					want[peer.IPBlock.CIDR] = true
				}
			}
		}
	}
	for ip, found := range want {
		if !found {
			t.Fatal("missing gateway address", ip)
		}
	}
	c.options.ServerlessAddress = "192.0.2.11:8082"
	if _, err = c.serverlessGatewaySources(context.Background(), target); err == nil {
		t.Fatal("unrecognized gateway node admitted")
	}
}

func TestServerlessBackendInventoryIsBatchedAndOwned(t *testing.T) {
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.Serverless = &spec.Serverless{}
	target.Spec.Services["web"] = svc
	labels := labelsFor(target, "web")
	labels[serverlessBackendLabel] = "true"
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: Namespace(target.ApplicationID), Labels: labels}, Spec: corev1.ServiceSpec{ClusterIP: "10.43.1.2", Selector: labelsFor(target, "web")}}
	k := fake.NewClientset(service)
	c := &Client{kube: k}
	b, err := c.ServerlessBackends(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 512; i++ {
		if _, err := b.Backend(target, "web"); err != nil {
			t.Fatal(err)
		}
	}
	if len(k.Actions()) != 1 || k.Actions()[0].GetVerb() != "list" {
		t.Fatal("routing made a Kubernetes request per service")
	}
	b.services[service.Namespace+"/web"].Labels[ownerKey] = "someone-else"
	if _, err = b.Backend(target, "web"); err == nil {
		t.Fatal("unowned Service admitted")
	}
}
func TestExplicitServerlessAllowsMultipleServicesButNoManualWake(t *testing.T) {
	c, target, _ := idleFixture(t)
	c.options.WorkloadPolicy = nil
	c.options.ServerlessAddress = "192.0.2.10:8082"
	svc := target.Spec.Services["web"]
	svc.Serverless = &spec.Serverless{}
	target.Spec.Services["web"] = svc
	target.Spec.Services["other"] = spec.Service{Image: "nginx:alpine"}
	if err := c.SetHTTPIdle(context.Background(), target, "web", true); err != nil {
		t.Fatal(err)
	}
	svc.Serverless.MinReplicas = 1
	target.Spec.Services["web"] = svc
	if err := c.SetHTTPIdle(context.Background(), target, "web", true); err == nil {
		t.Fatal("always warm service allowed to sleep")
	}
	svc.Suspended = true
	target.Spec.Services["web"] = svc
	if err := c.SetHTTPIdle(context.Background(), target, "web", false); err == nil {
		t.Fatal("manual stop awakened")
	}
	c.options.ServerlessAddress = ""
	if err := c.validateServerless(target); err == nil {
		t.Fatal("disabled installation admitted serverless")
	}
}

func TestServerlessAddressRejectsUnusableTargets(t *testing.T) {
	for _, address := range []string{"localhost:8082", "127.0.0.1:8082", "0.0.0.0:8082", "169.254.169.254:8082", "224.0.0.1:8082", "192.0.2.1:80", "192.0.2.1:70000"} {
		if ValidateServerlessAddress(address) == nil {
			t.Fatal("invalid gateway accepted", address)
		}
	}
	if err := ValidateServerlessAddress("10.0.0.10:8082"); err != nil {
		t.Fatal(err)
	}
}

func TestServerlessPortCannotBeReusedForPublicTCP(t *testing.T) {
	for _, port := range []int32{8082, 18082} {
		address := fmt.Sprintf("192.0.2.10:%d", port)
		if _, err := New("", Options{ServerlessAddress: address, PublicTCPPorts: []int32{port}}); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatal("conflicting operator ports accepted", err)
		}
		c := &Client{options: Options{ServerlessAddress: address}}
		target := Target{Spec: spec.Application{Services: map[string]spec.Service{"tcp": {PublicTCP: []spec.PublicTCPListener{{Port: port}}}}}}
		if err := c.ValidatePublicTCP(context.Background(), target); err == nil || !strings.Contains(err.Error(), "activation gateway") {
			t.Fatal("application claimed activation port", err)
		}
	}
}

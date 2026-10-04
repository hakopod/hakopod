package cluster

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func databaseNetworkMaintenanceFixture(t *testing.T) (*Client, *fake.Clientset, database.Resource, *networkingv1.NetworkPolicy) {
	t.Helper()
	d := database.Resource{ID: strings.Repeat("a", 32), Revision: 3, Status: "ready", PublicEndpointAccess: true,
		Spec: database.Spec{Engine: "postgresql", Version: "17"}}
	port := int32(6443)
	kube := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-identity", Labels: databaseLabels(d)}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIPs: []string{"10.43.0.1"}}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default", Labels: map[string]string{discoveryv1.LabelServiceName: "kubernetes"}},
			AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"192.0.2.10"}}}},
	)
	c := &Client{kube: kube, options: Options{ProxyNamespace: "ingress", ProxyRelease: "hakopod"}}
	if err := c.ReconcileDatabaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	policy, err := kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policy.UID, policy.ResourceVersion = "policy-identity", "17"
	policy.Annotations = map[string]string{"fixture": "preserve-metadata"}
	// The fake client does not apply the API server's TCP default. Model its
	// response so nil desired protocols cannot hide repeated policy writes.
	tcp := corev1.ProtocolTCP
	defaultPorts := func(ports []networkingv1.NetworkPolicyPort) {
		for i := range ports {
			if ports[i].Protocol == nil {
				ports[i].Protocol = &tcp
			}
		}
	}
	for i := range policy.Spec.Ingress {
		defaultPorts(policy.Spec.Ingress[i].Ports)
	}
	for i := range policy.Spec.Egress {
		defaultPorts(policy.Spec.Egress[i].Ports)
	}
	if err = kube.Tracker().Update(networkingv1.SchemeGroupVersion.WithResource("networkpolicies"), policy, policy.Namespace); err != nil {
		t.Fatal(err)
	}
	slice, err := kube.DiscoveryV1().EndpointSlices("default").Get(context.Background(), "kubernetes", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slice.Endpoints[0].Addresses = []string{"192.0.2.20"}
	if err = kube.Tracker().Update(discoveryv1.SchemeGroupVersion.WithResource("endpointslices"), slice, slice.Namespace); err != nil {
		t.Fatal(err)
	}
	kube.ClearActions()
	return c, kube, d, policy
}

func TestDatabaseNetworkMaintenanceRejectsUnownedNamespace(t *testing.T) {
	for _, state := range []string{"missing", "foreign", "unmanaged", "terminating"} {
		for _, existingPolicy := range []bool{true, false} {
			t.Run(state+"/policy="+fmt.Sprint(existingPolicy), func(t *testing.T) {
				c, kube, d, policy := databaseNetworkMaintenanceFixture(t)
				ns, err := kube.CoreV1().Namespaces().Get(context.Background(), policy.Namespace, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				switch state {
				case "missing":
					err = kube.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("namespaces"), "", ns.Name)
				case "foreign":
					ns.Labels[databaseOwner] = strings.Repeat("b", 32)
				case "unmanaged":
					delete(ns.Labels, managedBy)
				case "terminating":
					stamp := metav1.NewTime(time.Now())
					ns.DeletionTimestamp = &stamp
				}
				if state != "missing" {
					err = kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, "")
				}
				if err != nil {
					t.Fatal(err)
				}
				if !existingPolicy {
					if err = kube.Tracker().Delete(networkingv1.SchemeGroupVersion.WithResource("networkpolicies"), policy.Namespace, policy.Name); err != nil {
						t.Fatal(err)
					}
				}
				kube.ClearActions()
				if err = c.ReconcileDatabaseNetworkPolicy(context.Background(), d, func() error { t.Error("unowned namespace reached mutation authorization"); return nil }); err == nil {
					t.Fatal("namespace ownership or termination failure was hidden")
				}
				if len(databasePolicyWrites(kube)) != 0 {
					t.Fatal("maintenance created or updated policy in an unowned namespace")
				}
				if existingPolicy {
					current, err := kube.NetworkingV1().NetworkPolicies(policy.Namespace).Get(context.Background(), policy.Name, metav1.GetOptions{})
					if err != nil || !reflect.DeepEqual(current, policy) {
						t.Fatal("namespace rejection changed the existing policy", err)
					}
				}
			})
		}
	}
}

func databasePolicyWrites(kube *fake.Clientset) []ktesting.Action {
	var writes []ktesting.Action
	for _, action := range kube.Actions() {
		switch action.GetVerb() {
		case "create", "update", "patch", "delete", "delete-collection":
			writes = append(writes, action)
		}
	}
	return writes
}

func TestDatabaseNetworkMaintenanceRefreshesOrdinaryPostgresAPIEgress(t *testing.T) {
	c, kube, d, previous := databaseNetworkMaintenanceFixture(t)
	if d.Recovery != nil {
		t.Fatal("regression requires an ordinary database without recovery state")
	}
	checks := 0
	if err := c.ReconcileDatabaseNetworkPolicy(context.Background(), d, func() error { checks++; return nil }); err != nil {
		t.Fatal(err)
	}
	writes := databasePolicyWrites(kube)
	if checks != 1 || len(writes) != 1 || !writes[0].Matches("update", "networkpolicies") {
		t.Fatal("maintenance must authorize exactly one owned policy update")
	}
	current, err := kube.NetworkingV1().NetworkPolicies(previous.Namespace).Get(context.Background(), previous.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	expected := previous.DeepCopy()
	replaced, serviceRetained := false, false
	for i := range expected.Spec.Egress {
		for j := range expected.Spec.Egress[i].To {
			peer := &expected.Spec.Egress[i].To[j]
			if peer.IPBlock == nil {
				continue
			}
			if peer.IPBlock.CIDR == "10.43.0.1/32" {
				serviceRetained = true
			}
			if peer.IPBlock.CIDR == "192.0.2.10/32" {
				peer.IPBlock.CIDR = "192.0.2.20/32"
				replaced = true
			}
		}
	}
	if !replaced || !serviceRetained || !reflect.DeepEqual(current, expected) {
		t.Fatal("API endpoint refresh changed object identity, non-API rules, ports, ingress or metadata")
	}
	kube.ClearActions()
	if err = c.ReconcileDatabaseNetworkPolicy(context.Background(), d, func() error { t.Error("unchanged policy requested mutation authority"); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(databasePolicyWrites(kube)) != 0 {
		t.Fatal("unchanged discovery rewrote the policy")
	}
}

func TestDatabaseNetworkMaintenanceFailuresPreservePolicy(t *testing.T) {
	lostLease := errors.New("maintenance lease lost")
	for _, name := range []string{"lease lost", "different owner", "unmanaged policy", "discovery failed", "discovery truncated", "not lifecycle ready"} {
		t.Run(name, func(t *testing.T) {
			c, kube, d, expected := databaseNetworkMaintenanceFixture(t)
			before := func() error { return nil }
			switch name {
			case "lease lost":
				before = func() error { return lostLease }
			case "different owner":
				expected.Labels[databaseOwner] = strings.Repeat("b", 32)
			case "unmanaged policy":
				delete(expected.Labels, managedBy)
			case "discovery failed":
				kube.PrependReactor("list", "endpointslices", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("endpoint discovery unavailable")
				})
			case "discovery truncated":
				kube.PrependReactor("list", "endpointslices", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &discoveryv1.EndpointSliceList{ListMeta: metav1.ListMeta{Continue: "more"}}, nil
				})
			case "not lifecycle ready":
				d.Status = "restoring"
			}
			if err := kube.Tracker().Update(networkingv1.SchemeGroupVersion.WithResource("networkpolicies"), expected, expected.Namespace); err != nil {
				t.Fatal(err)
			}
			err := c.ReconcileDatabaseNetworkPolicy(context.Background(), d, before)
			if name == "not lifecycle ready" {
				if err != nil || len(kube.Actions()) != 0 {
					t.Fatal("maintenance touched an active lifecycle", err)
				}
			} else if err == nil {
				t.Fatal("maintenance failure was hidden")
			}
			if name == "lease lost" && !errors.Is(err, lostLease) {
				t.Fatal("mutation did not retain its lease error", err)
			}
			if len(databasePolicyWrites(kube)) != 0 {
				t.Fatal("failed maintenance mutated Kubernetes")
			}
			current, err := kube.NetworkingV1().NetworkPolicies(expected.Namespace).Get(context.Background(), expected.Name, metav1.GetOptions{})
			if err != nil || !reflect.DeepEqual(current, expected) {
				t.Fatal("failed maintenance changed or broadened the prior policy", err)
			}
		})
	}
}

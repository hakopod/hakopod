package cluster

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// These synthetic fixtures exercise admission checks, not live cluster behavior.
func clickhousePublicTestService(d database.Resource) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: DatabaseNamespace(d.ID), UID: "service", OwnerReferences: []metav1.OwnerReference{{APIVersion: "clickhouse.altinity.com/v1", Kind: "ClickHouseInstallation", Name: "database", UID: "owned-controller", Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}}, Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.50",
		Selector: map[string]string{"clickhouse.altinity.com/namespace": DatabaseNamespace(d.ID), "clickhouse.altinity.com/app": "chop", "clickhouse.altinity.com/chi": "database", "clickhouse.altinity.com/ready": "yes"},
		Ports:    []corev1.ServicePort{{Name: "tcp-secure", Protocol: corev1.ProtocolTCP, Port: 9440, TargetPort: intstr.FromInt(9440)}, {Name: "https", Protocol: corev1.ProtocolTCP, Port: 8443, TargetPort: intstr.FromInt(8443)}},
	}}
}

func TestClickHousePublicServiceRejectsChangedOwnerSelectorAndPorts(t *testing.T) {
	d := clickhouseFixture()
	for _, purpose := range []string{"native", "https"} {
		t.Run(purpose, func(t *testing.T) {
			route, err := database.PublicEndpointRouteFor(d.Spec, purpose)
			if err != nil {
				t.Fatal(err)
			}
			valid := clickhousePublicTestService(d)
			if !clickhousePublicServiceOwned(valid, d, "owned-controller", route) {
				t.Fatal("owned ClickHouse service rejected")
			}
			if _, err := databasePublicEndpointBackendServiceAddress(valid, d, route); err != nil {
				t.Fatal(err)
			}
			for name, change := range map[string]func(*corev1.Service){
				"owner":              func(s *corev1.Service) { s.OwnerReferences[0].UID = "other" },
				"owner_kind":         func(s *corev1.Service) { s.OwnerReferences[0].Kind = "StatefulSet" },
				"owner_api":          func(s *corev1.Service) { s.OwnerReferences[0].APIVersion = "apps/v1" },
				"owner_name":         func(s *corev1.Service) { s.OwnerReferences[0].Name = "foreign" },
				"missing_owner":      func(s *corev1.Service) { s.OwnerReferences = nil },
				"extra_owner":        func(s *corev1.Service) { s.OwnerReferences = append(s.OwnerReferences, s.OwnerReferences[0]) },
				"not_controller":     func(s *corev1.Service) { s.OwnerReferences[0].Controller = ptr(false) },
				"missing_controller": func(s *corev1.Service) { s.OwnerReferences[0].Controller = nil },
				"unblocked_owner":    func(s *corev1.Service) { s.OwnerReferences[0].BlockOwnerDeletion = ptr(false) },
				"empty_uid":          func(s *corev1.Service) { s.UID = "" },
				"unready_addresses":  func(s *corev1.Service) { s.Spec.PublishNotReadyAddresses = true },
				"missing_selector":   func(s *corev1.Service) { s.Spec.Selector = nil },
				"name":               func(s *corev1.Service) { s.Name = "foreign" },
				"port": func(s *corev1.Service) {
					for i := range s.Spec.Ports {
						s.Spec.Ports[i].Port = 9000
					}
				},
				"namespace":      func(s *corev1.Service) { s.Namespace = "other" },
				"selector":       func(s *corev1.Service) { s.Spec.Selector["clickhouse.altinity.com/ready"] = "no" },
				"extra_selector": func(s *corev1.Service) { s.Spec.Selector["unreviewed"] = "true" },
				"target": func(s *corev1.Service) {
					for i := range s.Spec.Ports {
						s.Spec.Ports[i].TargetPort = intstr.FromInt(3306)
					}
				},
				"named_target": func(s *corev1.Service) {
					for i := range s.Spec.Ports {
						s.Spec.Ports[i].TargetPort = intstr.FromString("tcp-secure")
					}
				},
				"port_name": func(s *corev1.Service) {
					for i := range s.Spec.Ports {
						s.Spec.Ports[i].Name = "wrong"
					}
				},
				"protocol": func(s *corev1.Service) {
					for i := range s.Spec.Ports {
						s.Spec.Ports[i].Protocol = corev1.ProtocolUDP
					}
				},
				"deleting": func(s *corev1.Service) { now := metav1.Now(); s.DeletionTimestamp = &now },
			} {
				t.Run(name, func(t *testing.T) {
					s := valid.DeepCopy()
					change(s)
					if clickhousePublicServiceOwned(s, d, "owned-controller", route) {
						t.Fatal("changed service accepted")
					}
				})
			}
			for name, change := range map[string]func(*corev1.Service){
				"external": func(s *corev1.Service) {
					s.Spec.Type = corev1.ServiceTypeExternalName
					s.Spec.ExternalName = "outside.invalid"
				},
				"external_ip": func(s *corev1.Service) { s.Spec.ExternalIPs = []string{"192.0.2.1"} },
				"ipv6":        func(s *corev1.Service) { s.Spec.ClusterIP = "2001:db8::1" },
				"mapped_ipv4": func(s *corev1.Service) { s.Spec.ClusterIP = "::ffff:10.43.0.50" },
				"loopback":    func(s *corev1.Service) { s.Spec.ClusterIP = "127.0.0.1" },
				"unspecified": func(s *corev1.Service) { s.Spec.ClusterIP = "0.0.0.0" },
				"multicast":   func(s *corev1.Service) { s.Spec.ClusterIP = "224.0.0.1" },
				"headless":    func(s *corev1.Service) { s.Spec.ClusterIP = corev1.ClusterIPNone },
			} {
				t.Run(name, func(t *testing.T) {
					s := valid.DeepCopy()
					change(s)
					if _, err := databasePublicEndpointBackendServiceAddress(s, d, route); err == nil {
						t.Fatal("external or headless service accepted")
					}
				})
			}
		})
	}
}

func clickhousePublicTargetsFixture(t *testing.T) (*Client, database.Resource, *kubefake.Clientset) {
	t.Helper()
	d, object, spec := clickhouseMigrationObject(t, false)
	d.Status = "ready"
	spec.NodeName = "worker"
	labels := clickhousePublicServiceSelector(d)
	labels[databaseOwner] = d.ID
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "data-0", Namespace: DatabaseNamespace(d.ID), UID: "pod", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "clickhouse.altinity.com/v1", Kind: "ClickHouseInstallation", Name: "database", UID: object.GetUID(), Controller: ptr(true)}}}, Spec: spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.42.0.10", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	d.Observation = database.Observation{Status: "ready", Revision: d.Revision, ObservedAt: time.Now(), Members: []database.Member{{Name: pod.Name, UID: string(pod.UID), Node: spec.NodeName, Ready: true}}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pod.Namespace, UID: "namespace", Labels: databaseLabels(d)}}
	kube := kubefake.NewClientset(ns, pod, clickhousePublicTestService(d))
	return &Client{kube: kube, dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)}, d, kube
}

func TestClickHousePublicTargetsRevalidateCurrentPods(t *testing.T) {
	ctx := context.Background()
	c, d, _ := clickhousePublicTargetsFixture(t)
	route, err := database.PublicEndpointRouteFor(d.Spec, "native")
	if err != nil {
		t.Fatal(err)
	}
	if targets, err := c.clickhousePublicEndpointTargets(ctx, d, route); err != nil || !reflect.DeepEqual(targets, []string{"10.43.0.50", "10.42.0.10"}) {
		t.Fatal("valid synthetic targets rejected", targets, err)
	}
	for name, change := range map[string]func(*corev1.Pod){
		"unready_same_uid":       func(p *corev1.Pod) { p.Status.Conditions[0].Status = corev1.ConditionFalse },
		"missing_readiness":      func(p *corev1.Pod) { p.Status.Conditions = nil },
		"replacement_uid":        func(p *corev1.Pod) { p.UID = "replacement" },
		"foreign_owner_same_uid": func(p *corev1.Pod) { p.OwnerReferences[0].UID = "foreign" },
		"missing_owner_same_uid": func(p *corev1.Pod) { p.OwnerReferences = nil },
		"image_same_uid":         func(p *corev1.Pod) { p.Spec.Containers[0].Image = "clickhouse:latest" },
		"projection_same_uid":    func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPath = "stale" },
		"changed_node":           func(p *corev1.Pod) { p.Spec.NodeName = "foreign" },
		"foreign_label":          func(p *corev1.Pod) { p.Labels[databaseOwner] = "foreign" },
		"selector_drift":         func(p *corev1.Pod) { p.Labels["clickhouse.altinity.com/ready"] = "no" },
		"deleting":               func(p *corev1.Pod) { now := metav1.Now(); p.DeletionTimestamp = &now },
		"pending":                func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending },
		"ipv6":                   func(p *corev1.Pod) { p.Status.PodIP = "2001:db8::1" },
		"loopback":               func(p *corev1.Pod) { p.Status.PodIP = "127.0.0.1" },
		"runtime_drift":          func(p *corev1.Pod) { p.Spec.RuntimeClassName = ptr("foreign") },
	} {
		t.Run(name, func(t *testing.T) {
			c, d, kube := clickhousePublicTargetsFixture(t)
			pod, err := kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, "data-0", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			change(pod)
			if _, err = kube.CoreV1().Pods(pod.Namespace).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.clickhousePublicEndpointTargets(ctx, d, route); err == nil {
				t.Fatal("stale observation admitted changed live pod")
			}
		})
	}
	t.Run("extra_selected_pod", func(t *testing.T) {
		c, d, kube := clickhousePublicTargetsFixture(t)
		pod, err := kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, "data-0", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pod.Name, pod.UID, pod.ResourceVersion = "extra-data", "extra-pod", ""
		pod.Status.PodIP = "10.42.0.11"
		if _, err = kube.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err = c.clickhousePublicEndpointTargets(ctx, d, route); err == nil {
			t.Fatal("unobserved selected pod admitted")
		}
	})
	for _, kind := range []string{"stale", "revision", "unready", "empty", "duplicate", "member-unready", "member-uid", "member-node"} {
		t.Run(kind, func(t *testing.T) {
			c, d, _ := clickhousePublicTargetsFixture(t)
			switch kind {
			case "stale":
				d.Observation.ObservedAt = time.Now().Add(-time.Hour)
			case "revision":
				d.Observation.Revision++
			case "unready":
				d.Observation.Status = "pending"
			case "empty":
				d.Observation.Members = nil
			case "duplicate":
				d.Observation.Members = append(d.Observation.Members, d.Observation.Members[0])
			case "member-unready":
				d.Observation.Members[0].Ready = false
			case "member-uid":
				d.Observation.Members[0].UID = "foreign"
			case "member-node":
				d.Observation.Members[0].Node = "foreign"
			}
			if _, err := c.clickhousePublicEndpointTargets(ctx, d, route); err == nil {
				t.Fatal("invalid observed inventory admitted")
			}
		})
	}
}

func TestClickHousePublicIdentityRefusesUnmigratedDatabase(t *testing.T) {
	d, object, _ := clickhouseMigrationObject(t, true)
	d.PublicEndpointNames = []string{"public.database.example.test"}
	c, kube, dynamic := clickhouseMigrationClient(d, object)
	if err := c.reconcileClickHousePublicNames(context.Background(), d, func() error { t.Fatal("publication must not migrate database"); return nil }); err == nil {
		t.Fatal("unmigrated identity accepted")
	}
	clickhouseMigrationAssertNoWrites(t, kube.Actions())
	clickhouseMigrationAssertNoWrites(t, dynamic.Actions())
}
func TestClickHousePublicRuntimeUsesExactServiceRoute(t *testing.T) {
	d := clickhouseFixture()
	for _, tc := range []struct {
		purpose, suffix string
		port            int
	}{{"native", "tcp-secure", 9440}, {"https", "https", 8443}} {
		t.Run(tc.purpose, func(t *testing.T) {
			endpoint := database.PublicEndpoint{ID: strings.Repeat("b", 32), Spec: database.PublicEndpointSpec{Purpose: tc.purpose, SourceCIDRs: []string{"192.0.2.1/32"}, MaxConnections: 8}, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database.public.example.test", Address: "192.0.2.10", Port: 15432}}
			models, err := databasePublicEndpointModels(d, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			service := models[0].(map[string]any)["service"].(map[string]any)
			if service["name"] != "database" || service["port"] != int64(tc.port) {
				t.Fatal("wrong ClickHouse route", service)
			}
			frontend := databasePublicEndpointPrefix(d, endpoint)
			config := fmt.Sprintf("frontend %s\n mode tcp\n maxconn 8\n timeout client 300000\n bind 0.0.0.0:15432 name v4\n acl allowed_source src 192.0.2.1/32\n tcp-request connection reject unless allowed_source\n default_backend %s_svc_database_%s\n", frontend, DatabaseNamespace(d.ID), tc.suffix)
			state := publicTCPRuntime{configuration: config, active: []string{frontend}}
			if err := validateDatabasePublicTCPRuntime(d, endpoint, models, state); err != nil {
				t.Fatal("valid ClickHouse suffix rejected", err)
			}
			for _, wrong := range []string{"postgresql", "tcp", "http", map[string]string{"native": "https", "https": "tcp-secure"}[tc.purpose]} {
				state.configuration = strings.Replace(config, "_svc_database_"+tc.suffix, "_svc_database_"+wrong, 1)
				if err := validateDatabasePublicTCPRuntime(d, endpoint, models, state); err == nil {
					t.Fatal("unreviewed HAProxy backend accepted", wrong)
				}
			}
		})
	}
}

func TestClickHousePublicIngressOnlyOwnedProxyAndClosedDuringRecovery(t *testing.T) {
	stamp := time.Now().UTC()
	for _, tc := range []struct {
		name, status string
		access       bool
		recovery     *database.Recovery
		want         bool
	}{
		{"active", "ready", true, nil, true}, {"disabled", "ready", false, nil, false}, {"restoring", "restoring", true, &database.Recovery{}, false},
		{"uninspected", "ready", true, &database.Recovery{RestoredAt: &stamp}, false}, {"inspected", "ready", true, &database.Recovery{RestoredAt: &stamp, InspectedAt: &stamp}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := clickhouseFixture()
			d.Status = tc.status
			d.PublicEndpointAccess = tc.access
			d.Recovery = tc.recovery
			c := &Client{kube: kubefake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIPs: []string{"10.43.0.1"}}}), options: Options{ProxyNamespace: "owned-proxy", ProxyRelease: "owned-release"}}
			if err := c.databaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			policy, err := c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, rule := range policy.Spec.Ingress {
				for _, peer := range rule.From {
					if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "owned-proxy" {
						continue
					}
					count++
					if peer.PodSelector == nil || !reflect.DeepEqual(peer.PodSelector.MatchLabels, map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": "owned-release"}) {
						t.Fatal("public ingress is not restricted to owned proxy pods")
					}
					if len(rule.Ports) != 2 {
						t.Fatal("public ingress includes nonclient ports")
					}
					ports := map[int32]bool{}
					for _, port := range rule.Ports {
						if port.Port == nil || port.Port.Type != intstr.Int || port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP {
							t.Fatal("invalid public ingress port")
						}
						ports[port.Port.IntVal] = true
					}
					if !ports[9440] || !ports[8443] {
						t.Fatal("public ingress exposes wrong ports")
					}
				}
			}
			if (count == 1) != tc.want || count > 1 {
				t.Fatal("public ingress disagrees with access/recovery gate", count)
			}
		})
	}
}

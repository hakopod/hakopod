package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// These are explicitly synthetic ownership fixtures, not cluster evidence.
func mysqlPublicTestService(d database.Resource) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: DatabaseNamespace(d.ID), UID: "service", OwnerReferences: []metav1.OwnerReference{{APIVersion: "mysql.oracle.com/v2", Kind: "InnoDBCluster", Name: "database", UID: "controller", Controller: ptr(true)}}}, Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.50",
		Selector: map[string]string{"component": "mysqlrouter", "tier": "mysql", "mysql.oracle.com/cluster": "database"},
		Ports:    []corev1.ServicePort{{Name: "mysql-alternate", Protocol: corev1.ProtocolTCP, Port: 6446, TargetPort: intstr.FromInt(6446)}, {Name: "mysql-ro", Protocol: corev1.ProtocolTCP, Port: 6447, TargetPort: intstr.FromInt(6447)}},
	}}
}

func TestMySQLPublicServiceRejectsChangedOwnerSelectorAndPorts(t *testing.T) {
	d := mysqlUnitFixture()
	for _, purpose := range []string{"read_write", "read_only"} {
		t.Run(purpose, func(t *testing.T) {
			route, err := database.PublicEndpointRouteFor(d.Spec, purpose)
			if err != nil {
				t.Fatal(err)
			}
			valid := mysqlPublicTestService(d)
			if !mysqlPublicRouterServiceOwned(valid, d, "controller", route) {
				t.Fatal("owned Router service rejected")
			}
			if _, err := databasePublicEndpointBackendServiceAddress(valid, d, route); err != nil {
				t.Fatal(err)
			}
			for name, change := range map[string]func(*corev1.Service){
				"owner":          func(s *corev1.Service) { s.OwnerReferences[0].UID = "other" },
				"namespace":      func(s *corev1.Service) { s.Namespace = "other" },
				"selector":       func(s *corev1.Service) { s.Spec.Selector["component"] = "mysqld" },
				"extra_selector": func(s *corev1.Service) { s.Spec.Selector["unreviewed"] = "true" },
				"target": func(s *corev1.Service) {
					for i := range s.Spec.Ports {
						s.Spec.Ports[i].TargetPort = intstr.FromInt(3306)
					}
				},
				"named_target": func(s *corev1.Service) {
					for i := range s.Spec.Ports {
						s.Spec.Ports[i].TargetPort = intstr.FromString("mysql")
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
					if mysqlPublicRouterServiceOwned(s, d, "controller", route) {
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

func mysqlPublicTargetsFixture(t *testing.T) (*Client, database.Resource, *kubefake.Clientset) {
	t.Helper()
	d := mysqlUnitFixture()
	d.Status = "ready"
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	object.SetUID("controller")
	object.SetAnnotations(map[string]string{"hakopod.io/database-revision": "1"})
	ns := DatabaseNamespace(d.ID)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "database-router", Namespace: ns, UID: "deployment", OwnerReferences: []metav1.OwnerReference{{APIVersion: "mysql.oracle.com/v2", Kind: "InnoDBCluster", Name: "database", UID: "controller", Controller: ptr(true)}}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "router-rs", Namespace: ns, UID: "replicaset", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID, Controller: ptr(true)}}}}
	objects := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, UID: "namespace", Labels: databaseLabels(d)}}, dep, rs, mysqlPublicTestService(d)}
	d.Observation = database.Observation{Status: "ready", Revision: d.Revision, ObservedAt: time.Now().UTC(), Routing: &database.RoutingObservation{Kind: "mysql-router", Ready: true}}
	for i := 0; i < d.Spec.RouterInstances(); i++ {
		name := fmt.Sprintf("router-%d", i)
		uid := types.UID(name + "-uid")
		node := fmt.Sprintf("node-%d", i)
		labels := map[string]string{databaseOwner: d.ID, databaseRouterLabel: "true", "component": "mysqlrouter", "tier": "mysql", "mysql.oracle.com/cluster": "database"}
		resources := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(database.MySQLRouterCPU), corev1.ResourceMemory: resource.MustParse(database.MySQLRouterMemory)}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid, Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr(true)}}}, Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "router", Image: mysqlRouterImage, Resources: corev1.ResourceRequirements{Requests: resources, Limits: resources.DeepCopy()}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: fmt.Sprintf("10.42.0.%d", 10+i), Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
		objects = append(objects, pod, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node}})
		d.Observation.Routing.Members = append(d.Observation.Routing.Members, database.Member{Name: name, UID: string(uid), Ready: true, Role: "router", Node: node, Image: mysqlRouterImage})
	}
	kube := kubefake.NewClientset(objects...)
	return &Client{kube: kube, dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)}, d, kube
}

func TestMySQLPublicTargetsRevalidateLiveRouterIdentity(t *testing.T) {
	ctx := context.Background()
	c, d, _ := mysqlPublicTargetsFixture(t)
	route, _ := database.PublicEndpointRouteFor(d.Spec, "read_write")
	if targets, err := c.mysqlPublicEndpointTargets(ctx, d, route); err != nil || len(targets) != 1+d.Spec.RouterInstances() {
		t.Fatal("valid targets rejected", err)
	}
	for name, change := range map[string]func(*corev1.Pod){
		"unready_same_uid":        func(p *corev1.Pod) { p.Status.Conditions[0].Status = corev1.ConditionFalse },
		"missing_readiness":       func(p *corev1.Pod) { p.Status.Conditions = nil },
		"replacement_uid":         func(p *corev1.Pod) { p.UID = "replacement" },
		"unpinned_image_same_uid": func(p *corev1.Pod) { p.Spec.Containers[0].Image = "mysql-router:latest" },
		"foreign_owner_same_uid":  func(p *corev1.Pod) { p.OwnerReferences[0].UID = "foreign" },
		"removed_owner_same_uid":  func(p *corev1.Pod) { p.OwnerReferences = nil },
		"extra_container": func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "unreviewed"})
		},
		"lost_database_label": func(p *corev1.Pod) { delete(p.Labels, databaseOwner) },
		"resource_limit_drift": func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("2Gi")
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, d, kube := mysqlPublicTargetsFixture(t)
			pod, err := kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, "router-0", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			change(pod)
			if _, err = kube.CoreV1().Pods(pod.Namespace).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.mysqlPublicEndpointTargets(ctx, d, route); err == nil {
				t.Fatal("changed live Router accepted from stale ready observation")
			}
		})
	}
	t.Run("stale_observation", func(t *testing.T) {
		c, d, _ := mysqlPublicTargetsFixture(t)
		d.Observation.ObservedAt = time.Now().Add(-time.Hour)
		if _, err := c.mysqlPublicEndpointTargets(ctx, d, route); err == nil {
			t.Fatal("stale Router observation accepted")
		}
	})
	t.Run("deleting_replicaset", func(t *testing.T) {
		c, d, kube := mysqlPublicTargetsFixture(t)
		rs, err := kube.AppsV1().ReplicaSets(DatabaseNamespace(d.ID)).Get(ctx, "router-rs", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		now := metav1.Now()
		rs.DeletionTimestamp = &now
		if _, err = kube.AppsV1().ReplicaSets(rs.Namespace).Update(ctx, rs, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.mysqlPublicEndpointTargets(ctx, d, route); err == nil {
			t.Fatal("deleting Router owner accepted")
		}
	})
	t.Run("changed_parent_chain", func(t *testing.T) {
		c, d, kube := mysqlPublicTargetsFixture(t)
		dep, err := kube.AppsV1().Deployments(DatabaseNamespace(d.ID)).Get(ctx, "database-router", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		dep.OwnerReferences[0].UID = "foreign"
		if _, err = kube.AppsV1().Deployments(dep.Namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err = c.mysqlPublicEndpointTargets(ctx, d, route); err == nil {
			t.Fatal("foreign Router deployment accepted")
		}
	})
}

func TestMySQLPublicRuntimeUsesExactRouterRoute(t *testing.T) {
	d := mysqlUnitFixture()
	for _, tc := range []struct {
		purpose, suffix string
		port            int
	}{{"read_write", "mysql-alternate", 6446}, {"read_only", "mysql-ro", 6447}} {
		t.Run(tc.purpose, func(t *testing.T) {
			endpoint := database.PublicEndpoint{ID: strings.Repeat("b", 32), Spec: database.PublicEndpointSpec{Purpose: tc.purpose, SourceCIDRs: []string{"192.0.2.1/32"}, MaxConnections: 8}, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database.public.example.test", Address: "192.0.2.10", Port: 15432}}
			models, err := databasePublicEndpointModels(d, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			service := models[0].(map[string]any)["service"].(map[string]any)
			if service["name"] != "database" || service["port"] != int64(tc.port) {
				t.Fatal("wrong Router route", service)
			}
			frontend := databasePublicEndpointPrefix(d, endpoint)
			config := fmt.Sprintf("frontend %s\n mode tcp\n maxconn 8\n timeout client 300000\n bind 0.0.0.0:15432 name v4\n acl allowed_source src 192.0.2.1/32\n tcp-request connection reject unless allowed_source\n default_backend %s_svc_database_%s\n", frontend, DatabaseNamespace(d.ID), tc.suffix)
			state := publicTCPRuntime{configuration: config, active: []string{frontend}}
			if err := validateDatabasePublicTCPRuntime(d, endpoint, models, state); err != nil {
				t.Fatal("valid Router suffix rejected", err)
			}
			for _, wrong := range []string{"postgresql", "mysql", "mysql-rw-split"} {
				state.configuration = strings.Replace(config, "_svc_database_"+tc.suffix, "_svc_database_"+wrong, 1)
				if err := validateDatabasePublicTCPRuntime(d, endpoint, models, state); err == nil {
					t.Fatal("unreviewed HAProxy backend accepted", wrong)
				}
			}
		})
	}
}

func TestMySQLPublicSANChangesRetainIssuerAndHonorLease(t *testing.T) {
	d := mysqlUnitFixture()
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace", Labels: databaseLabels(d)}}
	c := &Client{kube: kubefake.NewClientset(ns)}
	allow := func() error { return nil }
	if err := c.prepareDatabaseIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	root, err := api.Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	original, err := api.Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, ca, err := database.ParsePublicTrust(root.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	host := "database-15432.database.example.test"
	d.PublicEndpointNames = []string{host}
	lost := errors.New("lease lost")
	if err := c.prepareDatabaseIdentity(ctx, d, func() error { return lost }); !errors.Is(err, lost) {
		t.Fatal("lease loss did not block SAN issuance", err)
	}
	unchanged, _ := api.Get(ctx, "database-tls", metav1.GetOptions{})
	if !reflect.DeepEqual(original.Data, unchanged.Data) {
		t.Fatal("lease refusal changed leaf")
	}
	if err := c.prepareDatabaseIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	added, _ := api.Get(ctx, "database-tls", metav1.GetOptions{})
	if _, err := database.VerifyServerCertificate(added.Data["tls.crt"], ca, []string{host, "database." + ns.Name + ".svc"}, time.Now()); err != nil {
		t.Fatal("public and internal names not jointly valid", err)
	}
	if bytes.Equal(added.Data["tls.crt"], original.Data["tls.crt"]) {
		t.Fatal("SAN addition did not replace leaf")
	}
	d.PublicEndpointNames = nil
	if err := c.prepareDatabaseIdentity(ctx, d, allow); err != nil {
		t.Fatal(err)
	}
	removed, _ := api.Get(ctx, "database-tls", metav1.GetOptions{})
	afterRoot, _ := api.Get(ctx, "database-ca", metav1.GetOptions{})
	if !reflect.DeepEqual(root.Data, afterRoot.Data) {
		t.Fatal("public SAN changes replaced issuer")
	}
	if _, err := database.VerifyServerCertificate(removed.Data["tls.crt"], ca, []string{host}, time.Now()); err == nil {
		t.Fatal("revoked public SAN retained")
	}
	if _, err := database.VerifyServerCertificate(removed.Data["tls.crt"], ca, []string{"database." + ns.Name + ".svc"}, time.Now()); err != nil {
		t.Fatal("removal broke internal identity", err)
	}
}

func TestMySQLPublicIngressOnlyOwnedProxyAndClosedDuringRecovery(t *testing.T) {
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
			d := mysqlUnitFixture()
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
					if !ports[6446] || !ports[6447] {
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

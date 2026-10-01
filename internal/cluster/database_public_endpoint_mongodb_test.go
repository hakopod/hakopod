package cluster

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func mongodbPublicEndpointFixture() (database.Resource, database.PublicEndpoint) {
	d := mongodbUnitFixture()
	d.Observation.Members = []database.Member{
		{Name: "database-0", UID: "uid-0", Ready: true},
		{Name: "database-1", UID: "uid-1", Ready: true},
		{Name: "database-2", UID: "uid-2", Ready: true},
	}
	endpoint := database.PublicEndpoint{ID: strings.Repeat("e", 32), DatabaseID: d.ID, Revision: 7, Spec: database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 12}}
	for i, member := range d.Observation.Members {
		allocation := database.PublicEndpointAllocation{ID: "allocation-" + member.Name, Host: member.Name + ".public.example.test", Address: "203.0.113.10", Port: int32(27017 + i)}
		endpoint.MemberAllocations = append(endpoint.MemberAllocations, database.PublicEndpointMemberAllocation{MemberName: member.Name, MemberUID: member.UID, Allocation: allocation})
	}
	endpoint.Allocation = endpoint.MemberAllocations[0].Allocation
	return d, endpoint
}

func TestMongoDBPublicEndpointHorizonsBindOrderedMemberIdentities(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	horizons, err := mongodbPublicEndpointHorizons(d, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	wanted := []any{
		map[string]any{mongodbPublicEndpointHorizon: net.JoinHostPort("database-0.public.example.test", "27017")},
		map[string]any{mongodbPublicEndpointHorizon: net.JoinHostPort("database-1.public.example.test", "27018")},
		map[string]any{mongodbPublicEndpointHorizon: net.JoinHostPort("database-2.public.example.test", "27019")},
	}
	if !reflect.DeepEqual(horizons, wanted) {
		t.Fatal("MongoDB horizons do not preserve the reviewed ordinal mapping", horizons)
	}
	endpoint.MemberAllocations[1].MemberUID = "replacement"
	if _, err = mongodbPublicEndpointHorizons(d, endpoint); err == nil {
		t.Fatal("replacement MongoDB member accepted without a new review")
	}
}

func TestMongoDBPublicEndpointModelsUseOneListenerAndServicePerMember(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	models, err := databasePublicEndpointModels(d, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != len(endpoint.MemberAllocations) {
		t.Fatal("member listener count differs from reviewed topology")
	}
	seen := map[string]bool{}
	for i, raw := range models {
		model := raw.(map[string]any)
		service := model["service"].(map[string]any)
		frontend := model["frontend"].(map[string]any)
		name := service["name"].(string)
		if name != mongodbPublicEndpointServiceName(endpoint, endpoint.MemberAllocations[i]) || seen[name] {
			t.Fatal("member backend is shared or unstable")
		}
		seen[name] = true
		bind := frontend["binds"].(map[string]any)["v4"].(map[string]any)
		if bind["port"] != int64(endpoint.MemberAllocations[i].Allocation.Port) {
			t.Fatal("member listener uses the wrong reviewed port")
		}
	}
}

func TestMongoDBIdentityIncludesReviewedPublicNames(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	for _, member := range endpoint.MemberAllocations {
		d.PublicEndpointNames = append(d.PublicEndpointNames, member.Allocation.Host)
	}
	names := databaseIdentityNames(d)
	for _, private := range []string{"database-svc", "*.database-svc." + DatabaseNamespace(d.ID) + ".svc.cluster.local"} {
		if !slicesContains(names, private) {
			t.Fatal("private MongoDB identity was lost while adding public names")
		}
	}
	for _, member := range endpoint.MemberAllocations {
		if !slicesContains(names, member.Allocation.Host) {
			t.Fatal("public member hostname missing from MongoDB leaf")
		}
	}
}

func TestMongoDBPublicEndpointRejectsStaleOrForeignRuntimeTargets(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	member := endpoint.MemberAllocations[0]
	service := &corev1.Service{ObjectMeta: databaseIdentityMeta(d, ns.UID, mongodbPublicEndpointServiceName(endpoint, member)), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.30", Selector: map[string]string{databaseOwner: d.ID, mongodbPublicEndpointMemberLabel: ownerID(member.Allocation.ID), "statefulset.kubernetes.io/pod-name": member.MemberName}, Ports: []corev1.ServicePort{{Name: "mongodb", Protocol: corev1.ProtocolTCP, Port: 27017}}}}
	service.Labels[databasePublicEndpointLabel] = member.Allocation.ID
	service.Annotations = map[string]string{"hakopod.io/database-member-name": member.MemberName, "hakopod.io/database-member-uid": member.MemberUID}
	ready := true
	port := int32(27017)
	portName := "mongodb"
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "member-0", Namespace: ns.Name, Labels: map[string]string{"kubernetes.io/service-name": service.Name}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.42.0.30"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: member.MemberName, UID: "replacement-uid"}}}}
	c := &Client{kube: kubefake.NewSimpleClientset(ns, service, slice)}
	if err := c.verifyMongoDBPublicEndpointBackend(context.Background(), d, endpoint); err == nil || !strings.Contains(err.Error(), "endpoint identity changed") {
		t.Fatal("stale member UID reached native discovery", err)
	}
	foreign := service.DeepCopy()
	foreign.Labels[databaseOwner] = "another-database"
	if mongodbPublicEndpointServiceOwned(foreign, d, endpoint, member, ns.UID) {
		t.Fatal("foreign member Service accepted")
	}
	foreign = service.DeepCopy()
	foreign.Spec.Selector[mongodbPublicEndpointMemberLabel] = "another-allocation"
	if reflect.DeepEqual(foreign.Spec.Selector, service.Spec.Selector) || mongodbPublicEndpointServiceOwned(foreign, d, endpoint, member, ns.UID) && foreign.Spec.Selector[mongodbPublicEndpointMemberLabel] != ownerID(member.Allocation.ID) {
		t.Fatal("foreign member selector accepted")
	}
}

func TestMongoDBPublicEndpointHorizonMaintenancePreservesExactHosts(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	horizons, err := mongodbPublicEndpointHorizons(d, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := mongodbPublicEndpointHorizonHosts(horizons)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := database.PublicEndpointAllocationNames(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hosts, wanted) {
		t.Fatal("maintenance would not preserve exact public discovery", hosts, wanted)
	}
	if cleared, err := mongodbPublicEndpointHorizonHosts(nil); err != nil || len(cleared) != 0 {
		t.Fatal("cleared horizons are not stable", cleared, err)
	}
	bad := append([]any(nil), horizons...)
	bad[0] = map[string]any{"foreign": "member.example.test:27017"}
	if _, err = mongodbPublicEndpointHorizonHosts(bad); err == nil {
		t.Fatal("foreign horizon key accepted")
	}
}

func TestMongoDBEndpointHelloRequiresExactReviewedTopology(t *testing.T) {
	expected := []string{"database-0.private.test:27017", "database-1.private.test:27017", "database-2.private.test:27017"}
	hello := mongodbEndpointHello{Hosts: []string{expected[2], expected[0], expected[1]}, Primary: expected[1], SetName: "database"}
	if err := verifyMongoDBEndpointHello(hello, expected); err != nil {
		t.Fatal(err)
	}
	hello.Hosts[0] = "database-2.public.example.test:27019"
	if err := verifyMongoDBEndpointHello(hello, expected); err == nil {
		t.Fatal("private convergence accepted a remaining public horizon")
	}
	hello.Hosts[0] = expected[2]
	hello.Primary = "foreign.example.test:27017"
	if err := verifyMongoDBEndpointHello(hello, expected); err == nil {
		t.Fatal("MongoDB discovery accepted an unreviewed primary")
	}
}

func mongodbPublicEndpointMemberService(d database.Resource, endpoint database.PublicEndpoint, ns *corev1.Namespace, member database.PublicEndpointMemberAllocation) *corev1.Service {
	service := &corev1.Service{ObjectMeta: databaseIdentityMeta(d, ns.UID, mongodbPublicEndpointServiceName(endpoint, member)), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.30", Selector: map[string]string{databaseOwner: d.ID, mongodbPublicEndpointMemberLabel: ownerID(member.Allocation.ID), "statefulset.kubernetes.io/pod-name": member.MemberName}, Ports: []corev1.ServicePort{{Name: "mongodb", Protocol: corev1.ProtocolTCP, Port: 27017}}}}
	service.UID = "service-uid"
	service.Labels[databasePublicEndpointLabel] = member.Allocation.ID
	service.Annotations = map[string]string{"hakopod.io/database-member-name": member.MemberName, "hakopod.io/database-member-uid": member.MemberUID}
	return service
}

func TestMongoDBPublicEndpointCleanupHandlesMissingReviewedPod(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	endpoint.MemberAllocations = endpoint.MemberAllocations[:1]
	endpoint.Allocation = endpoint.MemberAllocations[0].Allocation
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	service := mongodbPublicEndpointMemberService(d, endpoint, ns, endpoint.MemberAllocations[0])
	c := &Client{kube: kubefake.NewSimpleClientset(ns, service)}
	if err := mongodbPublicEndpointMemberServices(context.Background(), c, d, endpoint, false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := c.kube.CoreV1().Services(ns.Name).Get(context.Background(), service.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("member Service survived cleanup after its reviewed pod disappeared")
	}
}

func TestMongoDBPublicEndpointCleanupRetriesLabelAfterServiceDelete(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	endpoint.MemberAllocations = endpoint.MemberAllocations[:1]
	endpoint.Allocation = endpoint.MemberAllocations[0].Allocation
	member := endpoint.MemberAllocations[0]
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: member.MemberName, Namespace: ns.Name, UID: typesUID(member.MemberUID), Labels: map[string]string{mongodbPublicEndpointMemberLabel: ownerID(member.Allocation.ID)}}}
	service := mongodbPublicEndpointMemberService(d, endpoint, ns, member)
	c := &Client{kube: kubefake.NewSimpleClientset(ns, pod, service)}
	writes := 0
	interruptAfterServiceDelete := func() error {
		writes++
		if writes == 2 {
			return errors.New("interrupted before member label removal")
		}
		return nil
	}
	if err := mongodbPublicEndpointMemberServices(context.Background(), c, d, endpoint, false, interruptAfterServiceDelete); err == nil {
		t.Fatal("interrupted cleanup succeeded")
	}
	if _, err := c.kube.CoreV1().Services(ns.Name).Get(context.Background(), service.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("member Service was not deleted before interruption")
	}
	retained, err := c.kube.CoreV1().Pods(ns.Name).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil || retained.Labels[mongodbPublicEndpointMemberLabel] == "" {
		t.Fatal("fixture did not retain the member label after interruption")
	}
	if err = mongodbPublicEndpointMemberServices(context.Background(), c, d, endpoint, false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	cleaned, err := c.kube.CoreV1().Pods(ns.Name).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil || cleaned.Labels[mongodbPublicEndpointMemberLabel] != "" {
		t.Fatal("cleanup retry did not remove the member label after Service deletion")
	}
}

func TestDatabasePublicEndpointMemberReleaseRetriesAfterPartialDelete(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	proxy := "haproxy-controller"
	objects := []runtime.Object{}
	for _, member := range endpoint.MemberAllocations {
		claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: publicTCPClaimName(member.Allocation.Port), Namespace: proxy, UID: typesUID(member.Allocation.ID), Labels: map[string]string{managedBy: "hakopod", databaseOwner: d.ID, databasePublicEndpointClaimLabel: "true", databasePublicEndpointLabel: endpoint.ID}}, Data: map[string]string{"database_id": d.ID, "endpoint_id": endpoint.ID, "allocation_id": member.Allocation.ID, databasePublicEndpointClaimStateKey: databasePublicEndpointClosed, databasePublicEndpointClaimRevisionKey: "7", databasePublicEndpointClaimAckKey: publicTCPHash([]any{})}}
		objects = append(objects, claim)
	}
	kube := kubefake.NewSimpleClientset(objects...)
	failed := false
	second := publicTCPClaimName(endpoint.MemberAllocations[1].Allocation.Port)
	kube.Fake.PrependReactor("delete", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.DeleteAction).GetName() == second && !failed {
			failed = true
			return true, nil, errors.New("interrupted delete")
		}
		return false, nil, nil
	})
	c := &Client{kube: kube, options: Options{ProxyNamespace: proxy}}
	if err := c.ReleaseDatabasePublicEndpoint(context.Background(), d, endpoint, func() error { return nil }); err == nil {
		t.Fatal("interrupted member release succeeded")
	}
	if _, err := kube.CoreV1().ConfigMaps(proxy).Get(context.Background(), publicTCPClaimName(endpoint.MemberAllocations[0].Allocation.Port), metav1.GetOptions{}); err == nil {
		t.Fatal("first claim was not deleted before interruption")
	}
	if err := c.ReleaseDatabasePublicEndpoint(context.Background(), d, endpoint, func() error { return nil }); err != nil {
		t.Fatal("release retry did not accept the already missing member claim", err)
	}
	for _, member := range endpoint.MemberAllocations {
		if _, err := kube.CoreV1().ConfigMaps(proxy).Get(context.Background(), publicTCPClaimName(member.Allocation.Port), metav1.GetOptions{}); err == nil {
			t.Fatal("member claim survived release retry")
		}
	}
}

func TestDatabasePublicEndpointMemberReleaseValidatesAllProofsBeforeDelete(t *testing.T) {
	d, endpoint := mongodbPublicEndpointFixture()
	proxy := "haproxy-controller"
	objects := []runtime.Object{}
	for i, member := range endpoint.MemberAllocations {
		state := databasePublicEndpointClosed
		if i == 1 {
			state = databasePublicEndpointPublished
		}
		claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: publicTCPClaimName(member.Allocation.Port), Namespace: proxy, UID: typesUID(member.Allocation.ID), Labels: map[string]string{managedBy: "hakopod", databaseOwner: d.ID, databasePublicEndpointClaimLabel: "true", databasePublicEndpointLabel: endpoint.ID}}, Data: map[string]string{"database_id": d.ID, "endpoint_id": endpoint.ID, "allocation_id": member.Allocation.ID, databasePublicEndpointClaimStateKey: state, databasePublicEndpointClaimRevisionKey: "7", databasePublicEndpointClaimAckKey: publicTCPHash([]any{})}}
		objects = append(objects, claim)
	}
	kube := kubefake.NewSimpleClientset(objects...)
	c := &Client{kube: kube, options: Options{ProxyNamespace: proxy}}
	if err := c.ReleaseDatabasePublicEndpoint(context.Background(), d, endpoint, func() error { return nil }); err == nil {
		t.Fatal("release accepted an unclosed member claim")
	}
	for _, member := range endpoint.MemberAllocations {
		if _, err := kube.CoreV1().ConfigMaps(proxy).Get(context.Background(), publicTCPClaimName(member.Allocation.Port), metav1.GetOptions{}); err != nil {
			t.Fatal("release deleted a claim before validating the complete closure proof", err)
		}
	}
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

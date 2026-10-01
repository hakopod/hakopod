package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func redisPublicMapFixture(t *testing.T) (database.Resource, database.PublicEndpoint, map[string]corev1.Pod, string) {
	t.Helper()
	d := database.Resource{ID: strings.Repeat("a", 32), Revision: 3, Spec: database.Spec{Engine: "redis", Version: "8", Mode: "cluster", Shards: 3, Replicas: 1, TLS: &database.TLSConfig{Mode: "required"}}, Observation: database.Observation{Revision: 3, Status: "ready", ObservedAt: time.Now(), SlotsHealthy: true, SlotsAssigned: 16384}}
	e := database.PublicEndpoint{DatabaseID: d.ID, ID: strings.Repeat("e", 32), Spec: database.PublicEndpointSpec{Purpose: "cluster", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 16}}
	pods := make(map[string]corev1.Pod)
	rows := []string{}
	for i := 0; i < 6; i++ {
		role, ordinal := "leader", i
		if i >= 3 {
			role, ordinal = "follower", i-3
		}
		name := fmt.Sprintf("database-%s-%d", role, ordinal)
		uid := fmt.Sprintf("uid-%d", i)
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)}, Status: corev1.PodStatus{PodIP: fmt.Sprintf("10.42.0.%d", i+10)}}
		pods[name] = pod
		member := database.Member{Name: name, UID: uid, Ready: true}
		d.Observation.Members = append(d.Observation.Members, member)
		e.MemberAllocations = append(e.MemberAllocations, database.PublicEndpointMemberAllocation{MemberName: name, MemberUID: uid, Allocation: database.PublicEndpointAllocation{ID: fmt.Sprintf("allocation-%d", i), Host: fmt.Sprintf("member-%d.public.example.test", i), Address: "203.0.113.10", Port: int32(15432 + i)}})
		flag, primary, slot := "master", "-", []string{"0-5460", "5461-10922", "10923-16383"}[i%3]
		if i >= 3 {
			flag, primary, slot = "slave", fmt.Sprintf("%040x", i-2), ""
		}
		rows = append(rows, strings.TrimSpace(fmt.Sprintf("%040x %s:6379@16379,%s %s %s 0 0 1 connected %s", i+1, pod.Status.PodIP, redisMemberHostname(d, member), flag, primary, slot)))
	}
	// Allocations are reviewed in member-name order.
	e.MemberAllocations = append(e.MemberAllocations[3:6:6], e.MemberAllocations[:3]...)
	e.Allocation = e.MemberAllocations[0].Allocation
	raw := strings.Join(rows, "\n")
	_, fingerprint, err := database.ParseRedisTopology(raw, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	d.Observation.TopologyFingerprint = fingerprint
	return d, e, pods, raw
}

func TestRedisPublicAddressMapRejectsForeignAndStaleDiscovery(t *testing.T) {
	d, endpoint, pods, raw := redisPublicMapFixture(t)
	mapping, err := redisPublicAddressMap(d, endpoint, pods, raw)
	if err != nil || len(mapping) != 6 {
		t.Fatal("complete mapping rejected", err)
	}
	for _, member := range mapping {
		if len(member.AdvertisedAddresses) != 2 || member.MemberUID == "" || member.PublicPort < 15432 {
			t.Fatal("incomplete mapping")
		}
	}
	for _, candidate := range []string{
		strings.Replace(raw, ":6379@", ":6380@", 1),
		strings.Replace(raw, "@16379,", "@16380,", 1),
		strings.Replace(raw, "database-leader-0.database-leader-headless", "foreign.database-leader-headless", 1),
		strings.Replace(raw, "10.42.0.10", "10.42.0.90", 1),
	} {
		if _, err = redisPublicAddressMap(d, endpoint, pods, candidate); err == nil {
			t.Fatal("unreviewed discovery accepted")
		}
	}
	name := endpoint.MemberAllocations[0].MemberName
	pod := pods[name]
	pod.UID = "replacement"
	pods[name] = pod
	if _, err = redisPublicAddressMap(d, endpoint, pods, raw); err == nil {
		t.Fatal("replacement member reused reviewed map")
	}
}

func TestRedisPublicModelsUseDedicatedMemberBackends(t *testing.T) {
	d, endpoint, _, _ := redisPublicMapFixture(t)
	models, err := databasePublicEndpointModels(d, endpoint)
	if err != nil || len(models) != 6 {
		t.Fatal(err)
	}
	for i, model := range models {
		service := model.(map[string]any)["service"].(map[string]any)
		if service["name"] != redisPublicEndpointServiceName(endpoint, endpoint.MemberAllocations[i]) {
			t.Fatal("Redis member route uses shared backend")
		}
	}
	if err = database.PublicEndpointAvailability(d.Spec); err == nil {
		t.Fatal("unqualified Redis public route was enabled")
	}
	if route, routeErr := database.PublicEndpointRouteFor(d.Spec, "cluster"); routeErr != nil || route.Routing != "client_address_mapping" {
		t.Fatal("Redis route hides mapping requirement", routeErr)
	}
}

func TestRedisPublicServiceCleanupKeepsForeignAndLeaseFences(t *testing.T) {
	d, endpoint, _, _ := redisPublicMapFixture(t)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace", Labels: databaseLabels(d)}}
	member := endpoint.MemberAllocations[0]
	service := redisPublicMemberService(d, endpoint, member, ns.UID)
	service.UID, service.Spec.ClusterIP = "service", "10.43.0.17"
	kube := kubefake.NewClientset(ns, service)
	c := &Client{kube: kube}
	blocked := errors.New("lease expired")
	if err := c.reconcileRedisPublicMemberServices(context.Background(), d, endpoint, false, func() error { return blocked }); !errors.Is(err, blocked) {
		t.Fatal("cleanup skipped authority", err)
	}
	current, err := kube.CoreV1().Services(ns.Name).Get(context.Background(), service.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal("service removed after lease loss")
	}
	current.Annotations["hakopod.io/database-member-uid"] = "foreign"
	if _, err = kube.CoreV1().Services(ns.Name).Update(context.Background(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.reconcileRedisPublicMemberServices(context.Background(), d, endpoint, false, func() error { return nil }); err == nil {
		t.Fatal("foreign service deleted")
	}
}

func TestRedisPublicCertificateRetainsPrivateNames(t *testing.T) {
	d, endpoint, _, _ := redisPublicMapFixture(t)
	for _, member := range endpoint.MemberAllocations {
		d.PublicEndpointNames = append(d.PublicEndpointNames, member.Allocation.Host)
	}
	names := databaseIdentityNames(d)
	for _, host := range append(d.PublicEndpointNames, "*.database-leader-headless."+DatabaseNamespace(d.ID)+".svc") {
		if !slicesContains(names, host) {
			t.Fatal("Redis private or public certificate name lost")
		}
	}
}

func TestRedisPublicCertificateVerifierChecksLeafWithCertificateChain(t *testing.T) {
	leaf := &x509.Certificate{Raw: []byte("current verified leaf")}
	issuer := &x509.Certificate{Raw: []byte("verified issuer")}
	verify := redisPublicCertificateVerifier(fmt.Sprintf("%x", sha256.Sum256(leaf.Raw)))
	if err := verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, issuer}}); err != nil {
		t.Fatal("verified certificate chain rejected", err)
	}
	for _, chain := range [][]*x509.Certificate{nil, {nil}, {issuer, leaf}} {
		if err := verify(tls.ConnectionState{PeerCertificates: chain}); err == nil {
			t.Fatal("absent or stale leaf accepted")
		}
	}
}

func TestRedisPublicCleanupInspectsEveryServiceBeforeDeletion(t *testing.T) {
	d, endpoint, _, _ := redisPublicMapFixture(t)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace", Labels: databaseLabels(d)}}
	first := redisPublicMemberService(d, endpoint, endpoint.MemberAllocations[0], ns.UID)
	first.UID, first.Spec.ClusterIP = "first", "10.43.0.17"
	foreign := redisPublicMemberService(d, endpoint, endpoint.MemberAllocations[1], ns.UID)
	foreign.UID, foreign.Spec.ClusterIP = "foreign", "10.43.0.18"
	foreign.Annotations["hakopod.io/database-member-uid"] = "replacement"
	kube := kubefake.NewClientset(ns, first, foreign)
	c := &Client{kube: kube}
	if err := c.reconcileRedisPublicMemberServices(context.Background(), d, endpoint, false, func() error { return nil }); err == nil {
		t.Fatal("foreign later member did not block cleanup")
	}
	if _, err := kube.CoreV1().Services(ns.Name).Get(context.Background(), first.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("earlier member deleted before complete ownership validation", err)
	}
}

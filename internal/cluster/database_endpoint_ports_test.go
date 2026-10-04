package cluster

import (
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

func TestClickHouseSharedServiceRequiresEveryMemberForEachProtocol(t *testing.T) {
	d := database.Resource{Spec: database.Spec{Engine: "clickhouse", Shards: 1, Replicas: 1}}
	for _, purpose := range []string{"cluster", "native", "https"} {
		if got := databaseEndpointMinimum(d, database.Endpoint{Purpose: purpose}, false, false); got != 2 {
			t.Fatalf("%s endpoint requires %d members, want 2", purpose, got)
		}
	}
	postgres := database.Resource{Spec: database.Spec{Engine: "postgresql", Shards: 1, Replicas: 1}}
	if got := databaseEndpointMinimum(postgres, database.Endpoint{Purpose: "native"}, false, false); got != 1 {
		t.Fatalf("PostgreSQL native endpoint requires %d members, want 1", got)
	}
}

func TestDatabaseEndpointSliceRejectsProtocolRedirection(t *testing.T) {
	ports := []discoveryv1.EndpointPort{{Name: ptr("native-tls"), Port: ptr(int32(9440)), Protocol: ptr(corev1.ProtocolTCP)}}
	if !databaseEndpointSlicePortMatches(ports, "native-tls", 9440) {
		t.Fatal("native TLS port rejected")
	}
	ports[0].Port = ptr(int32(9000))
	if databaseEndpointSlicePortMatches(ports, "native-tls", 9440) {
		t.Fatal("plaintext target redirection accepted")
	}
	ports[0].Port = ptr(int32(9440))
	ports[0].Protocol = ptr(corev1.ProtocolUDP)
	if databaseEndpointSlicePortMatches(ports, "native-tls", 9440) {
		t.Fatal("protocol drift accepted")
	}
	ports[0].Protocol = ptr(corev1.ProtocolTCP)
	if databaseEndpointSlicePortMatches(ports, "wrong-name", 9440) || databaseEndpointSlicePortMatches(nil, "native-tls", 9440) {
		t.Fatal("missing target port accepted")
	}
	if databaseEndpointSlicePortMatches(append(ports, ports[0]), "native-tls", 9440) {
		t.Fatal("ambiguous target port accepted")
	}
}

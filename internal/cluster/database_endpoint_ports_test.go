package cluster

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

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

package cluster

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	mysqlclient "github.com/go-sql-driver/mysql"
	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestVitessPublicTemplateIdentityUsesExactPEMBytes(t *testing.T) {
	certificate := []byte("-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n")
	want := fmt.Sprintf("%x", sha256.Sum256(certificate))
	got, err := vitessPublicTemplateIdentity(certificate)
	if err != nil || got != want {
		t.Fatalf("unexpected template identity %q: %v", got, err)
	}
	if _, err = vitessPublicTemplateIdentity(nil); err == nil {
		t.Fatal("empty template certificate accepted")
	}
	if _, err = vitessPublicTemplateIdentity(make([]byte, (64<<10)+1)); err == nil {
		t.Fatal("oversized template certificate accepted")
	}
}

func TestVitessIdentityRenewalRejectsInvalidPublicNamesBeforeClusterAccess(t *testing.T) {
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Spec: database.Spec{Engine: "vitess", TLS: &database.TLSConfig{Mode: "required"}}, PublicEndpointNames: []string{"*.example.test"}}
	if err := (&Client{}).renewVitessIdentity(context.Background(), d, func() error { return nil }); err == nil {
		t.Fatal("Vitess identity renewal accepted an invalid public hostname")
	}
}

func TestVitessPublicCredentialsRequireOwnedImmutableBasicAuthSecret(t *testing.T) {
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Spec: database.Spec{Engine: "vitess"}}
	valid := &corev1.Secret{
		ObjectMeta: databaseIdentityMeta(d, "namespace-uid", "database-credentials"),
		Type:       corev1.SecretTypeBasicAuth,
		Immutable:  ptr(true),
		Data: map[string][]byte{
			corev1.BasicAuthUsernameKey: []byte("app"),
			corev1.BasicAuthPasswordKey: []byte(strings.Repeat("p", 32)),
		},
	}
	valid.UID = "credentials-uid"
	password, err := vitessPublicEndpointPassword(valid, d, "namespace-uid")
	if err != nil || string(password) != strings.Repeat("p", 32) {
		t.Fatalf("valid owned credentials rejected: %v", err)
	}
	if _, err = vitessPublicEndpointPassword(nil, d, "namespace-uid"); err == nil {
		t.Fatal("missing credentials accepted")
	}
	for name, mutate := range map[string]func(*corev1.Secret){
		"missing uid":    func(s *corev1.Secret) { s.UID = "" },
		"wrong name":     func(s *corev1.Secret) { s.Name = "foreign" },
		"deleting":       func(s *corev1.Secret) { now := metav1.Now(); s.DeletionTimestamp = &now },
		"wrong type":     func(s *corev1.Secret) { s.Type = corev1.SecretTypeOpaque },
		"mutable":        func(s *corev1.Secret) { s.Immutable = ptr(false) },
		"wrong username": func(s *corev1.Secret) { s.Data[corev1.BasicAuthUsernameKey] = []byte("root") },
		"foreign owner":  func(s *corev1.Secret) { s.OwnerReferences[0].UID = "foreign" },
		"extra owner":    func(s *corev1.Secret) { s.OwnerReferences = append(s.OwnerReferences, s.OwnerReferences[0]) },
		"short password": func(s *corev1.Secret) { s.Data[corev1.BasicAuthPasswordKey] = []byte("short") },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid.DeepCopy()
			mutate(candidate)
			if _, err := vitessPublicEndpointPassword(candidate, d, "namespace-uid"); err == nil {
				t.Fatal("unsafe Vitess credentials accepted")
			}
		})
	}
}

func vitessPublicEndpointServiceFixture(d database.Resource) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: DatabaseNamespace(d.ID), UID: "service-uid", Labels: databaseLabels(d), OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: DatabaseNamespace(d.ID), UID: "namespace-uid"}}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.20", Selector: map[string]string{databaseOwner: d.ID, vitessComponentLabel: "gateway"}, Ports: []corev1.ServicePort{{Name: "mysql", Port: 3306, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt(3306)}}}}
}

func TestVitessPublicGatewayServiceRequiresExactNamespaceOwnership(t *testing.T) {
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Spec: database.Spec{Engine: "vitess", Replicas: 1}}
	route, err := database.PublicEndpointRouteFor(d.Spec, "read_write")
	if err != nil {
		t.Fatal(err)
	}
	valid := vitessPublicEndpointServiceFixture(d)
	if !vitessPublicGatewayServiceOwned(valid, d, "namespace-uid", route) {
		t.Fatal("valid owned Vitess gateway Service rejected")
	}
	for name, mutate := range map[string]func(*corev1.Service){
		"missing uid":       func(s *corev1.Service) { s.UID = "" },
		"foreign owner":     func(s *corev1.Service) { s.OwnerReferences[0].UID = "foreign" },
		"missing owner":     func(s *corev1.Service) { s.OwnerReferences = nil },
		"extra owner":       func(s *corev1.Service) { s.OwnerReferences = append(s.OwnerReferences, s.OwnerReferences[0]) },
		"named target port": func(s *corev1.Service) { s.Spec.Ports[0].TargetPort = intstr.FromString("mysql") },
		"foreign selector":  func(s *corev1.Service) { s.Spec.Selector[databaseOwner] = "foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid.DeepCopy()
			mutate(candidate)
			if vitessPublicGatewayServiceOwned(candidate, d, "namespace-uid", route) {
				t.Fatal("unsafe Vitess gateway Service accepted")
			}
		})
	}
}

func TestVitessPublicProbeRequiresExactTargetAndPlaintextRefusal(t *testing.T) {
	refused := &mysqlclient.MySQLError{Number: 3159, Message: "Connections using insecure transport are prohibited"}
	if err := verifyVitessPublicProbeResults("app@primary", "app@primary", nil, refused); err != nil {
		t.Fatal(err)
	}
	if err := verifyVitessPublicProbeResults("app@replica", "app@primary", nil, refused); err == nil {
		t.Fatal("wrong server-owned target accepted")
	}
	if err := verifyVitessPublicProbeResults("app@primary", "app@primary", nil, errors.New("connection closed")); err == nil {
		t.Fatal("generic plaintext failure accepted")
	}
	if err := verifyVitessPublicProbeResults("app@primary", "app@primary", nil, nil); err == nil {
		t.Fatal("successful plaintext connection accepted")
	}
}

func TestVitessPublicClientRejectsUnenforcedReadOnlyRoute(t *testing.T) {
	route := database.PublicEndpointRoute{Purpose: "read_only", Protocol: "mysql", Routing: "vitess_gateway", ReadOnly: true, BackendService: "database", BackendPort: 3306, BackendPortName: "mysql", BackendUser: "app", BackendDatabase: "app@replica"}
	if _, err := (&Client{}).vitessPublicEndpointClient(context.Background(), database.Resource{}, database.Member{}, route, "database.example.com", "10.43.0.20", nil, nil, false); err == nil {
		t.Fatal("unenforced Vitess read-only route accepted")
	}
}

func vitessPublicEndpointSliceFixture(service *corev1.Service, gateways []vitessPublicGateway) discoveryv1.EndpointSlice {
	controller, ready := true, true
	portName, port, protocol := "mysql", int32(3306), corev1.ProtocolTCP
	endpoints := make([]discoveryv1.Endpoint, len(gateways))
	for i, gateway := range gateways {
		endpoints[i] = discoveryv1.Endpoint{
			Addresses:  []string{gateway.Address},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef:  &corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: service.Namespace, Name: gateway.Member.Name, UID: types.UID(gateway.Member.UID)},
		}
	}
	return discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "database-abcde",
			Namespace: service.Namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID, Controller: &controller,
			}},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Name: &portName, Port: &port, Protocol: &protocol}},
		Endpoints:   endpoints,
	}
}

func TestVitessPublicGatewayEndpointSliceRequiresExactOwnedPods(t *testing.T) {
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Spec: database.Spec{Engine: "vitess", Replicas: 1}}
	route := database.PublicEndpointRoute{BackendService: "database", BackendPort: 3306, BackendPortName: "mysql"}
	service := vitessPublicEndpointServiceFixture(d)
	gateways := []vitessPublicGateway{
		{Member: database.Member{Name: "database-zone1-vtgate-0", UID: "gateway-0"}, Address: "10.42.0.20"},
		{Member: database.Member{Name: "database-zone1-vtgate-1", UID: "gateway-1"}, Address: "10.42.0.21"},
	}
	valid := vitessPublicEndpointSliceFixture(service, gateways)
	if !vitessPublicGatewayEndpointsOwned([]discoveryv1.EndpointSlice{valid}, service, gateways, route) {
		t.Fatal("valid owned Vitess gateway EndpointSlice rejected")
	}
	for name, mutate := range map[string]func(*discoveryv1.EndpointSlice){
		"foreign service":    func(s *discoveryv1.EndpointSlice) { s.Labels[discoveryv1.LabelServiceName] = "foreign" },
		"foreign owner":      func(s *discoveryv1.EndpointSlice) { s.OwnerReferences[0].UID = "foreign" },
		"missing controller": func(s *discoveryv1.EndpointSlice) { s.OwnerReferences[0].Controller = nil },
		"wrong port":         func(s *discoveryv1.EndpointSlice) { *s.Ports[0].Port = 3307 },
		"foreign pod":        func(s *discoveryv1.EndpointSlice) { s.Endpoints[0].TargetRef.UID = "foreign" },
		"wrong pod address":  func(s *discoveryv1.EndpointSlice) { s.Endpoints[0].Addresses[0] = "10.42.0.99" },
		"unready pod":        func(s *discoveryv1.EndpointSlice) { ready := false; s.Endpoints[0].Conditions.Ready = &ready },
		"duplicate pod":      func(s *discoveryv1.EndpointSlice) { s.Endpoints[1] = *s.Endpoints[0].DeepCopy() },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid.DeepCopy()
			mutate(candidate)
			if vitessPublicGatewayEndpointsOwned([]discoveryv1.EndpointSlice{*candidate}, service, gateways, route) {
				t.Fatal("unsafe Vitess gateway EndpointSlice accepted")
			}
		})
	}
	if vitessPublicGatewayEndpointsOwned([]discoveryv1.EndpointSlice{valid, valid}, service, gateways, route) {
		t.Fatal("multiple Vitess gateway EndpointSlices accepted")
	}
}

func TestVitessPublicProbeTargetsCoverEachPodAndServiceOnce(t *testing.T) {
	gateways := []vitessPublicGateway{
		{Member: database.Member{Name: "gateway-0", UID: "uid-0"}, Address: "10.42.0.20", Direct: true},
		{Member: database.Member{Name: "gateway-1", UID: "uid-1"}, Address: "10.42.0.21", Direct: true},
	}
	targets, err := vitessPublicProbeTargets(gateways, "10.43.0.20")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 || targets[0] != gateways[0] || targets[1] != gateways[1] || targets[2].Address != "10.43.0.20" || targets[2].Member != gateways[0].Member {
		t.Fatalf("unexpected Vitess public probe targets: %#v", targets)
	}
	if _, err = vitessPublicProbeTargets(gateways, gateways[0].Address); err == nil {
		t.Fatal("Service address equal to a gateway PodIP accepted")
	}
	duplicate := append([]vitessPublicGateway(nil), gateways...)
	duplicate[1].Address = duplicate[0].Address
	if _, err = vitessPublicProbeTargets(duplicate, "10.43.0.20"); err == nil {
		t.Fatal("duplicate gateway PodIP accepted")
	}
}

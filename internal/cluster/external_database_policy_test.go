package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func externalPolicyFixture(t *testing.T) (Target, spec.Binding, DatabaseConnection) {
	t.Helper()
	target := testTarget(t)
	b := spec.Binding{ExternalDatabase: strings.Repeat("e", 32), ExternalDatabaseRevision: 1, Protocol: "mysql"}
	svc := target.Spec.Services["api"]
	svc.Bindings = map[string]spec.Binding{"DATABASE_URL": b}
	target.Spec.Services["api"] = svc
	raw, err := externaldatabase.ConnectionURL(externaldatabase.Spec{SchemaVersion: 1, Name: "fixture", Provider: "planetscale", Engine: "mysql", Host: "fixture.psdb.cloud", Port: 3306, Database: "app"}, externaldatabase.Credentials{Username: "user", Password: "fixture-private-password"})
	if err != nil {
		t.Fatal(err)
	}
	c := DatabaseConnection{URL: raw, Port: 3306, ExternalIPs: []string{"8.8.8.8"}}
	target.databaseConnections = map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": c}}
	target.policy = &WorkloadPolicy{EgressPorts: []int32{80, 443}, DeniedEgressCIDRs: []string{"1.1.1.0/24", "::ffff:9.9.9.0/120"}}
	return target, b, c
}

func TestExternalDisconnectSecretCleanupRespectsOwnershipAndClaim(t *testing.T) {
	for _, fault := range []string{"application", "service", "claim"} {
		t.Run(fault, func(t *testing.T) {
			target, _, _ := externalPolicyFixture(t)
			client := &Client{kube: fake.NewClientset()}
			ctx := context.Background()
			if err := client.prepareWorkloadSecrets(ctx, target, "api", target.Spec.Services["api"]); err != nil {
				t.Fatal(err)
			}
			api := client.kube.CoreV1().Secrets(Namespace(target.ApplicationID))
			secret, err := api.Get(ctx, "api-environment", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "application":
				secret.Labels[ownerKey] = "another-application"
			case "service":
				secret.Labels[serviceKey] = "another-service"
			case "claim":
				target.BeforeStep = func(context.Context) error { return fmt.Errorf("maintenance claim lost") }
			}
			if _, err = api.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			service := target.Spec.Services["api"]
			service.Bindings = nil
			if err = client.prepareWorkloadSecrets(ctx, target, "api", service); err == nil {
				t.Fatal("cleanup ignored ownership or durable claim")
			}
			after, err := api.Get(ctx, "api-environment", metav1.GetOptions{})
			if err != nil || string(after.Data["DATABASE_URL"]) != string(secret.Data["DATABASE_URL"]) {
				t.Fatal("rejected cleanup changed credentials", err)
			}
		})
	}
}

func TestExternalDatabaseEgressRemainsExactUnderCloudPolicy(t *testing.T) {
	target, b, connection := externalPolicyFixture(t)
	if err := validateExternalDatabaseSnapshot(target, b, connection); err != nil {
		t.Fatal(err)
	}
	grants := 0
	for _, policy := range policies(target) {
		for _, rule := range policy.Spec.Egress {
			for _, port := range rule.Ports {
				if port.Port != nil && port.Port.IntVal == 3306 {
					grants++
					if len(rule.To) != 1 || rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "8.8.8.8/32" || len(rule.Ports) != 1 {
						t.Fatal("database port widened generic Cloud egress")
					}
				}
			}
		}
	}
	if grants != 1 {
		t.Fatalf("expected one exact provider grant, got %d", grants)
	}
	for _, ip := range []string{"10.0.0.1", "169.254.169.254", "100.100.100.200", "1.1.1.1", "9.9.9.9", "::ffff:8.8.8.8", "fd00::1", "64:ff9b::a00:1"} {
		changed := connection
		changed.ExternalIPs = []string{ip}
		if validateExternalDatabaseSnapshot(target, b, changed) == nil {
			t.Errorf("forbidden address accepted: %s", ip)
		}
	}
	changed := connection
	changed.URL = strings.ReplaceAll(changed.URL, "VERIFY_IDENTITY", "DISABLED")
	if validateExternalDatabaseSnapshot(target, b, changed) == nil {
		t.Fatal("TLS downgrade reached policy")
	}
}

func TestExternalBindingSnapshotCopiesOnlyServiceSecret(t *testing.T) {
	target, _, connection := externalPolicyFixture(t)
	client := &Client{kube: fake.NewClientset()}
	client.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
		return map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": connection}}, nil
	})
	if err := client.snapshotDatabaseBindings(context.Background(), &target); err != nil {
		t.Fatal(err)
	}
	if err := client.prepareWorkloadSecrets(context.Background(), target, "api", target.Spec.Services["api"]); err != nil {
		t.Fatal(err)
	}
	secret, err := client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(context.Background(), "api-environment", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["DATABASE_URL"]) != connection.URL {
		t.Fatal("service did not receive its verified connection")
	}
	if len(secret.Data) != 1 {
		t.Fatal("unrelated credentials were copied")
	}
	service := target.Spec.Services["api"]
	service.Bindings = nil
	if err = client.prepareWorkloadSecrets(context.Background(), target, "api", service); err != nil {
		t.Fatal("disconnect cleanup failed", err)
	}
	secret, err = client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(context.Background(), "api-environment", metav1.GetOptions{})
	if err != nil || len(secret.Data) != 0 {
		t.Fatal("last-binding disconnect retained plaintext credentials", err)
	}
	connection.ExternalIPs = []string{"10.0.0.1"}
	if err = client.snapshotDatabaseBindings(context.Background(), &target); err == nil {
		t.Fatal("private endpoint reached a deployment snapshot")
	}
}

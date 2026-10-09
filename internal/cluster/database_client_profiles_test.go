package cluster

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNodeDatabaseClientProfileBuildsOneBoundedTrustBundle(t *testing.T) {
	target := testTarget(t)
	first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
	service := target.Spec.Services["api"]
	service.Bindings = map[string]spec.Binding{
		"REDIS_URL": {ManagedDatabase: first, Protocol: "redis", Endpoint: "read_write"},
		"QUEUE_URL": {ManagedDatabase: second, Protocol: "redis", Endpoint: "read_write"},
	}
	service.DatabaseClientProfiles = map[string]string{"REDIS_URL": spec.DatabaseClientNodeExtraCAV1, "QUEUE_URL": spec.DatabaseClientNodeExtraCAV1}
	target.Spec.Services["api"] = service
	firstCA, secondCA := trustFixtureCA(t), trustFixtureCA(t)
	target.databaseConnections = map[string]map[string]DatabaseConnection{"api": {
		"REDIS_URL": {URL: "rediss://first", Port: 6379, CA: firstCA},
		"QUEUE_URL": {URL: "rediss://second", Port: 6379, CA: secondCA},
	}}
	trust := databaseTrustObject(target, "api")
	bundle := trust.Data[nodeExtraCABundle]
	if !strings.Contains(bundle, strings.TrimSpace(firstCA)) || !strings.Contains(bundle, strings.TrimSpace(secondCA)) || len(trust.Data) != 3 {
		t.Fatal("Node trust bundle did not include each profiled CA once")
	}
	workload := deployment(target, "api", service, time.Minute)
	found := 0
	for _, env := range workload.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "NODE_EXTRA_CA_CERTS" && env.Value == databaseTrustDirectory+"/"+nodeExtraCABundle && env.ValueFrom == nil {
			found++
		}
	}
	if found != 1 {
		t.Fatal("Node did not receive the generated additive CA bundle")
	}
}

func TestInfisicalProfileInjectsOnlyBase64PublicCA(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	id := strings.Repeat("c", 32)
	service := target.Spec.Services["api"]
	service.Bindings = map[string]spec.Binding{"DB_CONNECTION_URI": {ManagedDatabase: id, Protocol: "postgres", Endpoint: "read_write"}}
	service.DatabaseClientProfiles = map[string]string{"DB_CONNECTION_URI": spec.DatabaseClientInfisicalPostgresV1}
	target.Spec.Services["api"] = service
	ca := trustFixtureCA(t)
	target.databaseConnections = map[string]map[string]DatabaseConnection{"api": {"DB_CONNECTION_URI": {URL: "postgres://app:password@database:5432/app?sslmode=verify-full", Port: 5432, CA: ca}}}
	client := &Client{kube: fake.NewClientset()}
	if err := client.prepareWorkloadSecrets(ctx, target, "api", service); err != nil {
		t.Fatal(err)
	}
	secret, err := client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "api-environment", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(secret.Data["DB_ROOT_CERT"]))
	if err != nil || string(decoded) != ca {
		t.Fatal("Infisical did not receive its pinned base64 CA input")
	}
	if len(secret.Data) != 2 || !strings.Contains(string(secret.Data["DB_CONNECTION_URI"]), "sslmode=verify-full") {
		t.Fatal("profile changed credentials or removed verified PostgreSQL mode")
	}
}

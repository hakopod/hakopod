package cluster

import (
	"context"
	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
	"os"
	"testing"
)

func developmentDatabaseOptions(t *testing.T) Options {
	t.Helper()
	if os.Getenv("HAKOPOD_TEST_HOSTED_DATABASES") != "1" {
		return Options{ClickHouseSandbox: os.Getenv("HAKOPOD_TEST_CLICKHOUSE_SANDBOX") == "1"}
	}
	return Options{DatabasePolicy: func(context.Context, string, string, database.Spec) (DatabasePolicy, error) {
		return DatabasePolicy{NodeName: "k3d-hakopod-dev-agent-0", Pool: "free", RuntimeClass: "runsc", StorageClass: "hakopod-hosted-development"}, nil
	}}
}

func TestDatabasePolicyRequiresVerifiedSandboxAndPinsControllers(t *testing.T) {
	p := DatabasePolicy{NodeName: "worker", Pool: "free", RuntimeClass: "runsc", StorageClass: "block"}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: p.NodeName, Labels: map[string]string{"hakopod.com/pool": "free"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	kube := fake.NewClientset(node, &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "runsc"}, Handler: "runsc"}, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "block"}})
	c := &Client{kube: kube, options: Options{DatabasePolicy: func(context.Context, string, string, database.Spec) (DatabasePolicy, error) { return p, nil }}}
	ctx := context.Background()
	if _, err := c.databasePolicy(ctx, database.Resource{}); err == nil {
		t.Fatal("unverified default runtime accepted")
	}
	node.Labels[DatabaseDefaultRuntimeLabel] = "runsc"
	if _, err := kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"standalone", "cluster"} {
			d := database.Resource{ID: "fixture", Spec: database.Spec{SchemaVersion: 1, Name: "fixture", Engine: engine, Version: "17", Mode: mode, Shards: 1, CPU: "100m", Memory: "128Mi", StorageGiB: 1}}
			if engine == "redis" {
				d.Spec.Version = "8"
			}
			if mode == "cluster" {
				d.Spec.Replicas = 1
				if engine == "redis" {
					d.Spec.Shards = 3
				}
			}
			object, err := c.databaseObject(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			path := []string{"spec"}
			if engine == "postgresql" {
				path = append(path, "affinity")
			} else if mode == "cluster" {
				path = append(path, "redisLeader")
			}
			selector, _, _ := unstructured.NestedStringMap(object.Object, append(path, "nodeSelector")...)
			if selector[DatabaseDefaultRuntimeLabel] != "runsc" || selector["hakopod.com/pool"] != "free" {
				t.Fatal("controller escaped sandbox worker", selector)
			}
			affinityPath := append(path, "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
			if engine == "postgresql" {
				affinityPath = []string{"spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms"}
			}
			terms, _, _ := unstructured.NestedSlice(object.Object, affinityPath...)
			if len(terms) != 1 {
				t.Fatal("controller node restriction is missing")
			}
		}
	}
}

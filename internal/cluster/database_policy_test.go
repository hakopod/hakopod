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

func TestDatabasePodPolicyMatchesRequiredAffinity(t *testing.T) {
	policy := &DatabasePolicy{NodeNames: []string{"worker-1", "worker-2", "worker-3"}, Pool: "vitess-acceptance", RuntimeClass: "runsc"}
	requirement := func(key, value string) corev1.NodeSelectorRequirement {
		return corev1.NodeSelectorRequirement{Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{value}}
	}
	term := func(node string) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				requirement("hakopod.com/pool", "vitess-acceptance"),
				requirement(DatabaseDefaultRuntimeLabel, "runsc"),
				requirement(corev1.LabelArchStable, "amd64"),
			},
			MatchFields: []corev1.NodeSelectorRequirement{requirement("metadata.name", node)},
		}
	}
	affinity := func(terms ...corev1.NodeSelectorTerm) *corev1.Affinity {
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms}}}
	}
	observed := corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker-2", Affinity: affinity(term("worker-1"), term("worker-2"), term("worker-3"))}}

	tests := []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{name: "observed required affinity", pod: observed, want: true},
		{name: "exact selector", pod: corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker-2", NodeSelector: map[string]string{"hakopod.com/pool": "vitess-acceptance", DatabaseDefaultRuntimeLabel: "runsc"}}}, want: true},
		{name: "unauthorized node", pod: func() corev1.Pod { pod := observed.DeepCopy(); pod.Spec.NodeName = "worker-4"; return *pod }()},
		{name: "preferred only", pod: corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker-2", Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Preference: term("worker-2")}}}}}}},
		{name: "empty required terms", pod: corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker-2", Affinity: affinity()}}},
		{name: "weakened or term", pod: func() corev1.Pod {
			pod := observed.DeepCopy()
			pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[2].MatchExpressions = pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[2].MatchExpressions[1:]
			return *pod
		}()},
		{name: "wrong affinity pool", pod: func() corev1.Pod {
			pod := observed.DeepCopy()
			pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[1].MatchExpressions[0].Values[0] = "other"
			return *pod
		}()},
		{name: "contradictory selector", pod: func() corev1.Pod {
			pod := observed.DeepCopy()
			pod.Spec.NodeSelector = map[string]string{"hakopod.com/pool": "other", DatabaseDefaultRuntimeLabel: "runsc"}
			return *pod
		}()},
		{name: "partial selector", pod: func() corev1.Pod {
			pod := observed.DeepCopy()
			pod.Spec.NodeSelector = map[string]string{"hakopod.com/pool": "vitess-acceptance"}
			return *pod
		}()},
		{name: "wrong explicit runtime", pod: func() corev1.Pod { pod := observed.DeepCopy(); pod.Spec.RuntimeClassName = ptr("runc"); return *pod }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databasePodPolicyMatches(tt.pod, policy); got != tt.want {
				t.Fatalf("databasePodPolicyMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

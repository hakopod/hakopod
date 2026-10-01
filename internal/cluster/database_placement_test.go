package cluster

import (
	"context"
	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestDatabasePlacementRejectsInsufficientDomains(t *testing.T) {
	node := func(name, zone string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelHostname: name, corev1.LabelTopologyZone: zone}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	}
	c := &Client{kube: fake.NewClientset(node("a", "zone-a"), node("b", "zone-a"))}
	s := database.Spec{Shards: 1, Replicas: 1, Placement: database.Placement{Spread: "zones"}}
	if c.validateDatabasePlacementNodes(context.Background(), s, nil) == nil {
		t.Fatal("two nodes in one zone accepted")
	}
	s.Placement.Spread = "nodes"
	if err := c.validateDatabasePlacementNodes(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	s.Placement.NodeNames = []string{"a", "missing"}
	if c.validateDatabasePlacementNodes(context.Background(), s, nil) == nil {
		t.Fatal("missing selected node accepted")
	}
	if c.validateDatabasePlacementNodes(context.Background(), database.Spec{Shards: 1, Replicas: 1, Placement: database.Placement{Spread: "nodes"}}, &DatabasePolicy{NodeName: "a"}) == nil {
		t.Fatal("host allocation was broadened")
	}
}

func TestDatabasePlacementObservationRequiresEveryMember(t *testing.T) {
	s := database.Spec{Shards: 1, Replicas: 1, Placement: database.Placement{Spread: "zones"}}
	members := []database.Member{{Node: "a", Zone: "z1"}, {Node: "b", Zone: "z1"}}
	if databasePlacementObservation(s, members).Verified {
		t.Fatal("co-located replicas accepted")
	}
	members[1].Zone = "z2"
	if !databasePlacementObservation(s, members).Verified {
		t.Fatal("separate zones rejected")
	}
	members[1].Zone = ""
	if databasePlacementObservation(s, members).Verified {
		t.Fatal("unknown zone accepted")
	}
	if databasePlacementObservation(s, members[:1]).Verified {
		t.Fatal("partial placement accepted")
	}
	s.Placement = database.Placement{Spread: "nodes", NodeNames: []string{"a", "c"}}
	if databasePlacementObservation(s, members).Verified {
		t.Fatal("escaped node selection accepted")
	}
}

func TestDatabasePlacementGeneratesHardSchedulerConstraints(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		s := database.Spec{Engine: engine, Mode: "cluster", Placement: database.Placement{Spread: "zones"}}
		o := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
		applyDatabasePlacement(o, s, []string{"first", "second"})
		if engine == "postgresql" {
			terms, _, _ := unstructured.NestedSlice(o.Object, "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
			if len(terms) != 2 {
				t.Fatal("selected nodes require separate OR terms")
			}
			for _, term := range terms {
				fields := term.(map[string]any)["matchFields"].([]any)
				if len(fields[0].(map[string]any)["values"].([]any)) != 1 {
					t.Fatal("Kubernetes field selectors require one value per term")
				}
			}
			kind, _, _ := unstructured.NestedString(o.Object, "spec", "affinity", "podAntiAffinityType")
			key, _, _ := unstructured.NestedString(o.Object, "spec", "affinity", "topologyKey")
			if kind != "required" || key != corev1.LabelTopologyZone {
				t.Fatal("PostgreSQL spread was not required")
			}
		} else {
			for _, role := range []string{"redisLeader", "redisFollower"} {
				terms, _, _ := unstructured.NestedSlice(o.Object, "spec", role, "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")
				if len(terms) != 1 || terms[0].(map[string]any)["topologyKey"] != corev1.LabelTopologyZone {
					t.Fatal("Redis role escaped required zone spreading")
				}
			}
		}
	}
}

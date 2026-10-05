package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func vitessInventoryTablet(d database.Resource, name, identity string, owner metav1.OwnerReference) corev1.Pod {
	pod := vitessSocketPolicyPod()
	pod.Name, pod.Namespace, pod.UID = name, DatabaseNamespace(d.ID), types.UID(name+"-uid")
	pod.Labels = map[string]string{databaseOwner: d.ID, managedBy: "hakopod", vitessComponentLabel: "tablet", "planetscale.com/shard": "x-x"}
	pod.Annotations[vitessIdentityAnnotation] = identity
	pod.OwnerReferences = []metav1.OwnerReference{owner}
	pod.Spec.ServiceAccountName = "database-vitess-workload"
	for index := range pod.Spec.Containers {
		cpu, memory := database.VitessTabletCPU, database.VitessTabletMemory
		if pod.Spec.Containers[index].Name == "mysqld" {
			cpu, memory = d.Spec.CPU, d.Spec.Memory
		}
		pod.Spec.Containers[index].Image = vitessServerImage
		pod.Spec.Containers[index].Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}
	}
	return pod
}

func vitessInventoryRoot(t *testing.T, d database.Resource) (*corev1.Namespace, *unstructured.Unstructured) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	root, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	root.SetNamespace(ns.Name)
	root.SetUID("database-uid")
	return ns, root
}

func TestVitessObservationInventoryRequiresExactNameAndUID(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "tablet-a", UID: types.UID("tablet-a-uid")}}
	inventory := &vitessObservationInventory{tablets: map[string]corev1.Pod{pod.Name: pod}}

	got, container, err := inventory.tablet(database.Member{Name: pod.Name, UID: string(pod.UID)})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != pod.Name || container != "vttablet" {
		t.Fatal("verified tablet was not resolved", got, container)
	}
	for _, member := range []database.Member{{Name: pod.Name, UID: "old-uid"}, {Name: "tablet-b", UID: string(pod.UID)}} {
		if _, _, err = inventory.tablet(member); err == nil {
			t.Fatal("stale observation member resolved from inventory", member)
		}
	}
}

func TestVitessObservationInventoryReturnsPodCopy(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "tablet-a", UID: types.UID("tablet-a-uid"), Labels: map[string]string{"immutable": "yes"}}}
	inventory := &vitessObservationInventory{tablets: map[string]corev1.Pod{pod.Name: pod}}
	member := database.Member{Name: pod.Name, UID: string(pod.UID)}

	first, _, err := inventory.tablet(member)
	if err != nil {
		t.Fatal(err)
	}
	first.Labels["immutable"] = "changed"
	second, _, err := inventory.tablet(member)
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.Labels["immutable"] != "yes" {
		t.Fatal("caller mutated the scoped inventory")
	}
}

func TestVitessObservationInventoryCachesSharedOwnerGraph(t *testing.T) {
	d := vitessTestDatabase()
	d.Spec.Mode, d.Spec.Shards, d.Spec.Replicas = "cluster", 8, 5
	d.Spec.Vitess.Tables = []database.VitessTable{{Name: "events", ShardingColumn: "account_id"}}
	ns, root := vitessInventoryRoot(t, d)
	identity := strings.Repeat("a", 64)
	keyspace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessKeyspace", "metadata": map[string]any{"name": "keyspace", "namespace": ns.Name, "uid": "keyspace-uid", "ownerReferences": []any{map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessCluster", "name": "database", "uid": "database-uid"}}}}}
	objects := []runtime.Object{keyspace}
	pods := make([]corev1.Pod, 0, database.MaxMembers)
	for shard := 0; shard < 8; shard++ {
		name := fmt.Sprintf("shard-%d", shard)
		uid := types.UID(name + "-uid")
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessShard", "metadata": map[string]any{"name": name, "namespace": ns.Name, "uid": string(uid), "ownerReferences": []any{map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessKeyspace", "name": "keyspace", "uid": "keyspace-uid"}}}}})
		owner := metav1.OwnerReference{APIVersion: "planetscale.com/v2", Kind: "VitessShard", Name: name, UID: uid}
		for replica := 0; replica < 6; replica++ {
			pods = append(pods, vitessInventoryTablet(d, fmt.Sprintf("tablet-%d-%d", shard, replica), identity, owner))
		}
	}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	gets := 0
	dynamic.PrependReactor("get", "*", func(k8stesting.Action) (bool, runtime.Object, error) { gets++; return false, nil, nil })
	client := &Client{dynamic: dynamic}
	inventory, err := client.newVitessObservationInventory(context.Background(), d, ns, root, identity, pods)
	if err != nil {
		t.Fatal(err)
	}
	if inventory == nil || len(inventory.tablets) != database.MaxMembers || gets != 9 {
		t.Fatalf("shared owner graph was not cached: inventory=%v gets=%d", inventory != nil, gets)
	}
}

func TestVitessObservationInventoryRejectsAuthorityChanges(t *testing.T) {
	d := vitessTestDatabase()
	ns, root := vitessInventoryRoot(t, d)
	identity := strings.Repeat("a", 64)
	owner := metav1.OwnerReference{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: root.GetUID()}
	valid := vitessInventoryTablet(d, "tablet", identity, owner)

	for name, mutate := range map[string]func(*corev1.Namespace, *unstructured.Unstructured, *corev1.Pod){
		"namespace-owner": func(n *corev1.Namespace, _ *unstructured.Unstructured, _ *corev1.Pod) {
			n.Labels[databaseOwner] = "other"
		},
		"namespace-uid": func(n *corev1.Namespace, _ *unstructured.Unstructured, _ *corev1.Pod) { n.UID = "" },
		"namespace-deleting": func(n *corev1.Namespace, _ *unstructured.Unstructured, _ *corev1.Pod) {
			n.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
		},
		"controller-uid": func(_ *corev1.Namespace, r *unstructured.Unstructured, _ *corev1.Pod) { r.SetUID("replaced") },
		"controller-owner": func(_ *corev1.Namespace, r *unstructured.Unstructured, _ *corev1.Pod) {
			r.SetLabels(map[string]string{databaseOwner: "other", managedBy: "hakopod"})
		},
		"controller-deleting": func(_ *corev1.Namespace, r *unstructured.Unstructured, _ *corev1.Pod) {
			r.SetDeletionTimestamp(&metav1.Time{Time: metav1.Now().Time})
		},
		"pod-config": func(_ *corev1.Namespace, _ *unstructured.Unstructured, p *corev1.Pod) {
			p.Spec.Containers[0].Image = "unapproved"
		},
		"pod-identity": func(_ *corev1.Namespace, _ *unstructured.Unstructured, p *corev1.Pod) {
			p.Annotations[vitessIdentityAnnotation] = strings.Repeat("b", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			n, r, p := ns.DeepCopy(), root.DeepCopy(), valid.DeepCopy()
			mutate(n, r, p)
			if _, err := (&Client{}).newVitessObservationInventory(context.Background(), d, n, r, identity, []corev1.Pod{*p}); err == nil {
				t.Fatal("changed authority entered inventory")
			}
		})
	}
}

func TestVitessObservationInventoryRejectsDeletingOrMismatchedParent(t *testing.T) {
	d := vitessTestDatabase()
	ns, root := vitessInventoryRoot(t, d)
	identity := strings.Repeat("a", 64)
	shardUID := types.UID("shard-uid")
	pod := vitessInventoryTablet(d, "tablet", identity, metav1.OwnerReference{APIVersion: "planetscale.com/v2", Kind: "VitessShard", Name: "shard", UID: shardUID})
	for _, test := range []struct {
		name     string
		uid      types.UID
		deleting bool
	}{{"mismatched-uid", "other-uid", false}, {"deleting", shardUID, true}} {
		t.Run(test.name, func(t *testing.T) {
			shard := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessShard", "metadata": map[string]any{"name": "shard", "namespace": ns.Name, "uid": string(test.uid), "ownerReferences": []any{map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessCluster", "name": "database", "uid": "database-uid"}}}}}
			if test.deleting {
				shard.SetDeletionTimestamp(&metav1.Time{Time: metav1.Now().Time})
			}
			client := &Client{dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), shard)}
			if _, err := client.newVitessObservationInventory(context.Background(), d, ns, root, identity, []corev1.Pod{pod}); err == nil {
				t.Fatal("unsafe owner entered inventory")
			}
		})
	}
}

func TestVitessObservationInventoryRejectsMismatchedCachedParentUID(t *testing.T) {
	d := vitessTestDatabase()
	owner := metav1.OwnerReference{APIVersion: "planetscale.com/v2", Kind: "VitessShard", Name: "shard", UID: "expected-uid"}
	key := vitessShardResource.String() + "/" + DatabaseNamespace(d.ID) + "/shard"
	cache := map[string]vitessOwnerCacheEntry{key: {uid: "other-uid", owners: []metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: "database-uid"}}}}
	if (&Client{}).vitessOwnedChainCached(context.Background(), DatabaseNamespace(d.ID), "tablet-uid", []metav1.OwnerReference{owner}, "database-uid", cache) {
		t.Fatal("mismatched cached parent UID authorized a tablet")
	}
}

func TestVitessTabletForExecRejectsLiveChanges(t *testing.T) {
	d := vitessTestDatabase()
	identity := strings.Repeat("a", 64)
	owner := metav1.OwnerReference{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: "database-uid"}
	stored := vitessInventoryTablet(d, "tablet", identity, owner)
	inventory := &vitessObservationInventory{database: d, identity: identity, tablets: map[string]corev1.Pod{stored.Name: stored}}
	member := database.Member{Name: stored.Name, UID: string(stored.UID)}
	for name, mutate := range map[string]func(*corev1.Pod){
		"uid":      func(p *corev1.Pod) { p.UID = "replacement-uid" },
		"deleting": func(p *corev1.Pod) { p.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time} },
		"image":    func(p *corev1.Pod) { p.Spec.Containers[0].Image = "unapproved" },
		"identity": func(p *corev1.Pod) { p.Annotations[vitessIdentityAnnotation] = strings.Repeat("b", 64) },
		"owner":    func(p *corev1.Pod) { p.OwnerReferences[0].UID = "other-uid" },
	} {
		t.Run(name, func(t *testing.T) {
			live := stored.DeepCopy()
			mutate(live)
			client := &Client{kube: kubefake.NewSimpleClientset(live)}
			if _, _, err := client.tabletForExec(context.Background(), member, inventory); err == nil {
				t.Fatal("changed live Pod remained executable")
			}
		})
	}
}

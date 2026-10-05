package cluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func vitessStorageFixture(t *testing.T, members int) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
	t.Helper()
	d := vitessTestDatabase()
	if members == database.MaxMembers {
		d.Spec.Mode, d.Spec.Shards, d.Spec.Replicas = "cluster", 8, 5
		d.Spec.Vitess.Tables = []database.VitessTable{{Name: "events", ShardingColumn: "account_id"}}
	} else if members == 2 {
		d.Spec.Mode, d.Spec.Shards, d.Spec.Replicas = "cluster", 1, 1
	}
	owner := metav1.OwnerReference{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: "database-uid"}
	inventory := &vitessObservationInventory{database: d, tablets: make(map[string]corev1.Pod, members)}
	observed := make([]database.Member, 0, members)
	claims := make([]*corev1.PersistentVolumeClaim, 0, members)
	for index := 0; index < members; index++ {
		name := fmt.Sprintf("tablet-%02d", index)
		claimName := name + "-data"
		pod := vitessInventoryTablet(d, name, "", owner)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}}})
		inventory.tablets[name] = pod
		observed = append(observed, database.Member{Name: name, UID: string(pod.UID)})
		claims = append(claims, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: pod.Namespace, UID: types.UID(claimName + "-uid"), Labels: map[string]string{"planetscale.com/cluster": "database", "planetscale.com/component": "vttablet"}},
			Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", d.Spec.StorageGiB))}},
		})
	}
	return d, observed, inventory, claims
}

func vitessStorageClient(claims []*corev1.PersistentVolumeClaim) *kubefake.Clientset {
	objects := make([]runtime.Object, 0, len(claims))
	for _, claim := range claims {
		objects = append(objects, claim)
	}
	return kubefake.NewSimpleClientset(objects...)
}

func TestVerifyVitessStorageListsMaximumInventoryOnce(t *testing.T) {
	d, members, inventory, claims := vitessStorageFixture(t, database.MaxMembers)
	kube := vitessStorageClient(claims)
	lists, gets := 0, 0
	kube.Fake.PrependReactor("list", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		restrictions := action.(k8stesting.ListAction).GetListRestrictions()
		if restrictions.Labels == nil || restrictions.Labels.String() != "planetscale.com/cluster=database,planetscale.com/component=vttablet" {
			t.Fatal("Vitess storage list did not use the exact tablet claim selector")
		}
		optionsAction, ok := action.(interface{ GetListOptions() metav1.ListOptions })
		if !ok || optionsAction.GetListOptions().Limit != database.MaxMembers+1 {
			t.Fatal("Vitess storage list did not use the bounded list limit")
		}
		return false, nil, nil
	})
	kube.Fake.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) { gets++; return false, nil, nil })
	kube.Fake.PrependReactor("get", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) { gets++; return false, nil, nil })
	if err := (&Client{kube: kube}).verifyVitessStorage(context.Background(), d, members, inventory); err != nil {
		t.Fatal(err)
	}
	if lists != 1 || gets != 0 {
		t.Fatalf("Vitess storage verification was not one bounded list: lists=%d gets=%d", lists, gets)
	}
}

func TestVerifyVitessStorageRejectsInvalidInventoryAndClaims(t *testing.T) {
	for name, change := range map[string]func(database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim){
		"mismatched-inventory": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			inventory.database.ID = "other"
			return d, members, inventory, claims
		},
		"missing-member": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			return d, nil, inventory, claims
		},
		"missing-claim": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			return d, members, inventory, nil
		},
		"deleting-claim": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
			return d, members, inventory, claims
		},
		"empty-claim-uid": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].UID = ""
			return d, members, inventory, claims
		},
		"wrong-namespace": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].Namespace = "other"
			return d, members, inventory, claims
		},
		"wrong-label": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].Labels["planetscale.com/component"] = "vtbackup"
			return d, members, inventory, claims
		},
		"unbound": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].Status.Phase = corev1.ClaimPending
			return d, members, inventory, claims
		},
		"conditions": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].Status.Conditions = []corev1.PersistentVolumeClaimCondition{{Type: corev1.PersistentVolumeClaimResizing}}
			return d, members, inventory, claims
		},
		"undersized": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].Status.Capacity[corev1.ResourceStorage] = resource.MustParse("512Mi")
			return d, members, inventory, claims
		},
		"read-only-access-mode": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			claims[0].Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}
			return d, members, inventory, claims
		},
		"read-only-reference": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			pod := inventory.tablets[members[0].Name]
			pod.Spec.Volumes[len(pod.Spec.Volumes)-1].PersistentVolumeClaim.ReadOnly = true
			inventory.tablets[pod.Name] = pod
			return d, members, inventory, claims
		},
		"no-tablet-claim": func(d database.Resource, members []database.Member, inventory *vitessObservationInventory, claims []*corev1.PersistentVolumeClaim) (database.Resource, []database.Member, *vitessObservationInventory, []*corev1.PersistentVolumeClaim) {
			pod := inventory.tablets[members[0].Name]
			pod.Spec.Volumes = pod.Spec.Volumes[:len(pod.Spec.Volumes)-1]
			inventory.tablets[pod.Name] = pod
			return d, members, inventory, claims
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, members, inventory, claims := vitessStorageFixture(t, 1)
			d, members, inventory, claims = change(d, members, inventory, claims)
			if err := (&Client{kube: vitessStorageClient(claims)}).verifyVitessStorage(context.Background(), d, members, inventory); err == nil {
				t.Fatal("unsafe Vitess storage inventory was accepted")
			}
		})
	}
	if d, members, _, claims := vitessStorageFixture(t, 1); (&Client{kube: vitessStorageClient(claims)}).verifyVitessStorage(context.Background(), d, members, nil) == nil {
		t.Fatal("nil Vitess observation inventory was accepted")
	}
}

func TestVerifyVitessStorageRejectsDuplicateMember(t *testing.T) {
	d, members, inventory, claims := vitessStorageFixture(t, 2)
	members[1] = members[0]
	if err := (&Client{kube: vitessStorageClient(claims)}).verifyVitessStorage(context.Background(), d, members, inventory); err == nil {
		t.Fatal("duplicate Vitess member was accepted")
	}
}

func TestVerifyVitessStorageRejectsDuplicateClaimReference(t *testing.T) {
	d, members, inventory, claims := vitessStorageFixture(t, 2)
	second := inventory.tablets[members[1].Name]
	second.Spec.Volumes[len(second.Spec.Volumes)-1].PersistentVolumeClaim.ClaimName = claims[0].Name
	inventory.tablets[second.Name] = second
	if err := (&Client{kube: vitessStorageClient(claims)}).verifyVitessStorage(context.Background(), d, members, inventory); err == nil {
		t.Fatal("one Vitess claim was accepted for two tablets")
	}
}

func TestVerifyVitessStorageRejectsPaginationAndOverboundLists(t *testing.T) {
	_, _, _, one := vitessStorageFixture(t, 1)
	for _, test := range []struct {
		name string
		list *corev1.PersistentVolumeClaimList
	}{
		{name: "pagination", list: &corev1.PersistentVolumeClaimList{ListMeta: metav1.ListMeta{Continue: "next"}}},
		{name: "overbound", list: func() *corev1.PersistentVolumeClaimList {
			items := make([]corev1.PersistentVolumeClaim, database.MaxMembers+1)
			return &corev1.PersistentVolumeClaimList{Items: items}
		}()},
		{name: "duplicate-claims", list: &corev1.PersistentVolumeClaimList{Items: []corev1.PersistentVolumeClaim{*one[0].DeepCopy(), *one[0].DeepCopy()}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, members, inventory, claims := vitessStorageFixture(t, 1)
			kube := vitessStorageClient(claims)
			kube.Fake.PrependReactor("list", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) { return true, test.list, nil })
			if err := (&Client{kube: kube}).verifyVitessStorage(context.Background(), d, members, inventory); err == nil {
				t.Fatal("unbounded Vitess claim list was accepted")
			}
		})
	}
}

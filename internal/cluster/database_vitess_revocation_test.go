package cluster

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func vitessRevocationFixture(t *testing.T, processes bool) (*Client, database.Resource, VitessBackupStorage) {
	t.Helper()
	d := vitessTestDatabase()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace", Labels: databaseLabels(d)}}
	storage := VitessBackupStorage{DatabaseID: d.ID, Dedicated: true, Destination: backup.Destination{ID: d.Spec.Vitess.BackupDestinationID, Revision: 1, Project: d.Project, Environment: d.Environment, Name: "native-storage", Endpoint: "https://storage.example.com", Region: "development", Bucket: "vitess-development", Prefix: "development"}, Credentials: backup.Credentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"}, ApprovedEndpointCIDRs: []netip.Prefix{netip.MustParsePrefix("203.0.113.8/32"), netip.MustParsePrefix("203.0.113.9/32")}}
	root, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	root.SetUID("database-root")
	if err = applyVitessBackupStorage(root, d, storage); err != nil {
		t.Fatal(err)
	}
	policy, err := vitessBackupNetworkPolicy(d, ns.UID, storage)
	if err != nil {
		t.Fatal(err)
	}
	policy.UID = "native-policy"
	secret := &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-vitess-native-backup"), Data: map[string][]byte{"credentials": []byte("[default]\naws_access_key_id = fixture-key\naws_secret_access_key = fixture-secret\n")}}
	secret.UID = "native-secret"
	secret.Annotations = map[string]string{"hakopod.io/destination-id": storage.Destination.ID, "hakopod.io/destination-revision": "1"}
	operatorObject := vitessOperatorObject(d, ns.UID)
	operatorObject.SetUID("namespace-operator")
	operator := &appsv1.Deployment{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(operatorObject.Object, operator); err != nil {
		t.Fatal(err)
	}
	owner := func(kind, name string, uid types.UID) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: kind, Name: name, UID: uid, Controller: ptr(true)}}
	}
	schedule := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessBackupSchedule", "metadata": map[string]any{"name": "schedule", "namespace": ns.Name, "uid": "schedule-uid", "labels": map[string]any{"planetscale.com/cluster": "database"}}, "spec": map[string]any{}}}
	schedule.SetOwnerReferences(owner("VitessCluster", "database", root.GetUID()))
	vbs := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessBackupStorage", "metadata": map[string]any{"name": "storage", "namespace": ns.Name, "uid": "storage-uid"}}}
	vbs.SetOwnerReferences(owner("VitessCluster", "database", root.GetUID()))
	objects := []runtime.Object{ns, operator, secret, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "user-data-tablet", Namespace: ns.Name, UID: "data-pod", Labels: map[string]string{vitessComponentLabel: "tablet", "planetscale.com/component": "vttablet"}}}, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "user-data", Namespace: ns.Name, UID: "data-claim"}}}
	dynamicObjects := []runtime.Object{root, schedule, vbs}
	for _, entry := range []struct {
		object        any
		version, kind string
	}{{policy, "networking.k8s.io/v1", "NetworkPolicy"}, {secret, "v1", "Secret"}} {
		raw, e := runtime.DefaultUnstructuredConverter.ToUnstructured(entry.object)
		if e != nil {
			t.Fatal(e)
		}
		raw["apiVersion"], raw["kind"] = entry.version, entry.kind
		dynamicObjects = append(dynamicObjects, &unstructured.Unstructured{Object: raw})
	}
	if processes {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "native-backup", Namespace: ns.Name, UID: "job-uid", Labels: map[string]string{"planetscale.com/cluster": "database", "planetscale.com/component": "vtbackup"}, OwnerReferences: owner("VitessBackupSchedule", schedule.GetName(), schedule.GetUID())}}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "native-backup-pod", Namespace: ns.Name, UID: "backup-pod", Labels: job.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}}}
		sub := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "backup-storage-controller", Namespace: ns.Name, UID: "backup-subcontroller", Labels: map[string]string{"planetscale.com/component": "vbs-subcontroller"}, OwnerReferences: owner("VitessBackupStorage", vbs.GetName(), vbs.GetUID())}}
		objects = append(objects, job, pod, sub)
		raw, e := runtime.DefaultUnstructuredConverter.ToUnstructured(job)
		if e != nil {
			t.Fatal(e)
		}
		raw["apiVersion"], raw["kind"] = "batch/v1", "Job"
		dynamicObjects = append(dynamicObjects, &unstructured.Unstructured{Object: raw})
	}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "planetscale.com", Version: "v2", Resource: "vitessbackupschedules"}: "VitessBackupScheduleList"}, dynamicObjects...)
	return &Client{kube: kubefake.NewClientset(objects...), dynamic: dynamic}, d, storage
}

func TestVitessBackupRevocationStopsNativeWorkAndPreservesData(t *testing.T) {
	c, d, _ := vitessRevocationFixture(t, true)
	ctx := context.Background()
	err := c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil })
	if !errors.Is(err, VitessBackupRevocationPending) {
		t.Fatal("active backup cleanup did not remain retryable", err)
	}
	ns := DatabaseNamespace(d.ID)
	root, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := root.Object["spec"].(map[string]any)["backup"]; !ok || root.GetAnnotations()[vitessBackupRevoked] != "true" {
		t.Fatal("revocation changed tablet configuration before native processes exited")
	}
	for _, target := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{{schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "database-vitess-native-backup"}, {schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, "database-vitess-backup-egress"}} {
		if _, err = c.dynamic.Resource(target.gvr).Namespace(ns).Get(ctx, target.name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("native backup access was not removed", err)
		}
	}
	operator, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil || operator.Spec.Replicas == nil || *operator.Spec.Replicas != 0 {
		t.Fatal("namespace controller can recreate revoked work", err)
	}
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatal("active native backup Jobs remain", err)
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 || pods.Items[0].Name != "user-data-tablet" {
		t.Fatal("revocation changed user data pods or left backup processes", err)
	}
	if _, err = c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "user-data", metav1.GetOptions{}); err != nil {
		t.Fatal("revocation removed user data", err)
	}
	err = c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil })
	if err == nil || errors.Is(err, VitessBackupRevocationPending) {
		t.Fatal("revocation did not finish cleanup while preserving refusal", err)
	}
	root, err = c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := root.Object["spec"].(map[string]any)["backup"]; ok || root.GetAnnotations()[vitessBackupRevoked] != "true" {
		t.Fatal("completed revocation retained native backup desired authority")
	}
}

func TestVitessBackupRevocationQuiescesControllerBeforeChangingTabletConfig(t *testing.T) {
	c, d, storage := vitessRevocationFixture(t, false)
	ctx := context.Background()
	ns := DatabaseNamespace(d.ID)
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns)
	original, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	originalBackup, _, _ := unstructured.NestedMap(original.Object, "spec", "backup")
	operator, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	operator.Generation = 7
	operator.Status = appsv1.DeploymentStatus{ObservedGeneration: 7, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1}
	if _, err = c.kube.AppsV1().Deployments(ns).Update(ctx, operator, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.revokeVitessBackupAuthority(ctx, d, func() error { return nil }); !errors.Is(err, VitessBackupRevocationPending) {
		t.Fatal("unobserved controller shutdown did not remain pending", err)
	}
	assertUnchangedBackup := func() {
		t.Helper()
		root, err := api.Get(ctx, "database", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		backup, _, _ := unstructured.NestedMap(root.Object, "spec", "backup")
		if !reflect.DeepEqual(backup, originalBackup) || root.GetAnnotations()[vitessBackupRevoked] != "true" {
			t.Fatal("pending revocation changed desired tablet backup configuration")
		}
	}
	assertUnchangedBackup()
	for _, target := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, "database-vitess-backup-egress"},
		{schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "database-vitess-native-backup"},
	} {
		if _, err = c.dynamic.Resource(target.gvr).Namespace(ns).Get(ctx, target.name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("pending shutdown retained native backup access", err)
		}
	}
	schedule, err := c.dynamic.Resource(schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackupschedules"}).Namespace(ns).Get(ctx, "schedule", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if suspended, _, _ := unstructured.NestedBool(schedule.Object, "spec", "suspend"); !suspended {
		t.Fatal("pending shutdown retained its active backup schedule")
	}
	c.options.VitessBackup = func(context.Context, database.Resource) (VitessBackupStorage, error) { return storage, nil }
	if err = c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); !errors.Is(err, VitessBackupRevocationPending) {
		t.Fatal("reapproval bypassed unfinished controller shutdown", err)
	}
	assertUnchangedBackup()
	operator, err = c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	operator.Status = appsv1.DeploymentStatus{ObservedGeneration: operator.Generation - 1}
	if _, err = c.kube.AppsV1().Deployments(ns).UpdateStatus(ctx, operator, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); !errors.Is(err, VitessBackupRevocationPending) {
		t.Fatal("stale zero-replica status allowed reapproval", err)
	}
	assertUnchangedBackup()
	operator.Status.ObservedGeneration = operator.Generation
	if _, err = c.kube.AppsV1().Deployments(ns).UpdateStatus(ctx, operator, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); err != nil {
		t.Fatal("reapproval could not resume after observed shutdown", err)
	}
	root, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backup, _, _ := unstructured.NestedMap(root.Object, "spec", "backup")
	if !reflect.DeepEqual(backup, originalBackup) || root.GetAnnotations()[vitessBackupRevoked] != "" {
		t.Fatal("reapproval did not restore the original backup configuration")
	}
}

func TestVitessBackupRevocationRejectsControllerReplacementDuringShutdown(t *testing.T) {
	for _, target := range []string{"operator", "database"} {
		t.Run(target, func(t *testing.T) {
			c, d, _ := vitessRevocationFixture(t, false)
			ctx := context.Background()
			ns := DatabaseNamespace(d.ID)
			gets := 0
			if target == "operator" {
				c.kube.(*kubefake.Clientset).PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
					gets++
					if gets != 2 {
						return false, nil, nil
					}
					object, err := c.kube.(*kubefake.Clientset).Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), ns, "vitess-operator")
					if err != nil {
						return true, nil, err
					}
					operator := object.(*appsv1.Deployment).DeepCopy()
					operator.UID = "replacement-operator"
					return true, operator, nil
				})
			} else {
				shutdownObserved := false
				c.kube.(*kubefake.Clientset).PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
					gets++
					shutdownObserved = gets >= 2
					return false, nil, nil
				})
				c.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "vitessclusters", func(action ktesting.Action) (bool, runtime.Object, error) {
					if !shutdownObserved {
						return false, nil, nil
					}
					shutdownObserved = false
					object, err := c.dynamic.(*dynamicfake.FakeDynamicClient).Tracker().Get(vitessDatabaseResource, ns, "database")
					if err != nil {
						return true, nil, err
					}
					root := object.(*unstructured.Unstructured).DeepCopy()
					root.SetUID("replacement-database")
					return true, root, nil
				})
			}
			if err := c.revokeVitessBackupAuthority(ctx, d, func() error { return nil }); err == nil || errors.Is(err, VitessBackupRevocationPending) {
				t.Fatal("replacement controller did not stop revocation", err)
			}
			root, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, found, _ := unstructured.NestedMap(root.Object, "spec", "backup"); !found {
				t.Fatal("revocation changed tablet configuration after controller replacement")
			}
		})
	}
}

func TestVitessBackupRevocationHonorsFence(t *testing.T) {
	c, d, _ := vitessRevocationFixture(t, true)
	fence := errors.New("durable maintenance lease ended")
	if err := c.ReconcileVitessBackupAuthority(context.Background(), d, func() error { return fence }); !errors.Is(err, fence) {
		t.Fatal("lost fence was ignored", err)
	}
	for _, client := range []interface{ Actions() []ktesting.Action }{c.kube.(*kubefake.Clientset), c.dynamic.(*dynamicfake.FakeDynamicClient)} {
		for _, action := range client.Actions() {
			if action.GetVerb() == "update" || action.GetVerb() == "delete" || action.GetVerb() == "create" || action.GetVerb() == "patch" {
				t.Fatal("mutation crossed an expired maintenance fence")
			}
		}
	}
}

func TestVitessBackupRevocationWaitsForOrphansBeforeRegrant(t *testing.T) {
	for _, missing := range []struct {
		resource schema.GroupVersionResource
		name     string
	}{
		{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "native-backup"},
		{schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackupschedules"}, "schedule"},
		{schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackupstorages"}, "storage"},
	} {
		t.Run(missing.resource.Resource, func(t *testing.T) {
			c, d, storage := vitessRevocationFixture(t, true)
			ctx := context.Background()
			ns := DatabaseNamespace(d.ID)
			if err := c.dynamic.Resource(missing.resource).Namespace(ns).Delete(ctx, missing.name, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); err == nil {
				t.Fatal("missing process owner did not block cleanup")
			}
			c.options.VitessBackup = func(context.Context, database.Resource) (VitessBackupStorage, error) { return storage, nil }
			if err := c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); err == nil {
				t.Fatal("approval reopened access while orphaned processes remain")
			}
			root, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := root.Object["spec"].(map[string]any)["backup"]; !ok || root.GetAnnotations()[vitessBackupRevoked] != "true" {
				t.Fatal("orphan cleanup changed tablet configuration or lost the revocation marker")
			}
			if _, err = c.dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(ns).Get(ctx, "database-vitess-native-backup", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatal("regrant restored storage credentials before orphan cleanup", err)
			}
			operator, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
			if err != nil || operator.Spec.Replicas == nil || *operator.Spec.Replicas != 0 {
				t.Fatal("regrant resumed native backup controller before orphan cleanup", err)
			}
			// Garbage collection can remove descendants after their parent disappears.
			// Until it does, the missing chain is not authority to delete or restart them.
			for _, name := range []string{"native-backup-pod", "backup-storage-controller"} {
				if err = c.kube.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
					t.Fatal(err)
				}
			}
			if err = c.kube.BatchV1().Jobs(ns).Delete(ctx, "native-backup", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
			if err = c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); err != nil {
				t.Fatal("approval did not recover after orphan cleanup completed", err)
			}
		})
	}
}

func TestVitessBackupAddressShrinkRevokesBeforeRestoring(t *testing.T) {
	c, d, storage := vitessRevocationFixture(t, false)
	storage.ApprovedEndpointCIDRs = storage.ApprovedEndpointCIDRs[:1]
	c.options.VitessBackup = func(context.Context, database.Resource) (VitessBackupStorage, error) { return storage, nil }
	ctx := context.Background()
	if err := c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); !errors.Is(err, VitessBackupRevocationPending) {
		t.Fatal("address shrink did not revoke old authority first", err)
	}
	if err := c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); err != nil {
		t.Fatal("approved authority could not be restored", err)
	}
	policy, err := c.dynamic.Resource(schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database-vitess-backup-egress", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rules, _, _ := unstructured.NestedSlice(policy.Object, "spec", "egress")
	peers := rules[0].(map[string]any)["to"].([]any)
	if len(peers) != 1 || peers[0].(map[string]any)["ipBlock"].(map[string]any)["cidr"] != "203.0.113.8/32" {
		t.Fatal("old approved address remains reachable")
	}
}

func TestVitessDeletionKeepsRevokedOperatorPaused(t *testing.T) {
	c, d, _ := vitessRevocationFixture(t, false)
	ctx := context.Background()
	if err := c.ReconcileVitessBackupAuthority(ctx, d, func() error { return nil }); err == nil {
		t.Fatal("fixture did not revoke backup authority")
	}
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	root, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.pauseVitessDeletionController(ctx, d, root.GetUID(), func() error { return nil }); err == nil {
		t.Fatal("namespace operator changed before root deletion")
	}
	stamp := metav1.NewTime(time.Now())
	root.SetDeletionTimestamp(&stamp)
	if _, err = api.Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err := c.deleteVitessController(ctx, d, func() error { return nil })
	if err != nil || done {
		t.Fatal("pending garbage collection was not preserved", err)
	}
	operator, err := c.kube.AppsV1().Deployments(DatabaseNamespace(d.ID)).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil || operator.Spec.Replicas == nil || *operator.Spec.Replicas != 0 {
		t.Fatal("namespace operator resumed during deletion", err)
	}
	root, err = api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := root.Object["spec"].(map[string]any)["backup"]; ok {
		t.Fatal("deletion restored backup desired authority")
	}
	if _, err = c.dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database-vitess-native-backup", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("deletion restored revoked credentials", err)
	}
}

func TestVitessDeletionPausesActiveOperatorWithoutChangingFinalizers(t *testing.T) {
	c, d, _ := vitessRevocationFixture(t, false)
	ctx := context.Background()
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	root, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stamp := metav1.NewTime(time.Now())
	root.SetDeletionTimestamp(&stamp)
	root.SetFinalizers([]string{"foregroundDeletion"})
	if _, err = api.Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	leaseExpired := errors.New("fixture lease expired")
	if err = c.pauseVitessDeletionController(ctx, d, root.GetUID(), func() error { return leaseExpired }); !errors.Is(err, leaseExpired) {
		t.Fatal("deletion changed the operator without its operation lease", err)
	}
	operator, err := c.kube.AppsV1().Deployments(DatabaseNamespace(d.ID)).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil || operator.Spec.Replicas == nil || *operator.Spec.Replicas != 1 {
		t.Fatal("expired deletion lease changed the active operator", err)
	}
	done, err := c.deleteVitessController(ctx, d, func() error { return nil })
	if err != nil || done {
		t.Fatal("deletion did not wait for ordinary garbage collection", err)
	}
	operator, err = c.kube.AppsV1().Deployments(DatabaseNamespace(d.ID)).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil || operator.Spec.Replicas == nil || *operator.Spec.Replicas != 0 {
		t.Fatal("operator can recreate children under a deleting database", err)
	}
	root, err = api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil || len(root.GetFinalizers()) != 1 || root.GetFinalizers()[0] != "foregroundDeletion" {
		t.Fatal("deletion changed a Kubernetes finalizer", err)
	}
}

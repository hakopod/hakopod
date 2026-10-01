package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestPostgresDeletionStopsControllerAndRetainsVolumeReservation(t *testing.T) {
	d := database.Resource{ID: strings.Repeat("a", 32), Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "postgres-delete", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "512Mi", StorageGiB: 1}}
	for _, state := range []string{"owned", "foreign", "finalizing", "fenced"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			object, err := DatabaseObject(d)
			if err != nil {
				t.Fatal(err)
			}
			object.SetUID("postgres-controller")
			if state == "foreign" {
				object.SetLabels(map[string]string{databaseOwner: "other", managedBy: "hakopod"})
			}
			if state == "finalizing" {
				now := metav1.Now()
				object.SetDeletionTimestamp(&now)
			}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "postgres-namespace", Labels: databaseLabels(d)}}
			claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "database-1", Namespace: ns.Name, UID: "postgres-claim"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "postgres-volume"}}
			pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.VolumeName}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: ns.Name, Name: claim.Name, UID: claim.UID}}}
			c := &Client{kube: kubefake.NewClientset(ns, claim, pv), dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme(), object)}
			writes := 0
			before := func() error {
				writes++
				if state == "fenced" {
					return context.Canceled
				}
				return nil
			}
			done, err := c.DeleteDatabase(ctx, d, before)
			if done || (err != nil) != (state == "foreign" || state == "fenced") {
				t.Fatal("unexpected deletion result", done, err)
			}
			if _, err = c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{}); err != nil {
				t.Fatal("namespace removed before PostgreSQL controller collection", err)
			}
			if state != "owned" {
				if _, err = c.dynamic.Resource(pgDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{}); err != nil {
					t.Fatal("controller deleted despite ownership, finalization or operation fence", err)
				}
				current, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, pv.Name, metav1.GetOptions{})
				if err != nil || current.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
					t.Fatal("volume changed without accepted deletion authority", err)
				}
				return
			}
			if writes != 2 {
				t.Fatal("volume and controller mutations must each recheck authority", writes)
			}
			if _, err = c.dynamic.Resource(pgDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatal("owned PostgreSQL controller was not deleted", err)
			}
			actions := c.dynamic.(*fake.FakeDynamicClient).Actions()
			options := actions[len(actions)-2].(interface{ GetDeleteOptions() metav1.DeleteOptions }).GetDeleteOptions()
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != object.GetUID() || options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground {
				t.Fatal("PostgreSQL controller deletion lacks UID or foreground collection", options)
			}
			if done, err = c.DeleteDatabase(ctx, d, before); err != nil || done {
				t.Fatal("namespace deletion failed after controller collection", done, err)
			}
			if done, err = c.DeleteDatabase(ctx, d, before); err != nil || done {
				t.Fatal("capacity released before PostgreSQL volume reclamation", done, err)
			}
			if err = c.kube.CoreV1().PersistentVolumes().Delete(ctx, pv.Name, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			if done, err = c.DeleteDatabase(ctx, d, before); err != nil || !done {
				t.Fatal("reclaimed PostgreSQL deletion did not complete", done, err)
			}
		})
	}
}

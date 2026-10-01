package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestManagedVitessBackupRevocationLive(t *testing.T) {
	fixtures, ctx := newVitessLiveClient(t, 25*time.Minute)
	c := fixtures.c
	d, password := newVitessFixture(t, ctx, fixtures, "revocation", 1)
	health := waitVitessFixture(t, ctx, c, d, password)
	client := vitessFixtureClient(t, ctx, c, d, health, password, "app@primary", 0)
	seedVitessFixture(t, ctx, client)
	original := map[string]string{}
	for _, member := range health.Members {
		original[member.Name] = member.UID
	}
	revoke := func() {
		t.Helper()
		fixtures.revoked[d.ID] = true
		wait, stop := context.WithTimeout(ctx, 3*time.Minute)
		defer stop()
		for {
			// The resolver continues to report refusal after cleanup completes.
			// Verify the resulting resource/process boundary directly.
			_ = c.ReconcileVitessBackupAuthority(wait, d, func() error { return wait.Err() })
			if vitessFixtureRevocationComplete(t, wait, c, d) {
				break
			}
			if sleepContext(wait, 2*time.Second) != nil {
				t.Fatal("Vitess native backup revocation did not finish")
			}
		}
		for name, uid := range original {
			pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
			if err != nil || string(pod.UID) != uid || pod.DeletionTimestamp != nil {
				t.Fatal("Vitess storage revocation replaced a database tablet")
			}
		}
		checkVitessFixtureData(t, ctx, client)
	}
	revoke()
	fixtures.revoked[d.ID] = false
	wait, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	for {
		err := c.ReconcileVitessBackupAuthority(wait, d, func() error { return wait.Err() })
		if err == nil {
			break
		}
		if sleepContext(wait, 2*time.Second) != nil {
			t.Fatal("Vitess approved storage did not resume", err)
		}
	}
	health = waitVitessFixture(t, wait, c, d, password)
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	schedules, found, err := unstructured.NestedSlice(object.Object, "spec", "backup", "schedules")
	if err != nil || !found || len(schedules) != 1 || object.GetAnnotations()[vitessBackupRevoked] != "" {
		t.Fatal("Vitess storage reapproval did not restore its backup schedule")
	}
	if _, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-vitess-native-backup", metav1.GetOptions{}); err != nil {
		t.Fatal("Vitess storage reapproval did not restore its owned credential reference")
	}
	if _, err := c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(d.ID)).Get(ctx, "database-vitess-backup-egress", metav1.GetOptions{}); err != nil {
		t.Fatal("Vitess storage reapproval did not restore approved egress")
	}
	client = vitessFixtureClient(t, ctx, c, d, health, password, "app@primary", 0)
	checkVitessFixtureData(t, ctx, client)
	revoke()
	// Fixture cleanup must finish deletion with approval still absent. The
	// namespace operator may resume only to finish the owned native finalizers.
	t.Log("Vitess native backup revocation removed new-backup authority, retained tablet data, restored the approved schedule and enters deletion with approval revoked")
}

func vitessFixtureRevocationComplete(t *testing.T, ctx context.Context, c *Client, d database.Resource) bool {
	t.Helper()
	ns := DatabaseNamespace(d.ID)
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if object.GetAnnotations()[vitessBackupRevoked] != "true" {
		return false
	}
	if _, found, _ := unstructured.NestedMap(object.Object, "spec", "backup"); found {
		return false
	}
	operator, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if operator.Spec.Replicas == nil || *operator.Spec.Replicas != 0 || operator.Status.Replicas != 0 {
		return false
	}
	if _, err := c.kube.CoreV1().Secrets(ns).Get(ctx, "database-vitess-native-backup", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return false
	}
	if _, err := c.kube.NetworkingV1().NetworkPolicies(ns).Get(ctx, "database-vitess-backup-egress", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return false
	}
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: "planetscale.com/cluster=database,planetscale.com/component=vtbackup", Limit: 17})
	if err != nil || jobs.Continue != "" || len(jobs.Items) > 16 {
		t.Fatal("Vitess native backup Job inventory exceeded its bound")
	}
	if len(jobs.Items) != 0 {
		return false
	}
	schedules, err := c.dynamic.Resource(schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackupschedules"}).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "planetscale.com/cluster=database", Limit: 9})
	if err != nil || schedules.GetContinue() != "" || len(schedules.Items) > 8 {
		t.Fatal("Vitess native backup schedule inventory exceeded its bound")
	}
	for _, schedule := range schedules.Items {
		suspended, _, _ := unstructured.NestedBool(schedule.Object, "spec", "suspend")
		if !suspended && schedule.GetDeletionTimestamp() == nil {
			return false
		}
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 64})
	if err != nil || pods.Continue != "" {
		t.Fatal("Vitess native backup cleanup inventory exceeded its bound")
	}
	for _, pod := range pods.Items {
		component := pod.Labels["planetscale.com/component"]
		if component == "vtbackup" || component == "vbs-subcontroller" || pod.Labels[vitessComponentLabel] == "operator" {
			return false
		}
	}
	return true
}

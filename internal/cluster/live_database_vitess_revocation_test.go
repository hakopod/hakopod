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
	original := vitessFixtureTabletIdentities(t, ctx, c, d, health)
	revoke := func(phase string) {
		t.Helper()
		before := vitessFixtureTabletIdentities(t, ctx, c, d, health)
		t.Logf("Vitess %s starts at %s with %d tablets", phase, time.Now().UTC().Format(time.RFC3339Nano), len(before))
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
		checkVitessFixtureTabletIdentities(t, ctx, c, d, before, phase)
		checkVitessFixtureData(t, ctx, client)
		t.Logf("Vitess %s preserved tablet identities at %s", phase, time.Now().UTC().Format(time.RFC3339Nano))
	}
	revoke("first storage revocation")
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
	checkVitessFixtureTabletIdentities(t, ctx, c, d, original, "storage reapproval")
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
	revoke("second storage revocation")
	// Fixture cleanup must finish deletion with approval still absent. The
	// namespace operator may resume only to finish the owned native finalizers.
	t.Log("Vitess native backup revocation removed new-backup authority, retained tablet data, restored the approved schedule and enters deletion with approval revoked")
}

func vitessFixtureTabletIdentities(t *testing.T, ctx context.Context, c *Client, d database.Resource, health database.Observation) map[string]string {
	t.Helper()
	if len(health.Members) == 0 || len(health.Members) > database.MaxMembers {
		t.Fatal("Vitess tablet identity inventory is empty or exceeds its bound")
	}
	identities := make(map[string]string, len(health.Members))
	for _, member := range health.Members {
		if member.Name == "" || member.UID == "" || identities[member.Name] != "" {
			t.Fatal("Vitess tablet identity inventory is incomplete or duplicated")
		}
		identities[member.Name] = member.UID
	}
	checkVitessFixtureTabletIdentities(t, ctx, c, d, identities, "revocation baseline")
	return identities
}

func checkVitessFixtureTabletIdentities(t *testing.T, ctx context.Context, c *Client, d database.Resource, identities map[string]string, phase string) {
	t.Helper()
	for name, uid := range identities {
		pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Vitess %s lost tablet %q: %v", phase, name, err)
		}
		if string(pod.UID) != uid || pod.DeletionTimestamp != nil {
			t.Fatalf("Vitess %s replaced tablet %q: expected UID %s, observed UID %s, deleting=%t", phase, name, uid, pod.UID, pod.DeletionTimestamp != nil)
		}
	}
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

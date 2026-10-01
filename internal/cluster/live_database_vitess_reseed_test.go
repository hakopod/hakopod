package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func vitessFixtureNativeCommand(t *testing.T, ctx context.Context, c *Client, d database.Resource, args ...string) string {
	t.Helper()
	control, err := c.vitessControlMember(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	command := vitessControlCommand(d, args...)
	command[2] = "--action-timeout=12m"
	out := &databaseBoundedWriter{limit: 128 << 10}
	if err := c.DatabaseExec(ctx, d, control, command, nil, out); err != nil {
		t.Fatal("Vitess native fixture command failed", args[0], err)
	}
	return out.String()
}

func vitessFixtureReplicaAlias(t *testing.T, ctx context.Context, c *Client, d database.Resource, member database.Member) string {
	t.Helper()
	var tablets []struct {
		Alias struct {
			Cell string `json:"cell"`
			UID  uint32 `json:"uid"`
		} `json:"alias"`
		Hostname string `json:"hostname"`
		Keyspace string `json:"keyspace"`
		Shard    string `json:"shard"`
		Type     string `json:"type"`
	}
	value := vitessFixtureNativeCommand(t, ctx, c, d, "GetTablets", "--strict", "--format=json", "--keyspace=app", "--shard="+member.Shard)
	if json.Unmarshal([]byte(value), &tablets) != nil || len(tablets) != 1+d.Spec.Replicas {
		t.Fatal("Vitess native tablet identity inventory is incomplete")
	}
	alias := ""
	for _, tablet := range tablets {
		if tablet.Hostname == member.Name+"."+DatabaseNamespace(d.ID)+".svc.cluster.local" {
			if alias != "" || tablet.Keyspace != "app" || tablet.Shard != member.Shard || tablet.Type != "REPLICA" || tablet.Alias.Cell != "local" || tablet.Alias.UID == 0 {
				t.Fatal("Vitess selected tablet is not the observed replica")
			}
			alias = fmt.Sprintf("%s-%010d", tablet.Alias.Cell, tablet.Alias.UID)
		}
	}
	if alias == "" {
		t.Fatal("Vitess could not bind the observed replica to a native alias")
	}
	return alias
}

func vitessFixtureTabletVolume(t *testing.T, ctx context.Context, c *Client, d database.Resource, member database.Member) (corev1.PersistentVolumeClaim, corev1.PersistentVolume) {
	t.Helper()
	ns := DatabaseNamespace(d.ID)
	root, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || root.GetUID() == "" || root.GetLabels()[databaseOwner] != d.ID || root.GetLabels()[managedBy] != "hakopod" {
		t.Fatal("Vitess reseed controller ownership changed", err)
	}
	pod, err := c.kube.CoreV1().Pods(ns).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || string(pod.UID) != member.UID || pod.DeletionTimestamp != nil || !c.vitessPodOwned(ctx, *pod, root.GetUID()) {
		t.Fatal("Vitess reseed tablet ownership changed", err)
	}
	mounted := map[string]bool{}
	for _, container := range pod.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			if mount.MountPath == "/vt/vtdataroot" {
				mounted[mount.Name] = true
			}
		}
	}
	claims := []string{}
	for _, volume := range pod.Spec.Volumes {
		if mounted[volume.Name] && volume.PersistentVolumeClaim != nil {
			claims = append(claims, volume.PersistentVolumeClaim.ClaimName)
		}
	}
	if len(claims) != 1 || claims[0] != pod.Name {
		t.Fatal("Vitess reseed could not identify one owned tablet data claim")
	}
	claim, err := c.kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claims[0], metav1.GetOptions{})
	if err != nil || claim.UID == "" || claim.DeletionTimestamp != nil || claim.Spec.VolumeName == "" || claim.Labels[databaseOwner] != d.ID || claim.Labels[managedBy] != "hakopod" || !c.vitessOwnedChain(ctx, ns, claim.UID, claim.OwnerReferences, root.GetUID()) {
		t.Fatal("Vitess reseed data claim ownership changed", err)
	}
	volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
	if err != nil || volume.UID == "" || volume.DeletionTimestamp != nil || volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.Namespace != ns || volume.Spec.ClaimRef.Name != claim.Name || volume.Spec.ClaimRef.UID != claim.UID || volume.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
		t.Fatal("Vitess reseed volume is not a disposable owned fixture volume", err)
	}
	return *claim, *volume
}

func vitessFixtureSetOperatorReplicas(t *testing.T, ctx context.Context, c *Client, d database.Resource, replicas int32) {
	t.Helper()
	ns := DatabaseNamespace(d.ID)
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil || namespace.UID == "" || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" {
		t.Fatal("Vitess reseed namespace ownership changed", err)
	}
	operator, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil || !vitessNamespaceObjectOwned(operator, d, namespace.UID) || operator.DeletionTimestamp != nil {
		t.Fatal("Vitess reseed operator ownership changed", err)
	}
	if operator.Spec.Replicas == nil || *operator.Spec.Replicas != replicas {
		operator = operator.DeepCopy()
		operator.Spec.Replicas = ptr(replicas)
		if _, err = c.kube.AppsV1().Deployments(ns).Update(ctx, operator, metav1.UpdateOptions{}); err != nil {
			t.Fatal("Vitess reseed could not change the owned namespace operator", err)
		}
	}
	for ctx.Err() == nil {
		operator, err = c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
		pods, listErr := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=operator", Limit: 2})
		if err == nil && listErr == nil && pods.Continue == "" && len(pods.Items) <= 1 && operator.Spec.Replicas != nil && *operator.Spec.Replicas == replicas && operator.Status.ObservedGeneration >= operator.Generation {
			if replicas == 0 && operator.Status.Replicas == 0 && len(pods.Items) == 0 {
				return
			}
			if replicas == 1 && operator.Status.ReadyReplicas == 1 && operator.Status.AvailableReplicas == 1 && len(pods.Items) == 1 && c.vitessOperatorPodOwned(ctx, pods.Items[0].Name, pods.Items[0].UID, pods.Items[0].OwnerReferences, d, namespace.UID) {
				return
			}
		}
		if sleepContext(ctx, time.Second) != nil {
			break
		}
	}
	t.Fatal("Vitess reseed namespace operator did not reach its requested replica count", err)
}

func TestManagedVitessNativeReseedLive(t *testing.T) {
	fixtures, ctx := newVitessLiveClient(t, 45*time.Minute)
	c := fixtures.c
	d, password := newVitessFixture(t, ctx, fixtures, "reseed", 1, 2)
	health := waitVitessFixture(t, ctx, c, d, password)
	client := vitessFixtureClient(t, ctx, c, d, health, password, "app@primary", 0)
	seedVitessFixture(t, ctx, client)
	primaries, err := vitessObservedPrimaries(d, health)
	if err != nil {
		t.Fatal(err)
	}
	primary := primaries[0]
	var victim database.Member
	for _, member := range health.Members {
		if member.Role == "replica" {
			victim = member
			break
		}
	}
	if victim.Name == "" || victim.UID == "" {
		t.Fatal("Vitess reseed could not select an observed replica")
	}
	alias := vitessFixtureReplicaAlias(t, ctx, c, d, victim)
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=orchestrator", Limit: 2})
	if err != nil || len(pods.Items) != 1 || pods.Continue != "" {
		t.Fatal("Vitess reseed requires one verified native orchestrator")
	}
	controller := database.Member{Name: pods.Items[0].Name, UID: string(pods.Items[0].UID), Role: "orchestrator", Ready: true}
	setRecoveries := func(step context.Context, enabled bool) error {
		action, want := "disable", "Global recoveries disabled"
		if enabled {
			action, want = "enable", "Global recoveries enabled"
		}
		out := &databaseBoundedWriter{limit: 1024}
		err := c.DatabaseExec(step, d, controller, []string{"curl", "--fail", "--silent", "--show-error", "--max-time", "5", "http://127.0.0.1:15000/api/" + action + "-global-recoveries"}, nil, out)
		if err != nil || strings.TrimSpace(out.String()) != want {
			return fmt.Errorf("Vitess fixture recovery switch could not be verified")
		}
		return nil
	}
	if err := setRecoveries(ctx, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if err := setRecoveries(cleanup, true); err != nil {
			t.Error(err)
		}
	})
	local := func(member database.Member, query string) string {
		t.Helper()
		out := &databaseBoundedWriter{limit: 16 << 10}
		if err := c.DatabaseExec(ctx, d, member, vitessLocalCommand("vt_dba", query), nil, out); err != nil {
			t.Fatal("Vitess reseed SQL fixture failed", err)
		}
		return strings.TrimSpace(out.String())
	}
	local(victim, "STOP REPLICA")
	executed := local(victim, "SELECT @@global.gtid_executed")
	if !regexp.MustCompile(`^[a-fA-F0-9:,\n-]+$`).MatchString(executed) {
		t.Fatal("Vitess replica GTID boundary was invalid")
	}
	if _, err := client.ExecContext(ctx, "INSERT INTO records VALUES (99,0xCAFE,'after-replica-stopped')"); err != nil {
		t.Fatal(err)
	}
	beforeBackups := vitessFixtureNativeCommand(t, ctx, c, d, "GetBackups", "--limit=10", "app/-")
	vitessFixtureNativeCommand(t, ctx, c, d, "BackupShard", "--concurrency=1", "app/-")
	afterBackups := vitessFixtureNativeCommand(t, ctx, c, d, "GetBackups", "--limit=10", "app/-")
	if strings.TrimSpace(afterBackups) == "" || beforeBackups == afterBackups {
		t.Fatal("Vitess native backup did not publish a new recovery copy")
	}
	local(primary, "FLUSH BINARY LOGS")
	logs := strings.Split(local(primary, "SHOW BINARY LOGS"), "\n")
	fields := strings.Fields(logs[len(logs)-1])
	if len(fields) < 2 || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(fields[0]) {
		t.Fatal("Vitess primary binary-log identity was invalid")
	}
	local(primary, "PURGE BINARY LOGS TO '"+fields[0]+"'")
	if local(primary, "SELECT GTID_SUBSET(@@global.gtid_purged,'"+executed+"')") != "0" {
		t.Fatal("Vitess fixture did not prove that the replica missed purged transactions")
	}
	if local(victim, "SELECT COUNT(*) FROM records WHERE id=99") != "0" {
		t.Fatal("Vitess fixture replica did not retain its intended lag")
	}
	// Confirm the same orchestrator process stayed disabled across the fault.
	if _, _, err := c.vitessExecTarget(ctx, d, controller); err != nil {
		t.Fatal("Vitess orchestrator changed during the controlled recovery fault")
	}
	claim, volume := vitessFixtureTabletVolume(t, ctx, c, d, victim)
	operatorPaused := false
	t.Cleanup(func() {
		if operatorPaused {
			cleanup, stop := context.WithTimeout(context.Background(), 3*time.Minute)
			defer stop()
			vitessFixtureSetOperatorReplicas(t, cleanup, c, d, 1)
		}
	})
	operatorPaused = true
	pause, stopPause := context.WithTimeout(ctx, 3*time.Minute)
	vitessFixtureSetOperatorReplicas(t, pause, c, d, 0)
	stopPause()
	zero := int64(0)
	podUID := types.UID(victim.UID)
	if err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Delete(ctx, victim.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &podUID}}); err != nil {
		t.Fatal("Vitess reseed could not remove the stopped owned tablet", err)
	}
	waitPod, stopPod := context.WithTimeout(ctx, 3*time.Minute)
	podGone := false
	for waitPod.Err() == nil {
		_, podErr := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(waitPod, victim.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(podErr) {
			podGone = true
			break
		}
		if podErr != nil {
			stopPod()
			t.Fatal("Vitess reseed could not verify stopped tablet removal", podErr)
		}
		if sleepContext(waitPod, time.Second) != nil {
			stopPod()
			t.Fatal("Vitess reseed stopped tablet did not terminate")
		}
	}
	stopPod()
	if !podGone {
		t.Fatal("Vitess reseed stopped tablet removal was not confirmed")
	}
	if err := c.kube.CoreV1().PersistentVolumeClaims(claim.Namespace).Delete(ctx, claim.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &claim.UID}}); err != nil {
		t.Fatal("Vitess reseed could not remove the stopped tablet data claim", err)
	}
	waitStorage, stopStorage := context.WithTimeout(ctx, 3*time.Minute)
	storageGone := false
	for waitStorage.Err() == nil {
		_, claimErr := c.kube.CoreV1().PersistentVolumeClaims(claim.Namespace).Get(waitStorage, claim.Name, metav1.GetOptions{})
		_, volumeErr := c.kube.CoreV1().PersistentVolumes().Get(waitStorage, volume.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(claimErr) && apierrors.IsNotFound(volumeErr) {
			storageGone = true
			break
		}
		if claimErr != nil && !apierrors.IsNotFound(claimErr) {
			stopStorage()
			t.Fatal("Vitess reseed could not verify old data claim removal", claimErr)
		}
		if volumeErr != nil && !apierrors.IsNotFound(volumeErr) {
			stopStorage()
			t.Fatal("Vitess reseed could not verify old PersistentVolume object removal", volumeErr)
		}
		if sleepContext(waitStorage, time.Second) != nil {
			stopStorage()
			t.Fatal("Vitess reseed old data claim or PersistentVolume object was not deleted")
		}
	}
	stopStorage()
	if !storageGone {
		t.Fatal("Vitess reseed old data claim or PersistentVolume object deletion was not confirmed")
	}
	resume, stopResume := context.WithTimeout(ctx, 3*time.Minute)
	vitessFixtureSetOperatorReplicas(t, resume, c, d, 1)
	stopResume()
	operatorPaused = false
	health = waitVitessFixture(t, ctx, c, d, password)
	replacementFound := false
	for _, member := range health.Members {
		if member.Name == victim.Name {
			if member.UID == victim.UID || vitessFixtureReplicaAlias(t, ctx, c, d, member) != alias {
				t.Fatal("Vitess reseed did not replace the selected tablet identity")
			}
			replacement, replacementVolume := vitessFixtureTabletVolume(t, ctx, c, d, member)
			if replacement.UID == claim.UID || replacementVolume.UID == volume.UID || replacement.Spec.VolumeName == volume.Name {
				t.Fatal("Vitess reseed reused the deleted tablet storage identity")
			}
			replacementFound = true
		}
		checkVitessTabletData(t, ctx, c, d, member, true)
	}
	if !replacementFound {
		t.Fatal("Vitess reseed did not recreate the selected tablet slot")
	}
	if _, _, err := c.vitessExecTarget(ctx, d, controller); err != nil {
		t.Fatal("Vitess orchestrator changed before replacement verification")
	}
	if err := setRecoveries(ctx, true); err != nil {
		t.Fatal(err)
	}
	health = waitVitessFixture(t, ctx, c, d, password)
	for _, member := range health.Members {
		checkVitessTabletData(t, ctx, c, d, member, true)
	}
	client = vitessFixtureClient(t, ctx, c, d, health, password, "app@primary", 0)
	if _, err := client.ExecContext(ctx, "DELETE FROM records WHERE id=99"); err != nil {
		t.Fatal(err)
	}
	checkVitessFixtureData(t, ctx, client)
	t.Log("Vitess replaced a stopped replica after its data claim and PersistentVolume object were deleted, then restored the same tablet slot from native backup after its missing transactions were purged from primary binary logs")
}

package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
)

const myduckColdAnnotation = "hakopod.io/myduck-cold-storage"
const myduckStorageBinary = "/usr/local/bin/hakopod-myduck-storage"

var myduckJobID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type myduckColdRecord struct {
	JobID        string    `json:"job_id"`
	Revision     int64     `json:"revision"`
	Restore      bool      `json:"restore"`
	NamespaceUID types.UID `json:"namespace_uid"`
	SetUID       types.UID `json:"set_uid"`
	ClaimUID     types.UID `json:"claim_uid"`
	VolumeUID    types.UID `json:"volume_uid"`
	MemberUID    types.UID `json:"member_uid"`
	Node         string    `json:"node"`
}

func myduckColdName(jobID string) string { return "myduck-storage-" + jobID }

func myduckReadColdRecord(set *appsv1.StatefulSet, d database.Resource, jobID string) (myduckColdRecord, error) {
	var record myduckColdRecord
	raw := set.Annotations[myduckColdAnnotation]
	if !myduckJobID.MatchString(jobID) || len(raw) == 0 || len(raw) > 2048 {
		return record, fmt.Errorf("MyDuck cold-storage authority is unavailable")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, fmt.Errorf("MyDuck cold-storage receipt is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return record, fmt.Errorf("MyDuck cold-storage receipt has trailing data")
	}
	if record.JobID != jobID || record.Revision != d.Revision || record.SetUID != set.UID || record.NamespaceUID == "" || record.ClaimUID == "" || record.VolumeUID == "" || record.MemberUID == "" || record.Node == "" {
		return record, fmt.Errorf("MyDuck cold-storage receipt no longer matches its resource")
	}
	return record, nil
}

func (c *Client) myduckColdSet(ctx context.Context, d database.Resource) (*appsv1.StatefulSet, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume, types.UID, error) {
	if d.Spec.Engine != "duckdb" || d.Spec.ValidateMyDuck() != nil || myduckServerImage == "" {
		return nil, nil, nil, "", fmt.Errorf("invalid MyDuck cold-storage resource")
	}
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || namespace.UID == "" || namespace.DeletionTimestamp != nil || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" {
		return nil, nil, nil, "", fmt.Errorf("MyDuck cold-storage namespace changed")
	}
	set, err := c.kube.AppsV1().StatefulSets(namespace.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || set.UID == "" || set.DeletionTimestamp != nil || set.Labels[databaseOwner] != d.ID || set.Labels[managedBy] != "hakopod" || set.Annotations["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) || len(set.Spec.VolumeClaimTemplates) != 1 || set.Spec.VolumeClaimTemplates[0].Name != "data" {
		return nil, nil, nil, "", fmt.Errorf("MyDuck cold-storage workload changed")
	}
	claim, err := c.kube.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, "data-database-0", metav1.GetOptions{})
	if err != nil || claim.UID == "" || claim.DeletionTimestamp != nil || claim.Labels[databaseOwner] != d.ID || claim.Labels[managedBy] != "hakopod" || claim.Status.Phase != corev1.ClaimBound || len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce || claim.Spec.VolumeName == "" {
		return nil, nil, nil, "", fmt.Errorf("MyDuck cold-storage claim changed")
	}
	volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
	if err != nil || volume.UID == "" || volume.DeletionTimestamp != nil || volume.Status.Phase != corev1.VolumeBound || volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.UID != claim.UID || volume.Spec.ClaimRef.Name != claim.Name || volume.Spec.ClaimRef.Namespace != namespace.Name {
		return nil, nil, nil, "", fmt.Errorf("MyDuck cold-storage volume changed")
	}
	return set, claim, volume, namespace.UID, nil
}

func myduckColdIdentities(record myduckColdRecord, set *appsv1.StatefulSet, claim *corev1.PersistentVolumeClaim, volume *corev1.PersistentVolume, namespaceUID types.UID) bool {
	return record.SetUID == set.UID && record.NamespaceUID == namespaceUID && record.ClaimUID == claim.UID && record.VolumeUID == volume.UID
}

func (c *Client) myduckWaitStopped(ctx context.Context, d database.Resource, before func() error) error {
	for {
		if err := before(); err != nil {
			return err
		}
		pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: 4, LabelSelector: "!" + databaseRecoveryHelper})
		if err != nil {
			return err
		}
		if pods.Continue != "" || len(pods.Items) > 3 {
			return fmt.Errorf("MyDuck cold-storage member inventory exceeded its bound")
		}
		// A terminating or failed pod may still have a process attached to the
		// claim. Wait for actual deletion before mounting it anywhere else.
		if len(pods.Items) == 0 {
			return nil
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			return err
		}
	}
}

func myduckStoragePod(d database.Resource, set *appsv1.StatefulSet, record myduckColdRecord) *corev1.Pod {
	no := false
	yes := true
	uid := int64(1000)
	deadline, grace := int64(1800), int64(10)
	groupPolicy := corev1.FSGroupChangeOnRootMismatch
	spec := corev1.PodSpec{
		NodeName: record.Node, RestartPolicy: corev1.RestartPolicyNever,
		AutomountServiceAccountToken: &no, EnableServiceLinks: &no,
		ActiveDeadlineSeconds: &deadline, TerminationGracePeriodSeconds: &grace,
		RuntimeClassName: set.Spec.Template.Spec.RuntimeClassName,
		NodeSelector:     set.Spec.Template.Spec.NodeSelector, Tolerations: set.Spec.Template.Spec.Tolerations,
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, FSGroupChangePolicy: &groupPolicy, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Volumes:         []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-database-0"}}}},
		Containers: []corev1.Container{{Name: "storage", Image: myduckServerImage, ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{myduckStorageBinary, "--mode=hold"},
			SecurityContext: &corev1.SecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/myduck"}},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("256Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("32Mi")}},
		}},
	}
	meta := databaseIdentityMeta(d, record.NamespaceUID, myduckColdName(record.JobID))
	meta.Labels[databaseRecoveryHelper] = "true"
	meta.Annotations = map[string]string{myduckColdAnnotation: record.JobID}
	return &corev1.Pod{ObjectMeta: meta, Spec: spec}
}

func (c *Client) myduckStorageIsolation(ctx context.Context, d database.Resource, record myduckColdRecord, before func() error) error {
	meta := databaseIdentityMeta(d, record.NamespaceUID, "myduck-storage-isolation")
	wanted := &networkingv1.NetworkPolicy{ObjectMeta: meta, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{databaseRecoveryHelper: "true"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
	api := c.kube.NetworkingV1().NetworkPolicies(meta.Namespace)
	old, err := api.Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(old, d, record.NamespaceUID) || old.DeletionTimestamp != nil || !reflect.DeepEqual(old.Spec, wanted.Spec) {
		return fmt.Errorf("MyDuck cold-storage isolation changed")
	}
	return nil
}

func myduckStoragePodMatches(pod *corev1.Pod, desired *corev1.Pod, d database.Resource, record myduckColdRecord) bool {
	identity := pod.DeepCopy()
	identity.DeletionTimestamp = nil
	if !mongodbSupportOwned(identity, d, record.NamespaceUID) || pod.Annotations[myduckColdAnnotation] != record.JobID || pod.Labels[databaseRecoveryHelper] != "true" || pod.Labels[myduckMemberLabel] != "" || pod.UID == "" {
		return false
	}
	actual, expected := pod.Spec.DeepCopy(), desired.Spec.DeepCopy()
	// These defaults carry no additional authority. Keep every mounted volume,
	// capability, environment value, resource bound and scheduling field strict.
	for _, spec := range []*corev1.PodSpec{actual, expected} {
		if spec.ServiceAccountName == "default" {
			spec.ServiceAccountName = ""
		}
		if spec.DeprecatedServiceAccount == "default" {
			spec.DeprecatedServiceAccount = ""
		}
		if spec.DNSPolicy != "" && spec.DNSPolicy != corev1.DNSClusterFirst || spec.SchedulerName != "" && spec.SchedulerName != "default-scheduler" {
			return false
		}
		spec.DNSPolicy, spec.SchedulerName = "", ""
		if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
			return false
		}
		if spec.PreemptionPolicy != nil && *spec.PreemptionPolicy != corev1.PreemptLowerPriority {
			return false
		}
		spec.PreemptionPolicy = nil
		if spec.Priority != nil && *spec.Priority != 0 {
			return false
		}
		spec.Priority = nil
		for i := range spec.Containers {
			container := &spec.Containers[i]
			if container.TerminationMessagePath != "" && container.TerminationMessagePath != "/dev/termination-log" || container.TerminationMessagePolicy != "" && container.TerminationMessagePolicy != corev1.TerminationMessageReadFile {
				return false
			}
			spec.Containers[i].TerminationMessagePath = ""
			spec.Containers[i].TerminationMessagePolicy = ""
		}
		// The admission controller adds the standard not-ready/unreachable
		// NoExecute tolerations. They cannot change the fixed node assignment.
		tolerations := spec.Tolerations[:0]
		for _, tolerance := range spec.Tolerations {
			if (tolerance.Key == "node.kubernetes.io/not-ready" || tolerance.Key == "node.kubernetes.io/unreachable") && tolerance.Operator == corev1.TolerationOpExists && tolerance.Effect == corev1.TaintEffectNoExecute && tolerance.Value == "" && tolerance.TolerationSeconds != nil && *tolerance.TolerationSeconds == 300 {
				continue
			}
			tolerations = append(tolerations, tolerance)
		}
		spec.Tolerations = tolerations
		if len(spec.Tolerations) == 0 {
			spec.Tolerations = nil
		}
	}
	return reflect.DeepEqual(actual, expected)
}

// WithMyDuckColdStorage runs only after the API records a durable database/job
// fence. Every mutation rechecks that fence. A later maintenance pass drains
// the owned helper and resumes a backup source if its worker disappears.
func (c *Client) WithMyDuckColdStorage(ctx context.Context, d database.Resource, observed database.Observation, jobID string, restore bool, before func() error, input io.Reader, out io.Writer) error {
	if !myduckJobID.MatchString(jobID) || before == nil || observed.Status != "ready" || observed.Revision != d.Revision || len(observed.Members) != 1 || observed.Primary != observed.Members[0].Name {
		return fmt.Errorf("MyDuck cold storage requires current health and durable job authority")
	}
	if restore && (d.Status != "restoring" || d.Recovery == nil || d.Recovery.JobID != jobID || input == nil) || !restore && (d.Status != "ready" || out == nil) {
		return fmt.Errorf("MyDuck cold-storage operation does not match its resource")
	}
	member, _, err := c.databaseExecTarget(ctx, d, observed.Members[0])
	if err != nil {
		return err
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil || !databasePodPolicyMatches(*member, policy) || !databasePodMatches(*member, d) {
		return fmt.Errorf("MyDuck cold-storage runtime policy changed")
	}
	if restore {
		if err = c.databaseNetworkPolicy(ctx, d, before); err != nil {
			return err
		}
	}
	set, claim, volume, namespaceUID, err := c.myduckColdSet(ctx, d)
	if err != nil {
		return err
	}
	if set.Spec.Replicas == nil || *set.Spec.Replicas != 1 || set.Annotations[myduckColdAnnotation] != "" {
		return fmt.Errorf("MyDuck already has a cold-storage operation")
	}
	record := myduckColdRecord{JobID: jobID, Revision: d.Revision, Restore: restore, NamespaceUID: namespaceUID, SetUID: set.UID, ClaimUID: claim.UID, VolumeUID: volume.UID, MemberUID: member.UID, Node: member.Spec.NodeName}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if set.Annotations == nil {
		set.Annotations = map[string]string{}
	}
	set.Annotations[myduckColdAnnotation] = string(encoded)
	zero := int32(0)
	set.Spec.Replicas = &zero
	if err = before(); err != nil {
		return err
	}
	set, err = c.kube.AppsV1().StatefulSets(set.Namespace).Update(ctx, set, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	stop, cancel := context.WithTimeout(ctx, 3*time.Minute)
	err = c.myduckWaitStopped(stop, d, before)
	cancel()
	if err != nil {
		return fmt.Errorf("MyDuck process did not drain for cold storage: %w", err)
	}
	set, claim, volume, namespaceUID, err = c.myduckColdSet(ctx, d)
	if err != nil || !myduckColdIdentities(record, set, claim, volume, namespaceUID) {
		return fmt.Errorf("MyDuck storage identity changed after stopping")
	}
	if err = c.myduckStorageIsolation(ctx, d, record, before); err != nil {
		return err
	}
	wanted := myduckStoragePod(d, set, record)
	if !databasePodPolicyMatches(*wanted, policy) {
		return fmt.Errorf("MyDuck storage helper no longer matches its placement policy")
	}
	if err = before(); err != nil {
		return err
	}
	helper, err := c.kube.CoreV1().Pods(set.Namespace).Create(ctx, wanted, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	start, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		if err = before(); err != nil {
			return err
		}
		current, e := c.kube.CoreV1().Pods(set.Namespace).Get(start, helper.Name, metav1.GetOptions{})
		if e != nil || current.UID != helper.UID || current.DeletionTimestamp != nil || !myduckStoragePodMatches(current, wanted, d, record) {
			return fmt.Errorf("MyDuck storage helper changed")
		}
		helper = current
		if helper.Status.Phase == corev1.PodRunning {
			break
		}
		if helper.Status.Phase == corev1.PodFailed || helper.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("MyDuck storage helper stopped before capture")
		}
		if err = sleepContext(start, time.Second); err != nil {
			return err
		}
	}
	mode := "capture"
	if restore {
		// A live emptiness check can race an already connected writer. The
		// original process is now gone and this isolated helper alone mounts
		// the fenced volume. Check its catalog before reading restore bytes.
		if err = before(); err != nil {
			return err
		}
		empty, stop := context.WithTimeout(ctx, 30*time.Second)
		err = c.databaseExecVerifiedPod(empty, helper, "storage", []string{"/usr/local/bin/myduckserver", "--managed-storage-empty=/var/lib/myduck"}, nil, io.Discard)
		stop()
		if err != nil {
			return fmt.Errorf("MyDuck stopped target is not verified empty: %w", err)
		}
		mode = "restore"
	}
	command := []string{myduckStorageBinary, "--mode=" + mode, "--database-id=" + d.ID, "--revision=" + strconv.FormatInt(d.Revision, 10), "--version=" + d.Spec.Version, "--max-bytes=" + strconv.FormatInt(d.Spec.StorageGiB<<30, 10)}
	if err = before(); err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	if err = c.databaseExecVerifiedPod(ctx, helper, "storage", command, input, out); err != nil {
		return fmt.Errorf("MyDuck cold-storage stream failed: %w", err)
	}
	return nil
}

// ReconcileMyDuckColdStorage is idempotent. It never resumes until every helper
// process is gone. Failed restore targets stay stopped for deletion/inspection.
func (c *Client) ReconcileMyDuckColdStorage(ctx context.Context, d database.Resource, jobID string, resume bool, before func() error) error {
	if before == nil || !myduckJobID.MatchString(jobID) {
		return fmt.Errorf("MyDuck cold-storage cleanup requires durable authority")
	}
	set, claim, volume, namespaceUID, err := c.myduckColdSet(ctx, d)
	if err != nil {
		return err
	}
	if set.Annotations[myduckColdAnnotation] == "" {
		_, err := c.kube.CoreV1().Pods(set.Namespace).Get(ctx, myduckColdName(jobID), metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("MyDuck helper exists without its storage receipt")
		}
		return nil
	}
	record, err := myduckReadColdRecord(set, d, jobID)
	if err != nil || !myduckColdIdentities(record, set, claim, volume, namespaceUID) {
		return fmt.Errorf("MyDuck cold-storage cleanup identity changed")
	}
	if set.Spec.Replicas == nil || *set.Spec.Replicas != 0 {
		return fmt.Errorf("MyDuck cold-storage process restarted outside its fence")
	}
	pods := c.kube.CoreV1().Pods(set.Namespace)
	helper, err := pods.Get(ctx, myduckColdName(jobID), metav1.GetOptions{})
	if err == nil {
		wanted := myduckStoragePod(d, set, record)
		if !myduckStoragePodMatches(helper, wanted, d, record) {
			return fmt.Errorf("refusing to remove a changed MyDuck storage helper")
		}
		if helper.DeletionTimestamp == nil {
			if err = before(); err != nil {
				return err
			}
			if err = pods.Delete(ctx, helper.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &helper.UID}}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		for {
			if err = before(); err != nil {
				return err
			}
			current, e := pods.Get(ctx, helper.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				break
			}
			if e != nil || current.UID != helper.UID {
				return fmt.Errorf("MyDuck helper drain identity changed")
			}
			if err = sleepContext(ctx, time.Second); err != nil {
				return err
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	// Controller status updates can race cleanup. Each retry reads the current
	// object and rechecks storage identity and authority before resuming it.
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		set, claim, volume, namespaceUID, err := c.myduckColdSet(ctx, d)
		if err != nil || !myduckColdIdentities(record, set, claim, volume, namespaceUID) {
			return fmt.Errorf("MyDuck cleanup storage identity changed")
		}
		current, err := myduckReadColdRecord(set, d, jobID)
		if err != nil {
			return err
		}
		if current != record {
			return fmt.Errorf("MyDuck cold-storage receipt changed during cleanup")
		}
		if set.Spec.Replicas == nil || *set.Spec.Replicas != 0 {
			return fmt.Errorf("MyDuck cold-storage process restarted outside its fence")
		}
		if _, err = pods.Get(ctx, myduckColdName(jobID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			return fmt.Errorf("MyDuck storage helper reappeared during cleanup")
		}
		if err = before(); err != nil {
			return err
		}
		if resume {
			one := int32(1)
			set.Spec.Replicas = &one
		}
		delete(set.Annotations, myduckColdAnnotation)
		_, err = c.kube.AppsV1().StatefulSets(set.Namespace).Update(ctx, set, metav1.UpdateOptions{})
		return err
	})
}

func (c *Client) myduckDatabaseEmpty(ctx context.Context, d database.Resource, observed database.Observation) error {
	if observed.Status != "ready" || len(observed.Members) != 1 {
		return fmt.Errorf("MyDuck recovery requires one healthy separate target")
	}
	step, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.DatabaseExec(step, d, observed.Members[0], []string{"/usr/local/bin/myduckserver", "--managed-empty=" + myduckConfigPath}, nil, io.Discard)
}

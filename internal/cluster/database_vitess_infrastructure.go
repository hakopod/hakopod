package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type vitessEtcdStatus struct {
	Endpoint string `json:"Endpoint"`
	Status   struct {
		Header struct {
			ClusterID uint64 `json:"cluster_id"`
			MemberID  uint64 `json:"member_id"`
		} `json:"header"`
		Version string   `json:"version"`
		Leader  uint64   `json:"leader"`
		Errors  []string `json:"errors"`
		Learner bool     `json:"isLearner"`
	} `json:"Status"`
}

func (c *Client) verifyVitessEtcd(ctx context.Context, d database.Resource, members []database.Member) error {
	if len(members) != 3 {
		return fmt.Errorf("Vitess topology requires three owned voters")
	}
	views := make([]vitessEtcdStatus, 3)
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for i, member := range members {
		group.Go(func() error {
			var err error
			views[i], err = c.verifyVitessEtcdMember(step, d, member)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	ids := map[uint64]bool{}
	for _, view := range views {
		status := view.Status
		if status.Header.ClusterID == 0 || status.Header.ClusterID != views[0].Status.Header.ClusterID || status.Header.MemberID == 0 || ids[status.Header.MemberID] || status.Leader == 0 || status.Leader != views[0].Status.Leader || status.Version != "3.5.17" || len(status.Errors) > 0 || status.Learner {
			return fmt.Errorf("Vitess topology voters disagree or are unhealthy")
		}
		ids[status.Header.MemberID] = true
	}
	if !ids[views[0].Status.Leader] {
		return fmt.Errorf("Vitess topology leader is outside its owned voters")
	}
	return nil
}

// verifyVitessEtcdMember proves that one owned, current-identity voter can
// commit through its TLS endpoint. A successful write-path health check is
// also the quorum fence used between one-at-a-time identity replacements.
func (c *Client) verifyVitessEtcdMember(ctx context.Context, d database.Resource, member database.Member) (vitessEtcdStatus, error) {
	return c.verifyVitessEtcdMemberWithExec(ctx, d, member, func(command []string, stdout io.Writer) error {
		return c.DatabaseExec(ctx, d, member, command, nil, stdout)
	})
}

func (c *Client) verifyVitessEtcdMemberWithExec(ctx context.Context, d database.Resource, member database.Member, exec func([]string, io.Writer) error) (vitessEtcdStatus, error) {
	var empty vitessEtcdStatus
	host := member.Name + "." + vitessGeneratedName("database", "etcd") + "-peer." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
	endpoint := "https://" + host + ":2379"
	// The owned pod config sets ETCDCTL_ENDPOINTS. etcdctl refuses a duplicate
	// command-line option; status below still checks this exact owned member.
	command := []string{"etcdctl", "--dial-timeout=3s", "--command-timeout=5s", "endpoint", "health"}
	if err := exec(command, io.Discard); err != nil {
		return empty, fmt.Errorf("Vitess topology cannot commit through its TLS endpoint")
	}
	out := &databaseBoundedWriter{limit: 16 << 10}
	command = []string{"etcdctl", "--dial-timeout=3s", "--command-timeout=5s", "--write-out=json", "endpoint", "status"}
	if err := exec(command, out); err != nil {
		return empty, err
	}
	var result []vitessEtcdStatus
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result) != 1 || result[0].Endpoint != endpoint {
		return empty, fmt.Errorf("Vitess topology returned an invalid native status")
	}
	status := result[0].Status
	if status.Header.ClusterID == 0 || status.Header.MemberID == 0 || status.Leader == 0 || status.Version != "3.5.17" || len(status.Errors) > 0 || status.Learner {
		return empty, fmt.Errorf("Vitess topology voter is unhealthy")
	}
	return result[0], nil
}

func (c *Client) observeVitessInfrastructure(ctx context.Context, d database.Resource, object *unstructured.Unstructured, expectedIdentity string) error {
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || namespace.UID == "" || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess infrastructure namespace ownership changed")
	}
	ns := namespace.Name
	operator, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil || operator.DeletionTimestamp != nil || !vitessNamespaceObjectOwned(operator, d, namespace.UID) || operator.Spec.Template.Annotations[vitessIdentityAnnotation] != expectedIdentity || operator.Status.ObservedGeneration < operator.Generation || operator.Status.AvailableReplicas != 1 || operator.Status.UpdatedReplicas != 1 || operator.Status.Replicas != 1 || operator.Status.UnavailableReplicas != 0 || len(operator.Spec.Template.Spec.Containers) != 1 {
		return fmt.Errorf("Vitess namespace controller is unavailable")
	}
	container := operator.Spec.Template.Spec.Containers[0]
	if container.Image != vitessOperatorImage || container.Resources.Limits.Cpu().String() != database.VitessOperatorCPU || container.Resources.Limits.Memory().String() != database.VitessOperatorMemory {
		return fmt.Errorf("Vitess namespace controller differs from its approved runtime")
	}
	if !vitessControllerGoRuntimeBounded(container.Env, database.VitessControllerGOMEMLIMIT) {
		return fmt.Errorf("Vitess namespace controller Go runtime differs from its allocation")
	}
	operatorPods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=operator", Limit: 2, FieldSelector: activeDatabasePodFields})
	if err != nil || operatorPods.Continue != "" || len(operatorPods.Items) != 1 {
		return fmt.Errorf("Vitess namespace controller process is unavailable")
	}
	operatorPod := operatorPods.Items[0]
	if !c.vitessOperatorPodOwned(ctx, operatorPod.Name, operatorPod.UID, operatorPod.OwnerReferences, d, namespace.UID) || operatorPod.Spec.ServiceAccountName != "database-vitess-operator" || len(operatorPod.Spec.Containers) != 1 || operatorPod.Spec.Containers[0].Image != vitessOperatorImage || !vitessPodReadyWithIdentity(operatorPod, expectedIdentity) {
		return fmt.Errorf("Vitess namespace controller process identity is not ready")
	}
	storages, err := c.dynamic.Resource(schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackupstorages"}).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 2, LabelSelector: "planetscale.com/cluster=database"})
	if err != nil || storages.GetContinue() != "" || len(storages.Items) != 1 {
		return fmt.Errorf("Vitess native backup storage inventory is unavailable")
	}
	storage := storages.Items[0]
	if !c.vitessOwnedChain(ctx, ns, storage.GetUID(), storage.GetOwnerReferences(), object.GetUID()) {
		return fmt.Errorf("Vitess native backup storage ownership changed")
	}
	backups, err := c.dynamic.Resource(schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackups"}).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 801, LabelSelector: "planetscale.com/cluster=database"})
	if err != nil || backups.GetContinue() != "" || len(backups.Items) > 800 {
		return fmt.Errorf("Vitess native backup inventory exceeds its bound")
	}
	fresh := map[string]bool{}
	for _, copy := range backups.Items {
		owners := copy.GetOwnerReferences()
		if len(owners) != 1 || copy.GetUID() == "" || owners[0].UID != storage.GetUID() || owners[0].Name != storage.GetName() || owners[0].Kind != "VitessBackupStorage" || owners[0].APIVersion != "planetscale.com/v2" {
			return fmt.Errorf("Vitess native backup ownership changed")
		}
		complete, _, _ := unstructured.NestedBool(copy.Object, "status", "complete")
		if !complete {
			continue
		}
		stamp, _, _ := unstructured.NestedString(copy.Object, "status", "startTime")
		at, parseErr := time.Parse(time.RFC3339, stamp)
		if parseErr != nil || at.After(time.Now().Add(time.Minute)) {
			return fmt.Errorf("Vitess native backup timestamp is invalid")
		}
		shard := copy.GetLabels()["planetscale.com/shard"]
		parts := strings.Split(shard, "-")
		if len(parts) != 2 {
			return fmt.Errorf("Vitess native backup shard is invalid")
		}
		if parts[0] == "x" {
			parts[0] = ""
		}
		if parts[1] == "x" {
			parts[1] = ""
		}
		shard = strings.Join(parts, "-")
		if !slices.Contains(d.Spec.VitessShardNames(), shard) {
			return fmt.Errorf("Vitess native backup belongs to an unexpected shard")
		}
		if at.After(time.Now().Add(-2 * time.Hour)) {
			fresh[shard] = true
		}
	}
	if len(fresh) != d.Spec.Shards {
		return fmt.Errorf("Vitess native member recovery has no recent complete copy for every shard")
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "planetscale.com/cluster=database,planetscale.com/component=vbs-subcontroller", Limit: 3, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) != 1 {
		return fmt.Errorf("Vitess backup storage controller is unavailable")
	}
	pod := pods.Items[0]
	if !c.vitessPodOwned(ctx, pod, object.GetUID()) || pod.DeletionTimestamp != nil || pod.Spec.ServiceAccountName != "database-vitess-operator" || len(pod.Spec.Containers) != 1 {
		return fmt.Errorf("Vitess backup storage controller ownership changed")
	}
	container = pod.Spec.Containers[0]
	if container.Image != vitessOperatorImage || container.Resources.Requests.Cpu().String() != database.VitessBackupControllerCPU || container.Resources.Limits.Cpu().String() != database.VitessBackupControllerCPU || container.Resources.Requests.Memory().String() != database.VitessBackupControllerMemory || container.Resources.Limits.Memory().String() != database.VitessBackupControllerMemory {
		return fmt.Errorf("Vitess backup storage resources differ from their allocation")
	}
	if !vitessControllerGoRuntimeBounded(container.Env, database.VitessBackupControllerGOMEMLIMIT) {
		return fmt.Errorf("Vitess backup storage Go runtime differs from its allocation")
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return nil
		}
	}
	return fmt.Errorf("Vitess backup storage controller is not ready")
}

func vitessControllerGoRuntimeBounded(environment []corev1.EnvVar, memoryLimit string) bool {
	values := map[string]string{}
	for _, variable := range environment {
		if variable.Name != "GOMAXPROCS" && variable.Name != "GOMEMLIMIT" {
			continue
		}
		if variable.ValueFrom != nil {
			return false
		}
		if _, exists := values[variable.Name]; exists {
			return false
		}
		values[variable.Name] = variable.Value
	}
	return values["GOMAXPROCS"] == database.VitessControllerGOMAXPROCS && values["GOMEMLIMIT"] == memoryLimit
}

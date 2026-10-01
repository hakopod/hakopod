package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const vitessNativeViewQuery = `SELECT JSON_OBJECT('uuid',@@server_uuid,'readonly',@@super_read_only,'secure',@@require_secure_transport,'version',VERSION(),'channels',COALESCE((SELECT JSON_ARRAYAGG(JSON_OBJECT('host',HOST,'ssl',SSL_ALLOWED,'verify',SSL_VERIFY_SERVER_CERTIFICATE,'ca',SSL_CA_FILE)) FROM performance_schema.replication_connection_configuration),JSON_ARRAY()),'io_errors',(SELECT COUNT(*) FROM performance_schema.replication_connection_status WHERE SERVICE_STATE!='ON' OR LAST_ERROR_NUMBER!=0),'apply_errors',(SELECT COUNT(*) FROM performance_schema.replication_applier_status_by_worker WHERE SERVICE_STATE!='ON' OR LAST_ERROR_NUMBER!=0))`

type vitessNativeView struct {
	UUID        string                                   `json:"uuid"`
	ReadOnly    int                                      `json:"readonly"`
	Secure      int                                      `json:"secure"`
	Version     string                                   `json:"version"`
	Channels    []struct{ Host, SSL, Verify, CA string } `json:"channels"`
	IOErrors    int                                      `json:"io_errors"`
	ApplyErrors int                                      `json:"apply_errors"`
}

func verifyVitessNativeViews(d database.Resource, members []database.Member, views []vitessNativeView) ([]database.Member, error) {
	if len(views) != len(members) || len(members) != d.Spec.Members() {
		return nil, fmt.Errorf("Vitess native inventory is incomplete")
	}
	result := append([]database.Member(nil), members...)
	primaries := map[string]string{}
	serverIDs := map[string]bool{}
	for i, view := range views {
		if view.UUID == "" || serverIDs[view.UUID] || view.Secure != 1 || view.Version != database.VitessMySQLVersion || !slices.Contains(d.Spec.VitessShardNames(), members[i].Shard) || (view.ReadOnly != 0 && view.ReadOnly != 1) || view.IOErrors != 0 || view.ApplyErrors != 0 {
			return nil, fmt.Errorf("Vitess native role, version or transport is unhealthy")
		}
		serverIDs[view.UUID] = true
		result[i].Role = "replica"
		if view.ReadOnly == 0 {
			if primaries[members[i].Shard] != "" || len(view.Channels) != 0 {
				return nil, fmt.Errorf("Vitess shard has conflicting primary state")
			}
			primaries[members[i].Shard] = members[i].Name
			result[i].Role = "primary"
		}
	}
	for i, view := range views {
		primary := primaries[members[i].Shard]
		if primary == "" {
			return nil, fmt.Errorf("Vitess shard has no writable primary")
		}
		if view.ReadOnly == 0 {
			continue
		}
		if len(view.Channels) != 1 {
			return nil, fmt.Errorf("Vitess replica does not have exactly one replication source")
		}
		channel := view.Channels[0]
		if channel.Host != primary+"."+DatabaseNamespace(d.ID)+".svc.cluster.local" || channel.SSL != "YES" || channel.Verify != "YES" || channel.CA != vitessTLSPath+"/ca.crt" {
			return nil, fmt.Errorf("Vitess replica does not verify its owned primary's hostname and issuer")
		}
	}
	o := database.Observation{Status: "ready", Revision: d.Revision, Members: result}
	if _, err := vitessObservedPrimaries(d, o); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) observeVitessDatabase(ctx context.Context, d database.Resource, object *unstructured.Unstructured, o *database.Observation, expectedIdentity string) error {
	views := make([]vitessNativeView, len(o.Members))
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for i, member := range o.Members {
		group.Go(func() error {
			out := &databaseBoundedWriter{limit: 16 << 10}
			if err := c.DatabaseExec(step, d, member, vitessLocalCommand("vt_dba", vitessNativeViewQuery), nil, out); err != nil {
				return err
			}
			if json.Unmarshal(out.Bytes(), &views[i]) != nil {
				return fmt.Errorf("Vitess native health response is invalid")
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	members, err := verifyVitessNativeViews(d, o.Members, views)
	if err != nil {
		return err
	}
	o.Members = members
	identities := make([]string, 0, len(members))
	for _, m := range members {
		identities = append(identities, m.Shard+":"+m.Name+":"+m.UID+":"+m.Role)
		if d.Spec.Shards == 1 && m.Role == "primary" {
			o.Primary = m.Name
		}
	}
	slices.Sort(identities)
	o.TopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(identities, "\n"))))
	if err = c.verifyVitessRoutingWithIdentity(ctx, d, expectedIdentity); err != nil {
		return err
	}
	return c.observeVitessSupport(ctx, d, object, o, expectedIdentity)
}

func (c *Client) observeVitessSupport(ctx context.Context, d database.Resource, object *unstructured.Unstructured, o *database.Observation, expectedIdentity string) error {
	o.Routing = &database.RoutingObservation{Kind: "vtgate", Members: []database.Member{}}
	o.Coordination = &database.CoordinationObservation{Members: []database.Member{}}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + " in (gateway,topology,control,orchestrator)", Limit: 17, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) > 16 {
		return fmt.Errorf("Vitess supporting component inventory exceeds its bound")
	}
	counts := map[string]int{}
	for _, pod := range pods.Items {
		if !c.vitessPodOwned(ctx, pod, object.GetUID()) || pod.DeletionTimestamp != nil || pod.Labels[databaseOwner] != d.ID {
			return fmt.Errorf("Vitess supporting component ownership changed")
		}
		role := pod.Labels[vitessComponentLabel]
		member := database.Member{Name: pod.Name, UID: string(pod.UID), Role: role, Node: pod.Spec.NodeName, Phase: string(pod.Status.Phase)}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				member.Ready = vitessPodMatches(pod, d) && databasePodPolicyMatches(pod, policy) && vitessPodIdentityMatches(pod, expectedIdentity)
			}
		}
		for _, state := range pod.Status.ContainerStatuses {
			member.Restarts += state.RestartCount
		}
		if !member.Ready {
			return fmt.Errorf("Vitess supporting components are not ready")
		}
		counts[role]++
		if role == "gateway" {
			o.Routing.Members = append(o.Routing.Members, member)
		}
		if role == "topology" {
			o.Coordination.Members = append(o.Coordination.Members, member)
		}
	}
	if counts["gateway"] != d.Spec.VitessGateways() || counts["topology"] != 3 || counts["control"] != 1 || counts["orchestrator"] != d.Spec.Shards {
		return fmt.Errorf("Vitess supporting capacity does not match its allocation")
	}
	c.observeDatabaseMemberPlacement(ctx, o.Routing.Members)
	c.observeDatabaseMemberPlacement(ctx, o.Coordination.Members)
	if err = c.verifyVitessEtcd(ctx, d, o.Coordination.Members); err != nil {
		return err
	}
	if err = c.observeVitessInfrastructure(ctx, d, object, expectedIdentity); err != nil {
		return err
	}
	o.Routing.Ready, o.Coordination.Ready = true, true
	o.Endpoints = append(o.Endpoints, database.Endpoint{Purpose: "read_write", Host: "database." + DatabaseNamespace(d.ID) + ".svc", Port: 3306})
	if d.Spec.Replicas > 0 {
		o.Endpoints = append(o.Endpoints, database.Endpoint{Purpose: "read_only", Host: "database." + DatabaseNamespace(d.ID) + ".svc", Port: 3306})
	}
	return nil
}

func vitessPodMatches(pod corev1.Pod, d database.Resource) bool {
	role := pod.Labels[vitessComponentLabel]
	expected := map[string][3]string{}
	switch role {
	case "tablet":
		expected["mysqld"] = [3]string{vitessServerImage, d.Spec.CPU, d.Spec.Memory}
		expected["vttablet"] = [3]string{vitessServerImage, database.VitessTabletCPU, database.VitessTabletMemory}
	case "gateway":
		expected["vtgate"] = [3]string{vitessServerImage, database.VitessGatewayCPU, database.VitessGatewayMemory}
	case "control":
		expected["vtctld"] = [3]string{vitessServerImage, database.VitessControlCPU, database.VitessControlMemory}
	case "orchestrator":
		expected["vtorc"] = [3]string{vitessServerImage, database.VitessControlCPU, database.VitessControlMemory}
	case "topology":
		expected["etcd"] = [3]string{vitessEtcdImage, database.VitessTopologyCPU, database.VitessTopologyMemory}
	default:
		return false
	}
	if len(pod.Spec.Containers) != len(expected) || pod.Spec.ServiceAccountName != "database-vitess-workload" {
		return false
	}
	for _, container := range pod.Spec.Containers {
		want, ok := expected[container.Name]
		if !ok || container.Image != want[0] || container.Resources.Requests.Cpu().String() != want[1] || container.Resources.Limits.Cpu().String() != want[1] || container.Resources.Requests.Memory().String() != want[2] || container.Resources.Limits.Memory().String() != want[2] {
			return false
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if role != "tablet" || container.Image != vitessServerImage || container.Resources.Requests.Cpu().String() != database.VitessTabletCPU || container.Resources.Limits.Cpu().String() != database.VitessTabletCPU || container.Resources.Requests.Memory().String() != database.VitessTabletMemory || container.Resources.Limits.Memory().String() != database.VitessTabletMemory {
			return false
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ServiceAccountToken != nil {
					return false
				}
			}
		}
	}
	return true
}

func (c *Client) vitessExecTarget(ctx context.Context, d database.Resource, member database.Member) (*corev1.Pod, string, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return nil, "", fmt.Errorf("Vitess namespace ownership changed")
	}
	object, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID {
		return nil, "", fmt.Errorf("Vitess controller ownership changed")
	}
	expectedIdentity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return nil, "", err
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || pod.UID != types.UID(member.UID) || pod.DeletionTimestamp != nil || !c.vitessPodOwned(ctx, *pod, object.GetUID()) || !vitessPodMatches(*pod, d) || !vitessPodIdentityMatches(*pod, expectedIdentity) {
		return nil, "", fmt.Errorf("Vitess execution target changed")
	}
	container := map[string]string{"tablet": "vttablet", "gateway": "vtgate", "control": "vtctld", "orchestrator": "vtorc", "topology": "etcd"}[pod.Labels[vitessComponentLabel]]
	if container == "" || c.execConfig == nil || c.restClient() == nil {
		return nil, "", fmt.Errorf("Vitess execution transport is unavailable")
	}
	return pod, container, nil
}

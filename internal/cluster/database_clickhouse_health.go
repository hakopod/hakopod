package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (c *Client) observeClickHouseDatabase(ctx context.Context, d database.Resource, object *unstructured.Unstructured, o *database.Observation) error {
	if err := clickhouseClientIdentityConfigured(object, d); err != nil {
		return err
	}
	state, _, _ := unstructured.NestedString(object.Object, "status", "status")
	if state != "Completed" {
		return fmt.Errorf("waiting for ClickHouse controller reconciliation")
	}
	if err := c.observeClickHouseKeeper(ctx, d, o); err != nil {
		return err
	}
	group, step := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for i, m := range o.Members {
		group.Go(func() error {
			query := `SELECT
 (SELECT engine FROM system.databases WHERE name='app') AS database_engine,
 (SELECT substitution FROM system.macros WHERE macro='shard') AS shard,
 (SELECT substitution FROM system.macros WHERE macro='replica') AS replica,
 (SELECT count() FROM system.replicas WHERE database='app' AND (is_readonly OR is_session_expired OR active_replicas!=total_replicas OR total_replicas!=` + strconv.Itoa(d.Spec.Replicas+1) + `)) AS unhealthy_tables,
 (SELECT count() FROM system.replication_queue WHERE database='app' AND num_tries>0 AND last_exception!='') AS failed_replication,
 (SELECT count() FROM system.database_replicas WHERE database='app' AND is_readonly=0 AND zookeeper_path='/hakopod/` + d.ID + `/database/app') AS active_database_replicas
 FORMAT JSONEachRow SETTINGS output_format_json_quote_64bit_integers=0`
			out, err := c.clickhouseQuery(step, d, m, "monitor", query)
			if err != nil {
				return err
			}
			shard, err := parseClickHouseHealth(out, d.Spec)
			if err != nil {
				return err
			}
			o.Members[i].Shard = shard
			o.Members[i].Role = "replica"
			if d.Spec.Mode == "standalone" {
				o.Members[i].Role = "primary"
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	shards := map[string]int{}
	identities := []string{}
	for _, m := range o.Members {
		shards[m.Shard]++
		identities = append(identities, m.Name+":"+m.UID+":"+m.Shard)
	}
	if len(shards) != d.Spec.Shards {
		return fmt.Errorf("ClickHouse shard inventory is incomplete")
	}
	for _, count := range shards {
		if count != d.Spec.Replicas+1 {
			return fmt.Errorf("ClickHouse replica inventory is incomplete")
		}
	}
	if o.Coordination != nil {
		for _, m := range o.Coordination.Members {
			identities = append(identities, m.Name+":"+m.UID)
		}
	}
	slices.Sort(identities)
	o.TopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(identities, "\n"))))
	purpose := "cluster"
	if d.Spec.Mode == "standalone" {
		purpose = "read_write"
		o.Primary = o.Members[0].Name
	}
	host := "database." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
	o.Endpoints = []database.Endpoint{{Purpose: purpose, Host: host, Port: 9440}, {Purpose: "native", Host: host, Port: 9440}, {Purpose: "https", Host: host, Port: 8443}}
	return nil
}

// One native query per member keeps observation bounded as replicas increase.
func parseClickHouseHealth(raw string, s database.Spec) (string, error) {
	var sample struct {
		Engine                 string `json:"database_engine"`
		Shard                  string `json:"shard"`
		Replica                string `json:"replica"`
		UnhealthyTables        *int64 `json:"unhealthy_tables"`
		FailedReplication      *int64 `json:"failed_replication"`
		ActiveDatabaseReplicas *int64 `json:"active_database_replicas"`
	}
	if json.Unmarshal([]byte(raw), &sample) != nil || sample.UnhealthyTables == nil || sample.FailedReplication == nil || sample.ActiveDatabaseReplicas == nil {
		return "", fmt.Errorf("ClickHouse native health response is incomplete")
	}
	expected := "Atomic"
	if s.Mode == "cluster" {
		expected = "Replicated"
	}
	shard, err := strconv.Atoi(sample.Shard)
	if sample.Engine != expected || err != nil || shard < 0 || shard >= s.Shards || sample.Replica == "" {
		return "", fmt.Errorf("ClickHouse database or shard identity changed")
	}
	if *sample.UnhealthyTables != 0 || *sample.FailedReplication != 0 {
		return "", fmt.Errorf("ClickHouse replication has unhealthy or retrying work")
	}
	if s.Mode == "cluster" && *sample.ActiveDatabaseReplicas != 1 {
		return "", fmt.Errorf("ClickHouse replicated database has no active Keeper session")
	}
	return sample.Shard, nil
}

func (c *Client) observeClickHouseKeeper(ctx context.Context, d database.Resource, o *database.Observation) error {
	if d.Spec.KeeperInstances() == 0 {
		return nil
	}
	view := &database.CoordinationObservation{Members: []database.Member{}, Message: "Waiting for the three-member Keeper quorum."}
	o.Coordination = view
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: databaseKeeperLabel + "=true", Limit: 4, FieldSelector: activeDatabasePodFields})
	if err != nil || len(pods.Items) != 3 || pods.Continue != "" {
		return fmt.Errorf("Keeper member inventory is incomplete")
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	sandbox, err := c.clickhouseRuntime(ctx, d, policy)
	if err != nil {
		return err
	}
	set, err := c.kube.AppsV1().StatefulSets(DatabaseNamespace(d.ID)).Get(ctx, "database-keeper", metav1.GetOptions{})
	if err != nil || set.UID == "" || set.Spec.Replicas == nil || *set.Spec.Replicas != 3 || set.Status.ObservedGeneration < set.Generation || set.Status.UpdatedReplicas != 3 || set.Status.ReadyReplicas != 3 {
		return fmt.Errorf("Keeper workload has not converged")
	}
	leaders := 0
	for i := 0; i < 3; i++ {
		var pod *corev1.Pod
		name := fmt.Sprintf("database-keeper-%d", i)
		for j := range pods.Items {
			if pods.Items[j].Name == name {
				pod = &pods.Items[j]
			}
		}
		expectedPod := set.Spec.Template.Spec.DeepCopy()
		expectedPod.Volumes = append([]corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-" + name}}}}, expectedPod.Volumes...)
		if pod == nil || pod.DeletionTimestamp != nil || !databasePodPolicyMatches(*pod, policy) || !clickhousePodRuntimeMatches(*pod, sandbox) || !safeClickHouseKeeperTemplate(pod.Spec, *expectedPod) || pod.Spec.Containers[0].Image != clickhouseKeeperImage {
			return fmt.Errorf("Keeper member identity or placement changed")
		}
		for key, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: database.ClickHouseKeeperCPU, corev1.ResourceMemory: database.ClickHouseKeeperMemory} {
			q := resource.MustParse(value)
			request, limit := pod.Spec.Containers[0].Resources.Requests[key], pod.Spec.Containers[0].Resources.Limits[key]
			if request.Cmp(q) != 0 || limit.Cmp(q) != 0 {
				return fmt.Errorf("Keeper resource limits changed")
			}
		}
		m := database.Member{Name: pod.Name, UID: string(pod.UID), Node: pod.Spec.NodeName, Phase: string(pod.Status.Phase), Image: clickhouseKeeperImage}
		for _, status := range pod.Status.ContainerStatuses {
			m.Restarts += status.RestartCount
		}
		if !pod.CreationTimestamp.IsZero() {
			stamp := pod.CreationTimestamp.Time
			m.CreatedAt = &stamp
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				m.Ready = true
			}
		}
		view.Members = append(view.Members, m)
		if !m.Ready {
			return fmt.Errorf("Keeper member is not ready")
		}
		claim, err := c.kube.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, "data-"+name, metav1.GetOptions{})
		if err != nil || claim.UID == "" || claim.Status.Phase != corev1.ClaimBound || len(claim.Status.Conditions) > 0 || claim.Status.Capacity.Storage().Cmp(resource.MustParse("1Gi")) < 0 {
			return fmt.Errorf("Keeper persistent storage is not ready")
		}
		volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil || volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.UID != claim.UID || volume.Spec.ClaimRef.Namespace != claim.Namespace || volume.Spec.ClaimRef.Name != claim.Name {
			return fmt.Errorf("Keeper persistent volume identity changed")
		}
		config, err := c.clickhouseTLSConfig(ctx, d, clickhouseKeeperHost(d, i), true)
		if err != nil {
			return err
		}
		step, stop := context.WithTimeout(ctx, 5*time.Second)
		raw, err := c.clickhouseStream(step, d, m, 9281, true)
		if err != nil {
			stop()
			return err
		}
		conn := tls.Client(raw, config)
		err = conn.HandshakeContext(step)
		var output []byte
		if err == nil {
			_, err = io.WriteString(conn, "mntr")
			if err == nil {
				output, err = io.ReadAll(io.LimitReader(conn, 8193))
			}
		}
		_ = conn.Close()
		stop()
		if err != nil || len(output) > 8192 {
			return fmt.Errorf("Keeper native quorum status is unavailable")
		}
		role, err := clickhouseKeeperRole(string(output))
		if err != nil {
			return err
		}
		view.Members[len(view.Members)-1].Role = role
		if role == "leader" {
			leaders++
		}
	}
	if leaders != 1 {
		return fmt.Errorf("Keeper has no unique elected leader")
	}
	c.observeDatabaseMemberPlacement(ctx, view.Members)
	placementSpec := d.Spec
	placementSpec.Shards = 1
	placementSpec.Replicas = 2
	if !databasePlacementObservation(placementSpec, view.Members).Verified {
		return fmt.Errorf("Keeper members do not satisfy their placement")
	}
	view.Ready = true
	view.Message = ""
	return nil
}

func (c *Client) clickhouseEngineMetrics(ctx context.Context, d database.Resource, o database.Observation) (database.EngineMetrics, error) {
	samples := make([]database.EngineMetrics, len(o.Members))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(3)
	for i, m := range o.Members {
		group.Go(func() error {
			out, err := c.clickhouseQuery(ctx, d, m, "monitor", `SELECT (SELECT sum(value) FROM system.metrics WHERE metric IN ('TCPConnection','HTTPConnection')) AS connections, (SELECT count() FROM system.processes) AS active_connections, 200 AS max_connections, (SELECT coalesce(sum(bytes_on_disk),0) FROM system.parts WHERE active AND database='app') AS data_bytes, (SELECT value FROM system.events WHERE event='Query') AS commands, toUInt64(uptime()) AS uptime_seconds FORMAT JSONEachRow SETTINGS output_format_json_quote_64bit_integers=0`)
			if err != nil {
				return err
			}
			if json.Unmarshal([]byte(out), &samples[i]) != nil {
				return fmt.Errorf("ClickHouse metric response is invalid")
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return database.EngineMetrics{}, err
	}
	return aggregateClickHouseEngineMetrics(o.Members, samples)
}

func aggregateClickHouseEngineMetrics(members []database.Member, samples []database.EngineMetrics) (database.EngineMetrics, error) {
	var total database.EngineMetrics
	if len(members) == 0 || len(members) != len(samples) || len(members) > database.MaxMembers {
		return total, fmt.Errorf("ClickHouse metrics have an incomplete inventory")
	}
	shards := map[string]bool{}
	for i, sample := range samples {
		if !validDatabaseEngineMetrics(sample) || sample.ActiveConnections == nil || sample.Commands == nil || sample.UptimeSeconds == nil || members[i].Shard == "" {
			return total, fmt.Errorf("ClickHouse metric response is incomplete")
		}
		if total.Connections == nil {
			total = sample
		} else {
			*total.Connections += *sample.Connections
			*total.ActiveConnections += *sample.ActiveConnections
			*total.MaxConnections += *sample.MaxConnections
			*total.Commands += *sample.Commands
			// Logical data is counted once per shard, not once per replica.
			if !shards[members[i].Shard] {
				*total.DataBytes += *sample.DataBytes
			}
			*total.UptimeSeconds = min(*total.UptimeSeconds, *sample.UptimeSeconds)
		}
		shards[members[i].Shard] = true
	}
	now := time.Now().UTC()
	total.Available = true
	total.SampledAt = &now
	return total, nil
}

func (c *Client) renewClickHouseIdentity(ctx context.Context, d database.Resource, before func() error) error {
	if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	if err := c.prepareClickHouseClientIdentity(ctx, d, before); err != nil {
		return err
	}
	if err := c.reconcileClickHouseClientIdentityConfiguration(ctx, d, before); err != nil {
		return err
	}
	if err := c.applyClickHouseKeeper(ctx, d, before); err != nil {
		return err
	}
	// ClickHouse reloads certificate files on new TLS connections. Verification
	// still checks the served fingerprint, including every data and Keeper node.
	return nil
}

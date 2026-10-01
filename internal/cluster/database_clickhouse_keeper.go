package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func clickhouseKeeperHost(d database.Resource, i int) string {
	return fmt.Sprintf("database-keeper-%d.%s.svc.cluster.local", i, DatabaseNamespace(d.ID))
}

func clickhouseKeeperConfiguration(d database.Resource) string {
	servers := ""
	for i := 0; i < d.Spec.KeeperInstances(); i++ {
		servers += fmt.Sprintf("<server><id>%d</id><hostname>%s</hostname><port>9444</port></server>", i+1, clickhouseKeeperHost(d, i))
	}
	return `<clickhouse><logger><level>error</level><console>1</console></logger><listen_host>0.0.0.0</listen_host><max_connections>100</max_connections><max_server_memory_usage>201326592</max_server_memory_usage>
<keeper_server><tcp_port_secure>9281</tcp_port_secure><server_id from_env="KEEPER_SERVER_ID"/><log_storage_path>/var/lib/clickhouse/coordination/log</log_storage_path><snapshot_storage_path>/var/lib/clickhouse/coordination/snapshots</snapshot_storage_path><four_letter_word_white_list>ruok,mntr,srvr</four_letter_word_white_list><coordination_settings><operation_timeout_ms>10000</operation_timeout_ms><session_timeout_ms>30000</session_timeout_ms><raft_logs_level>warning</raft_logs_level></coordination_settings><raft_configuration><secure>true</secure>` + servers + `</raft_configuration></keeper_server>
<openSSL><server><certificateFile>/etc/hakopod-tls/tls.crt</certificateFile><privateKeyFile>/etc/hakopod-tls/tls.key</privateKeyFile><caConfig>/etc/hakopod-tls/ca.crt</caConfig><verificationMode>strict</verificationMode><loadDefaultCAFile>false</loadDefaultCAFile><disableProtocols>sslv2,sslv3,tlsv1,tlsv1_1</disableProtocols></server><client><certificateFile>/etc/hakopod-tls/tls.crt</certificateFile><privateKeyFile>/etc/hakopod-tls/tls.key</privateKeyFile><caConfig>/etc/hakopod-tls/ca.crt</caConfig><verificationMode>strict</verificationMode><loadDefaultCAFile>false</loadDefaultCAFile><extendedVerification>true</extendedVerification><disableProtocols>sslv2,sslv3,tlsv1,tlsv1_1</disableProtocols><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL></clickhouse>`
}

func (c *Client) applyClickHouseKeeper(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.KeeperInstances() == 0 {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return err
	}
	meta := databaseIdentityMeta(d, ns.UID, "database-keeper")
	labels := databaseLabels(d)
	labels[databaseKeeperLabel] = "true"
	meta.Labels = labels
	config := clickhouseKeeperConfiguration(d)
	cm, err := c.kube.CoreV1().ConfigMaps(ns.Name).Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		cm, err = c.kube.CoreV1().ConfigMaps(ns.Name).Create(ctx, &corev1.ConfigMap{ObjectMeta: meta, Immutable: ptr(true), Data: map[string]string{"keeper.xml": config}}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(cm, d, ns.UID) || cm.Data["keeper.xml"] != config {
		return fmt.Errorf("ClickHouse Keeper configuration identity changed")
	}
	service, err := c.kube.CoreV1().Services(ns.Name).Get(ctx, meta.Name, metav1.GetOptions{})
	selector := map[string]string{databaseOwner: d.ID, databaseKeeperLabel: "true"}
	ports := []corev1.ServicePort{{Name: "keeper-tls", Port: 9281, TargetPort: intstr.FromInt(9281), Protocol: corev1.ProtocolTCP}, {Name: "raft-tls", Port: 9444, TargetPort: intstr.FromInt(9444), Protocol: corev1.ProtocolTCP}}
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		service, err = c.kube.CoreV1().Services(ns.Name).Create(ctx, &corev1.Service{ObjectMeta: meta, Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Type: corev1.ServiceTypeClusterIP, PublishNotReadyAddresses: true, Selector: selector, Ports: ports}}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(service, d, ns.UID) || service.Spec.ClusterIP != corev1.ClusterIPNone || service.Spec.Type != corev1.ServiceTypeClusterIP || len(service.Spec.ExternalIPs) > 0 || !reflect.DeepEqual(service.Spec.Selector, selector) || !reflect.DeepEqual(service.Spec.Ports, ports) {
		return fmt.Errorf("ClickHouse Keeper service identity changed")
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	var nodeNames = d.Spec.Placement.NodeNames
	if len(nodeNames) == 0 && policy != nil {
		nodeNames = policy.nodes()
	}
	pod := corev1.PodSpec{
		AutomountServiceAccountToken: ptr(false), TerminationGracePeriodSeconds: ptr(int64(60)),
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(101)), RunAsGroup: ptr(int64(101)), FSGroup: ptr(int64(101)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{Name: "keeper", Image: clickhouseKeeperImage, ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"bash", "-c", `set -eu; export KEEPER_SERVER_ID=$(( ${HOSTNAME##*-} + 1 )); exec clickhouse-keeper --config-file=/etc/hakopod/keeper.xml`},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(database.ClickHouseKeeperCPU), corev1.ResourceMemory: resource.MustParse(database.ClickHouseKeeperMemory)}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(database.ClickHouseKeeperCPU), corev1.ResourceMemory: resource.MustParse(database.ClickHouseKeeperMemory)}},
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/clickhouse"}, {Name: "config", MountPath: "/etc/hakopod", ReadOnly: true}, {Name: "tls", MountPath: "/etc/hakopod-tls", ReadOnly: true}, {Name: "temporary", MountPath: "/tmp"}},
			ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(9281)}}, TimeoutSeconds: 3, PeriodSeconds: 5},
			LivenessProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(9281)}}, InitialDelaySeconds: 30, TimeoutSeconds: 3, PeriodSeconds: 10},
		}},
		Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: meta.Name}}}}, {Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "database-tls", DefaultMode: ptr(int32(0440))}}}, {Name: "temporary", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr(resource.MustParse("32Mi"))}}}},
	}
	if len(nodeNames) > 0 {
		terms := []corev1.NodeSelectorTerm{}
		for _, name := range nodeNames {
			terms = append(terms, corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{name}}}})
		}
		pod.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms}}}
	}
	if d.Spec.Placement.Spread != "" {
		if pod.Affinity == nil {
			pod.Affinity = &corev1.Affinity{}
		}
		pod.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{TopologyKey: databaseTopologyKey(d.Spec), LabelSelector: &metav1.LabelSelector{MatchLabels: selector}}}}
	}
	claim := corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data"}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", database.ClickHouseKeeperStorageGiB))}}}}
	if policy != nil {
		pod.NodeSelector = map[string]string{"hakopod.com/pool": policy.Pool, DatabaseDefaultRuntimeLabel: policy.RuntimeClass}
		pod.Tolerations = []corev1.Toleration{{Key: "hakopod.com/pool", Operator: corev1.TolerationOpEqual, Value: policy.Pool, Effect: corev1.TaintEffectNoSchedule}}
		claim.Spec.StorageClassName = ptr(policy.StorageClass)
	}
	sandbox, err := c.clickhouseRuntime(ctx, d, policy)
	if err != nil {
		return err
	}
	if sandbox != "" {
		pod.RuntimeClassName = ptr(sandbox)
		if pod.NodeSelector == nil {
			pod.NodeSelector = map[string]string{}
		}
		pod.NodeSelector[clickhouseRuntimeLabel] = clickhouseRuntimeProfile
	}
	identity, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(identity, d, ns.UID); err != nil {
		return err
	}
	desired := &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{ServiceName: meta.Name, Replicas: ptr(int32(3)), PodManagementPolicy: appsv1.ParallelPodManagement, Selector: &metav1.LabelSelector{MatchLabels: selector}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{"hakopod.io/tls-fingerprint": fmt.Sprintf("%x", sha256.Sum256(identity.Data["tls.crt"]))}}, Spec: pod}, VolumeClaimTemplates: []corev1.PersistentVolumeClaim{claim}}}
	sets := c.kube.AppsV1().StatefulSets(ns.Name)
	old, err := sets.Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = sets.Create(ctx, desired, metav1.CreateOptions{})
	} else if err == nil {
		if !mongodbSupportOwned(old, d, ns.UID) || old.Spec.ServiceName != desired.Spec.ServiceName || old.Spec.Replicas == nil || *old.Spec.Replicas != 3 || !reflect.DeepEqual(old.Spec.Selector, desired.Spec.Selector) || !safeClickHouseKeeperTemplate(old.Spec.Template.Spec, pod) || len(old.Spec.VolumeClaimTemplates) != 1 || !clickhouseOwnedFieldsMatch(&old.Spec.VolumeClaimTemplates[0].Spec, &claim.Spec) {
			return fmt.Errorf("ClickHouse Keeper workload ownership changed")
		}
		// Only certificate renewal rolls Keeper. Capacity and placement are immutable.
		if old.Spec.Template.Annotations["hakopod.io/tls-fingerprint"] != desired.Spec.Template.Annotations["hakopod.io/tls-fingerprint"] {
			old.Spec.Template.Annotations = desired.Spec.Template.Annotations
			if err = before(); err != nil {
				return err
			}
			_, err = sets.Update(ctx, old, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		return err
	}
	pdb, err := c.kube.PolicyV1().PodDisruptionBudgets(ns.Name).Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		pdb, err = c.kube.PolicyV1().PodDisruptionBudgets(ns.Name).Create(ctx, &policyv1.PodDisruptionBudget{ObjectMeta: meta, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: ptr(intstr.FromInt(2)), Selector: &metav1.LabelSelector{MatchLabels: selector}}}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(pdb, d, ns.UID) || pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 2 || pdb.Spec.Selector == nil || !reflect.DeepEqual(pdb.Spec.Selector, &metav1.LabelSelector{MatchLabels: selector}) {
		return fmt.Errorf("ClickHouse Keeper disruption policy changed")
	}
	return nil
}

func safeClickHouseKeeperTemplate(actual, expected corev1.PodSpec) bool {
	if actual.HostNetwork || actual.HostPID || actual.HostIPC || actual.ShareProcessNamespace != nil && *actual.ShareProcessNamespace || len(actual.Containers) != 1 || len(expected.Containers) != 1 || len(actual.InitContainers) != 0 || len(actual.EphemeralContainers) != 0 || len(actual.Volumes) != len(expected.Volumes) {
		return false
	}
	c := actual.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged || len(c.EnvFrom) != 0 || len(c.Env) != len(expected.Containers[0].Env) {
		return false
	}
	return clickhouseOwnedFieldsMatch(&actual, &expected)
}

func clickhouseOwnedFieldsMatch(actual, expected any) bool {
	// Use serialized fields so omitted API defaults (for example probe failure
	// thresholds) do not masquerade as drift. Explicit false pointers remain.
	a, err := runtime.DefaultUnstructuredConverter.ToUnstructured(actual)
	if err != nil {
		return false
	}
	e, err := runtime.DefaultUnstructuredConverter.ToUnstructured(expected)
	return err == nil && apiequality.Semantic.DeepDerivative(e, a)
}

func clickhouseKeeperRole(output string) (string, error) {
	role := ""
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "zk_server_state" {
			if role != "" {
				return "", fmt.Errorf("Keeper returned duplicate state")
			}
			role = fields[1]
		}
	}
	if role != "leader" && role != "follower" {
		return "", fmt.Errorf("Keeper has not joined a writable quorum")
	}
	return role, nil
}

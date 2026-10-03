package cluster

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type ManagedPlatformNodeReservation struct {
	UID                    string
	Architecture           string
	OperatingSystem        string
	SchedulingPool         string
	SchedulingRuntimeClass string
	Capacity               managedplatform.Capacity
	Ownership              managedplatform.CapacityPoolOwnership
	Namespaces             map[string]managedplatform.CapacityNamespaceOwnership
}

// DatabaseNodeReservation covers all grants sharing this node. Scopes identify
// managed database namespaces already included in that reserved envelope.
type DatabaseNodeReservation struct {
	UID                       string
	Capacity                  database.Capacity
	Scopes                    []string
	Ownership                 managedplatform.CapacityPoolOwnership
	ManagedPlatformNamespaces map[string]managedplatform.CapacityNamespaceOwnership
}

type capacityNamespaceIdentity struct {
	kind        string
	id          string
	owner       string
	project     string
	environment string
	capacity    managedplatform.Capacity
}

type capacityOwnershipIndex struct {
	applications map[string]capacityNamespaceIdentity
	databases    map[string]capacityNamespaceIdentity
	platforms    map[string]managedplatform.CapacityNamespaceOwnership
}

func pooledDatabaseRootResource(owner metav1.OwnerReference) (schema.GroupVersionResource, bool) {
	resources := map[string]schema.GroupVersionResource{
		"postgresql.cnpg.io/v1/Cluster":                     pgDatabaseResource,
		"redis.redis.opstreelabs.in/v1beta2/Redis":          redisDatabaseResource,
		"redis.redis.opstreelabs.in/v1beta2/RedisCluster":   redisClusterResource,
		"mysql.oracle.com/v2/InnoDBCluster":                 mysqlDatabaseResource,
		"mongodbcommunity.mongodb.com/v1/MongoDBCommunity":  mongodbDatabaseResource,
		"clickhouse.altinity.com/v1/ClickHouseInstallation": clickhouseDatabaseResource,
		"planetscale.com/v2/VitessCluster":                  vitessDatabaseResource,
		"database.oracle.com/v4/SingleInstanceDatabase":     oracleEnterpriseResource,
	}
	gvr, ok := resources[owner.APIVersion+"/"+owner.Kind]
	return gvr, ok
}

func pooledDatabaseRootName(owner metav1.OwnerReference) bool {
	if owner.Name == "database" {
		return true
	}
	if owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || !strings.HasPrefix(owner.Name, "database-") {
		return false
	}
	for _, value := range owner.Name[len("database-"):] {
		if value < '0' || value > '9' {
			return false
		}
	}
	return len(owner.Name) > len("database-")
}

func (c *Client) pooledDatabaseRootOwned(ctx context.Context, namespace string, owner metav1.OwnerReference, identity capacityNamespaceIdentity) (bool, error) {
	gvr, ok := pooledDatabaseRootResource(owner)
	if !ok || !pooledDatabaseRootName(owner) || c.dynamic == nil {
		return false, nil
	}
	item, err := c.dynamic.Resource(gvr).Namespace(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	labels := item.GetLabels()
	return item.GetUID() == owner.UID && item.GetDeletionTimestamp() == nil && labels[managedBy] == "hakopod" && labels[databaseOwner] == identity.owner && labels["hakopod.io/project"] == identity.project && labels["hakopod.io/environment"] == identity.environment, nil
}

func (c *Client) pooledVitessPodOwned(ctx context.Context, pod corev1.Pod, identity capacityNamespaceIdentity) (bool, error) {
	if c.dynamic == nil {
		return false, nil
	}
	root, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(pod.Namespace).Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	labels := root.GetLabels()
	if root.GetUID() == "" || root.GetDeletionTimestamp() != nil || labels[managedBy] != "hakopod" || labels[databaseOwner] != identity.owner || labels["hakopod.io/project"] != identity.project || labels["hakopod.io/environment"] != identity.environment {
		return false, nil
	}
	return c.vitessPodOwned(ctx, pod, root.GetUID()), nil
}

func capacityOwnership(ownership managedplatform.CapacityPoolOwnership, platforms map[string]managedplatform.CapacityNamespaceOwnership) (capacityOwnershipIndex, error) {
	result := capacityOwnershipIndex{applications: map[string]capacityNamespaceIdentity{}, databases: map[string]capacityNamespaceIdentity{}, platforms: map[string]managedplatform.CapacityNamespaceOwnership{}}
	if len(ownership.Workloads) > managedplatform.MaxCapacityPoolWorkloads || len(ownership.PlatformNamespaces)+len(platforms) > managedplatform.MaxCapacityPoolWorkloads {
		return result, fmt.Errorf("managed capacity pool ownership inventory exceeds its bound")
	}
	for _, workload := range ownership.Workloads {
		if workload.ID == "" || workload.Project == "" || workload.Environment == "" || workload.Capacity.CPUMilli < 0 || workload.Capacity.MemoryBytes < 0 {
			return result, fmt.Errorf("managed capacity pool workload identity is invalid")
		}
		identity := capacityNamespaceIdentity{kind: workload.Kind, id: workload.ID, project: workload.Project, environment: workload.Environment, capacity: workload.Capacity}
		var namespace string
		switch workload.Kind {
		case "application":
			namespace, identity.owner = Namespace(workload.ID), ownerID(workload.ID)
			if _, exists := result.applications[namespace]; exists {
				return result, fmt.Errorf("managed capacity pool application identity is duplicated")
			}
			result.applications[namespace] = identity
		case "database":
			namespace, identity.owner = DatabaseNamespace(workload.ID), workload.ID
			if _, exists := result.databases[namespace]; exists {
				return result, fmt.Errorf("managed capacity pool database identity is duplicated")
			}
			result.databases[namespace] = identity
		default:
			return result, fmt.Errorf("managed capacity pool workload kind is invalid")
		}
	}
	for namespace, ownership := range ownership.PlatformNamespaces {
		result.platforms[namespace] = ownership
	}
	for namespace, ownership := range platforms {
		if previous, exists := result.platforms[namespace]; exists && previous.UID != ownership.UID {
			return result, fmt.Errorf("managed platform namespace ownership is inconsistent")
		}
		result.platforms[namespace] = ownership
	}
	return result, nil
}

func (c *Client) pooledPodControllerOwned(ctx context.Context, pod corev1.Pod, identity capacityNamespaceIdentity, controllers map[string]bool) (bool, error) {
	if pod.UID == "" || len(pod.OwnerReferences) != 1 {
		return false, nil
	}
	owner := pod.OwnerReferences[0]
	if owner.UID == "" || owner.Controller == nil || !*owner.Controller {
		return false, nil
	}
	key := pod.Namespace + "\x00" + owner.APIVersion + "\x00" + owner.Kind + "\x00" + owner.Name + "\x00" + string(owner.UID)
	if owned, ok := controllers[key]; ok {
		return owned, nil
	}
	valid := func(labels map[string]string, uid types.UID, expected metav1.OwnerReference, deleting bool) bool {
		if uid != expected.UID || deleting || labels[managedBy] != "hakopod" {
			return false
		}
		if identity.kind == "application" {
			return labels[ownerKey] == identity.owner && labels[serviceKey] != ""
		}
		return labels[databaseOwner] == identity.owner
	}
	owned, err := func() (bool, error) {
		if identity.kind == "database" {
			if _, ok := pooledDatabaseRootResource(owner); ok {
				return c.pooledDatabaseRootOwned(ctx, pod.Namespace, owner, identity)
			}
			if strings.HasPrefix(owner.APIVersion+"/"+owner.Kind, "planetscale.com/v2/") || pod.Labels["planetscale.com/cluster"] == "database" {
				return c.pooledVitessPodOwned(ctx, pod, identity)
			}
		}
		switch owner.APIVersion + "/" + owner.Kind {
		case "apps/v1/ReplicaSet":
			item, err := c.kube.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil || !valid(item.Labels, item.UID, owner, item.DeletionTimestamp != nil) || len(item.OwnerReferences) != 1 {
				return false, err
			}
			parent := item.OwnerReferences[0]
			if parent.APIVersion != "apps/v1" || parent.Kind != "Deployment" || parent.UID == "" || parent.Controller == nil || !*parent.Controller {
				return false, nil
			}
			deployment, err := c.kube.AppsV1().Deployments(pod.Namespace).Get(ctx, parent.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			owned := err == nil && valid(deployment.Labels, deployment.UID, parent, deployment.DeletionTimestamp != nil)
			if identity.kind == "application" {
				owned = owned && deployment.Name == pod.Labels[serviceKey] && len(deployment.OwnerReferences) == 0
			}
			return owned, err
		case "apps/v1/StatefulSet":
			item, err := c.kube.AppsV1().StatefulSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return err == nil && valid(item.Labels, item.UID, owner, item.DeletionTimestamp != nil), err
		case "batch/v1/Job":
			item, err := c.kube.BatchV1().Jobs(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil || !valid(item.Labels, item.UID, owner, item.DeletionTimestamp != nil) {
				return false, err
			}
			if len(item.OwnerReferences) == 0 {
				return identity.kind == "application" && item.Name == jobName(pod.Labels[serviceKey]), nil
			}
			if len(item.OwnerReferences) != 1 {
				return false, nil
			}
			parent := item.OwnerReferences[0]
			if parent.APIVersion != "batch/v1" || parent.Kind != "CronJob" || parent.UID == "" || parent.Controller == nil || !*parent.Controller {
				return false, nil
			}
			cron, err := c.kube.BatchV1().CronJobs(pod.Namespace).Get(ctx, parent.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return err == nil && valid(cron.Labels, cron.UID, parent, cron.DeletionTimestamp != nil), err
		default:
			return false, nil
		}
	}()
	if err == nil {
		controllers[key] = owned
	}
	return owned, err
}

func (c *Client) pooledNamespacePod(ctx context.Context, pod corev1.Pod, namespaces map[string]*corev1.Namespace, controllers map[string]bool, ownership capacityOwnershipIndex) (string, managedplatform.Capacity, bool, error) {
	application, applicationOwned := ownership.applications[pod.Namespace]
	database, databaseOwned := ownership.databases[pod.Namespace]
	if !applicationOwned && !databaseOwned {
		return "", managedplatform.Capacity{}, false, nil
	}
	namespace, loaded := namespaces[pod.Namespace]
	if !loaded {
		var err error
		namespace, err = c.kube.CoreV1().Namespaces().Get(ctx, pod.Namespace, metav1.GetOptions{})
		if err != nil {
			return "", managedplatform.Capacity{}, false, fmt.Errorf("managed capacity namespace identity is unavailable")
		}
		namespaces[pod.Namespace] = namespace
	}
	if namespace.UID == "" || namespace.DeletionTimestamp != nil || namespace.Labels[managedBy] != "hakopod" {
		return "", managedplatform.Capacity{}, false, nil
	}
	identity := database
	if applicationOwned {
		identity = application
		if namespace.Name != "hp-"+identity.owner || namespace.Labels[ownerKey] != identity.owner || namespace.Labels[scopeKey] != scopeLabel(identity.project, identity.environment) || pod.Labels[managedBy] != "hakopod" || pod.Labels[ownerKey] != identity.owner || pod.Labels[serviceKey] == "" {
			return "", managedplatform.Capacity{}, false, nil
		}
	} else if namespace.Name != DatabaseNamespace(identity.owner) || namespace.Labels[databaseOwner] != identity.owner || namespace.Labels["hakopod.io/project"] != identity.project || namespace.Labels["hakopod.io/environment"] != identity.environment {
		return "", managedplatform.Capacity{}, false, nil
	}
	controlled, err := c.pooledPodControllerOwned(ctx, pod, identity, controllers)
	return identity.kind + "\x00" + identity.id, identity.capacity, controlled, err
}

func capacityWithHeadroom(cpu, memory int64) (int64, int64, error) {
	const cpuHeadroom int64 = 250
	const memoryHeadroom int64 = 256 << 20
	if cpu < 1 || memory < 1 || cpu > math.MaxInt64-cpuHeadroom || memory > math.MaxInt64-memoryHeadroom {
		return 0, 0, fmt.Errorf("node capacity reservation cannot include system headroom")
	}
	return cpu + cpuHeadroom, memory + memoryHeadroom, nil
}

func validManagedPlatformLabels(labels map[string]string, owned managedplatform.CapacityNamespaceOwnership) bool {
	return labels[managedBy] == "hakopod" && labels["hakopod.io/managed-platform-id"] == owned.PlatformID && labels["hakopod.io/managed-platform"] == owned.PlatformName
}

func capacityContainersMatch(actual, planned []corev1.Container) bool {
	if len(actual) != len(planned) {
		return false
	}
	for i := range actual {
		if actual[i].Name != planned[i].Name || actual[i].Image != planned[i].Image || actual[i].Resources.Requests.Cpu().MilliValue() != planned[i].Resources.Requests.Cpu().MilliValue() || actual[i].Resources.Requests.Memory().Value() != planned[i].Resources.Requests.Memory().Value() {
			return false
		}
	}
	return true
}

func capacityTemplateMatches(pod corev1.Pod, planned corev1.PodTemplateSpec) bool {
	if pod.Spec.NodeName == "" || planned.Spec.NodeName != "" && pod.Spec.NodeName != planned.Spec.NodeName || !reflect.DeepEqual(pod.Spec.RuntimeClassName, planned.Spec.RuntimeClassName) || !capacityContainersMatch(pod.Spec.Containers, planned.Spec.Containers) || !capacityContainersMatch(pod.Spec.InitContainers, planned.Spec.InitContainers) {
		return false
	}
	for key, value := range planned.Spec.NodeSelector {
		if pod.Spec.NodeSelector[key] != value {
			return false
		}
	}
	for key, value := range planned.Labels {
		if pod.Labels[key] != value {
			return false
		}
	}
	return true
}

func capacityTemplatesMatch(actual, planned corev1.PodTemplateSpec) bool {
	if actual.Spec.NodeName != planned.Spec.NodeName || !reflect.DeepEqual(actual.Spec.RuntimeClassName, planned.Spec.RuntimeClassName) || !capacityContainersMatch(actual.Spec.Containers, planned.Spec.Containers) || !capacityContainersMatch(actual.Spec.InitContainers, planned.Spec.InitContainers) {
		return false
	}
	for key, value := range planned.Labels {
		if actual.Labels[key] != value {
			return false
		}
	}
	return true
}

func workloadRequestsMatch(spec corev1.PodSpec, expected managedplatform.Capacity) bool {
	cpu, memory, err := podCapacityUsage(spec)
	reservedCPU, reservedMemory := int64(0), managedplatform.PodMemoryOverheadBytes
	if spec.RuntimeClassName != nil {
		observedCPU, observedMemory, valid := managedPlatformSandboxOverhead(spec.Overhead)
		if !valid {
			return false
		}
		reservedCPU = managedplatform.SandboxCPUOverheadMilli - observedCPU
		reservedMemory -= observedMemory
	}
	if err != nil || addNodeCapacityUsage(&cpu, &memory, reservedCPU, reservedMemory) != nil {
		return false
	}
	return cpu == expected.CPUMilli && memory == expected.MemoryBytes
}

func managedPlatformSandboxOverhead(resources corev1.ResourceList) (int64, int64, bool) {
	maxCPU, maxMemory := resource.MustParse("20m"), resource.MustParse("50Mi")
	for name, quantity := range resources {
		if quantity.Sign() < 0 {
			return 0, 0, false
		}
		switch name {
		case corev1.ResourceCPU:
			if quantity.Cmp(maxCPU) > 0 {
				return 0, 0, false
			}
		case corev1.ResourceMemory:
			if quantity.Cmp(maxMemory) > 0 {
				return 0, 0, false
			}
		default:
			if quantity.Sign() > 0 {
				return 0, 0, false
			}
		}
	}
	cpu, memory, err := capacityRequest(resources)
	return cpu, memory, err == nil
}

func validateManagedPlatformRuntime(runtime *nodev1.RuntimeClass, node *corev1.Node) error {
	if runtime == nil || runtime.Overhead == nil {
		if runtime == nil {
			return fmt.Errorf("managed platform sandbox is unavailable")
		}
	} else if _, _, valid := managedPlatformSandboxOverhead(runtime.Overhead.PodFixed); !valid {
		return fmt.Errorf("managed platform sandbox overhead exceeds the reserved profile")
	}
	if runtime.Scheduling != nil {
		for key, value := range runtime.Scheduling.NodeSelector {
			if node == nil || node.Labels[key] != value {
				return fmt.Errorf("managed platform sandbox scheduling conflicts with the approved node")
			}
		}
	}
	return nil
}

func (c *Client) managedPlatformPodReservation(ctx context.Context, pod corev1.Pod, ns *corev1.Namespace, owned managedplatform.CapacityNamespaceOwnership) (string, managedplatform.Capacity, bool, error) {
	if ns == nil || owned.UID == "" || string(ns.UID) != owned.UID || ns.Name != "managed-platform-"+owned.PlatformID || ns.Labels[managedBy] != "hakopod" || ns.Labels["hakopod.io/managed-platform-id"] != owned.PlatformID || !validManagedPlatformLabels(pod.Labels, owned) || len(pod.OwnerReferences) != 1 {
		return "", managedplatform.Capacity{}, false, nil
	}
	owner := pod.OwnerReferences[0]
	if owner.UID == "" || owner.Controller == nil || !*owner.Controller {
		return "", managedplatform.Capacity{}, false, nil
	}
	reservation := func(key, uid string) (managedplatform.Capacity, bool) {
		workload, ok := owned.Workloads[key]
		if !ok || workload.UID != uid {
			return managedplatform.Capacity{}, false
		}
		capacity, ok := workload.Nodes[pod.Spec.NodeName]
		return capacity, ok
	}
	switch owner.APIVersion + "/" + owner.Kind {
	case "apps/v1/StatefulSet":
		item, err := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "", managedplatform.Capacity{}, false, nil
		}
		if err != nil {
			return "", managedplatform.Capacity{}, false, err
		}
		key := "statefulset." + item.Name
		expected, ok := reservation(key, string(item.UID))
		if !ok || item.UID != owner.UID || !validManagedPlatformLabels(item.Labels, owned) || item.Spec.Replicas == nil || *item.Spec.Replicas != 1 || pod.Name != item.Name+"-0" || !capacityTemplateMatches(pod, item.Spec.Template) || !workloadRequestsMatch(item.Spec.Template.Spec, expected) {
			return "", managedplatform.Capacity{}, false, nil
		}
		return key, expected, true, nil
	case "apps/v1/ReplicaSet":
		replica, err := c.kube.AppsV1().ReplicaSets(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "", managedplatform.Capacity{}, false, nil
		}
		if err != nil {
			return "", managedplatform.Capacity{}, false, err
		}
		if replica.UID != owner.UID || !validManagedPlatformLabels(replica.Labels, owned) || len(replica.OwnerReferences) != 1 {
			return "", managedplatform.Capacity{}, false, nil
		}
		deploymentOwner := replica.OwnerReferences[0]
		if deploymentOwner.APIVersion != "apps/v1" || deploymentOwner.Kind != "Deployment" || deploymentOwner.UID == "" || deploymentOwner.Controller == nil || !*deploymentOwner.Controller {
			return "", managedplatform.Capacity{}, false, nil
		}
		deployment, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, deploymentOwner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "", managedplatform.Capacity{}, false, nil
		}
		if err != nil {
			return "", managedplatform.Capacity{}, false, err
		}
		key := "deployment." + deployment.Name
		expected, ok := reservation(key, string(deployment.UID))
		if !ok || deployment.UID != deploymentOwner.UID || !validManagedPlatformLabels(deployment.Labels, owned) || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != "Recreate" || replica.Spec.Replicas == nil || *replica.Spec.Replicas > 1 || !capacityTemplatesMatch(replica.Spec.Template, deployment.Spec.Template) || !capacityTemplateMatches(pod, replica.Spec.Template) || !workloadRequestsMatch(replica.Spec.Template.Spec, expected) {
			return "", managedplatform.Capacity{}, false, nil
		}
		return key, expected, true, nil
	default:
		return "", managedplatform.Capacity{}, false, nil
	}
}

func addNodeCapacityUsage(cpu, memory *int64, usedCPU, usedMemory int64) error {
	if usedCPU < 0 || usedMemory < 0 || *cpu > math.MaxInt64-usedCPU || *memory > math.MaxInt64-usedMemory {
		return fmt.Errorf("node workload capacity exceeds its arithmetic bound")
	}
	*cpu += usedCPU
	*memory += usedMemory
	return nil
}

func capacityRequest(resources corev1.ResourceList) (int64, int64, error) {
	cpuQuantity, memoryQuantity := resources.Cpu(), resources.Memory()
	// Check magnitude before rounded conversion: MilliValue can overflow and
	// AsInt64 rejects valid decimal quantities even when their value fits.
	if cpuQuantity.Sign() < 0 || memoryQuantity.Sign() < 0 || cpuQuantity.Cmp(*resource.NewMilliQuantity(math.MaxInt64, resource.DecimalSI)) > 0 || memoryQuantity.Cmp(*resource.NewQuantity(math.MaxInt64, resource.DecimalSI)) > 0 {
		return 0, 0, fmt.Errorf("pod resource request exceeds its arithmetic bound")
	}
	return cpuQuantity.MilliValue(), memoryQuantity.Value(), nil
}

func podCapacityUsage(spec corev1.PodSpec) (int64, int64, error) {
	var cpu, memory, sideCPU, sideMemory, initCPU, initMemory int64
	for _, container := range spec.Containers {
		containerCPU, containerMemory, err := capacityRequest(container.Resources.Requests)
		if err != nil || addNodeCapacityUsage(&cpu, &memory, containerCPU, containerMemory) != nil {
			return 0, 0, fmt.Errorf("pod container requests exceed their arithmetic bound")
		}
	}
	for _, container := range spec.InitContainers {
		containerCPU, containerMemory, err := capacityRequest(container.Resources.Requests)
		if err != nil {
			return 0, 0, err
		}
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			if err = addNodeCapacityUsage(&sideCPU, &sideMemory, containerCPU, containerMemory); err != nil {
				return 0, 0, err
			}
			initCPU, initMemory = max(initCPU, sideCPU), max(initMemory, sideMemory)
		} else {
			stageCPU, stageMemory := sideCPU, sideMemory
			if err = addNodeCapacityUsage(&stageCPU, &stageMemory, containerCPU, containerMemory); err != nil {
				return 0, 0, err
			}
			initCPU, initMemory = max(initCPU, stageCPU), max(initMemory, stageMemory)
		}
	}
	runningCPU, runningMemory := cpu, memory
	if err := addNodeCapacityUsage(&runningCPU, &runningMemory, sideCPU, sideMemory); err != nil {
		return 0, 0, err
	}
	cpu, memory = max(runningCPU, initCPU), max(runningMemory, initMemory)
	overheadCPU, overheadMemory, err := capacityRequest(spec.Overhead)
	if err != nil || addNodeCapacityUsage(&cpu, &memory, overheadCPU, overheadMemory) != nil {
		return 0, 0, fmt.Errorf("pod requests and overhead exceed their arithmetic bound")
	}
	return cpu, memory, nil
}

func addManagedPlatformExcess(cpu, memory *int64, usage, envelope map[string]managedplatform.Capacity) error {
	for key, actual := range usage {
		expected := envelope[key]
		if actual.CPUMilli < 0 || actual.MemoryBytes < 0 || expected.CPUMilli < 0 || expected.MemoryBytes < 0 {
			return fmt.Errorf("node workload capacity envelope is invalid")
		}
		excessCPU, excessMemory := int64(0), int64(0)
		if actual.CPUMilli > expected.CPUMilli {
			excessCPU = actual.CPUMilli - expected.CPUMilli
		}
		if actual.MemoryBytes > expected.MemoryBytes {
			excessMemory = actual.MemoryBytes - expected.MemoryBytes
		}
		if err := addNodeCapacityUsage(cpu, memory, excessCPU, excessMemory); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) checkDatabaseNodeReservation(ctx context.Context, node corev1.Node, r DatabaseNodeReservation) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if r.UID == "" || string(node.UID) != r.UID || r.Capacity.CPUMilli < 1 || r.Capacity.MemoryBytes < 1 || len(r.Scopes) < 1 || len(r.Scopes) > 64 {
		return fmt.Errorf("database node identity or capacity reservation is invalid")
	}
	ownership, err := capacityOwnership(r.Ownership, r.ManagedPlatformNamespaces)
	if err != nil {
		return err
	}
	// Allocatable already excludes kube/system reservations when configured. Keep
	// additional headroom for bounded probes and host processes on every node.
	cpu, memory, err := capacityWithHeadroom(r.Capacity.CPUMilli, r.Capacity.MemoryBytes)
	if err != nil {
		return err
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name, Limit: 1001})
	if err != nil || pods.Continue != "" || len(pods.Items) > 1000 {
		return fmt.Errorf("database node workload inventory is unavailable or exceeds its bound")
	}
	namespaces := map[string]*corev1.Namespace{}
	controllers := map[string]bool{}
	managedUsage := map[string]managedplatform.Capacity{}
	managedEnvelope := map[string]managedplatform.Capacity{}
	pooledUsage := map[string]managedplatform.Capacity{}
	pooledEnvelope := map[string]managedplatform.Capacity{}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node.Name || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		inGrant := false
		if strings.HasPrefix(pod.Namespace, "managed-platform-") {
			owned, ok := ownership.platforms[pod.Namespace]
			if ok {
				ns, loaded := namespaces[pod.Namespace]
				if !loaded {
					var e error
					ns, e = c.kube.CoreV1().Namespaces().Get(ctx, pod.Namespace, metav1.GetOptions{})
					if e != nil {
						return fmt.Errorf("managed platform namespace identity is unavailable")
					}
					namespaces[pod.Namespace] = ns
				}
				var e error
				var workload string
				var envelope managedplatform.Capacity
				workload, envelope, inGrant, e = c.managedPlatformPodReservation(ctx, pod, ns, owned)
				if e != nil {
					return fmt.Errorf("managed platform workload identity is unavailable")
				}
				if inGrant {
					key := pod.Namespace + "\x00" + workload
					usedCPU, usedMemory, usageErr := podCapacityUsage(pod.Spec)
					if usageErr != nil {
						return usageErr
					}
					actual := managedUsage[key]
					if usageErr = addNodeCapacityUsage(&actual.CPUMilli, &actual.MemoryBytes, usedCPU, usedMemory); usageErr != nil {
						return usageErr
					}
					managedUsage[key] = actual
					managedEnvelope[key] = envelope
				}
			} else {
				inGrant = false
			}
		} else {
			var key string
			var envelope managedplatform.Capacity
			key, envelope, inGrant, err = c.pooledNamespacePod(ctx, pod, namespaces, controllers, ownership)
			if err != nil {
				return err
			}
			if inGrant {
				usedCPU, usedMemory, usageErr := podCapacityUsage(pod.Spec)
				if usageErr != nil {
					return usageErr
				}
				actual := pooledUsage[key]
				if usageErr = addNodeCapacityUsage(&actual.CPUMilli, &actual.MemoryBytes, usedCPU, usedMemory); usageErr != nil {
					return usageErr
				}
				pooledUsage[key] = actual
				pooledEnvelope[key] = envelope
			}
		}
		if inGrant {
			continue
		}
		usedCPU, usedMemory, usageErr := podCapacityUsage(pod.Spec)
		if usageErr != nil {
			return usageErr
		}
		if err = addNodeCapacityUsage(&cpu, &memory, usedCPU, usedMemory); err != nil {
			return err
		}
	}
	if err = addManagedPlatformExcess(&cpu, &memory, managedUsage, managedEnvelope); err != nil {
		return err
	}
	if err = addManagedPlatformExcess(&cpu, &memory, pooledUsage, pooledEnvelope); err != nil {
		return err
	}
	if cpu > node.Status.Allocatable.Cpu().MilliValue() || memory > node.Status.Allocatable.Memory().Value() {
		return fmt.Errorf("database grants and existing workloads exceed node capacity including system headroom")
	}
	return nil
}

func (c *Client) CheckDatabaseNodeReservations(ctx context.Context, reservations map[string]DatabaseNodeReservation) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if len(reservations) > database.MaxMembers {
		return fmt.Errorf("database reservation inventory exceeds its bound")
	}
	for name, reservation := range reservations {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("database reservation node is unavailable")
		}
		if err = c.checkDatabaseNodeReservation(ctx, *node, reservation); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) CheckManagedPlatformNodeReservations(ctx context.Context, reservations map[string]ManagedPlatformNodeReservation) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if len(reservations) < 1 || len(reservations) > 48 {
		return fmt.Errorf("managed platform node reservation inventory is invalid")
	}
	for name, reservation := range reservations {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil || reservation.UID == "" || string(node.UID) != reservation.UID || reservation.Architecture == "" || node.Status.NodeInfo.Architecture != reservation.Architecture || reservation.OperatingSystem == "" || node.Status.NodeInfo.OperatingSystem != reservation.OperatingSystem || reservation.Capacity.CPUMilli < 1 || reservation.Capacity.MemoryBytes < 1 || len(reservation.Namespaces) > managedplatform.MaxCapacityReservations {
			return fmt.Errorf("managed platform reservation node is unavailable or invalid")
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready || node.Spec.Unschedulable || reservation.SchedulingPool != "" && node.Labels["hakopod.com/pool"] != reservation.SchedulingPool {
			return fmt.Errorf("managed platform reservation node is not schedulable")
		}
		matchedPoolTaint := reservation.SchedulingPool == ""
		for _, taint := range node.Spec.Taints {
			if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
				continue
			}
			if reservation.SchedulingPool != "" && taint.Key == "hakopod.com/pool" && taint.Value == reservation.SchedulingPool && taint.Effect == corev1.TaintEffectNoSchedule {
				matchedPoolTaint = true
				continue
			}
			return fmt.Errorf("managed platform reservation node has an unexpected blocking taint")
		}
		if !matchedPoolTaint {
			return fmt.Errorf("managed platform reservation node pool taint is unavailable")
		}
		if reservation.SchedulingRuntimeClass != "" {
			if err = c.CheckWorkloadPool(ctx, name, reservation.SchedulingPool, reservation.SchedulingRuntimeClass); err != nil {
				return err
			}
			runtimeClass, runtimeErr := c.kube.NodeV1().RuntimeClasses().Get(ctx, reservation.SchedulingRuntimeClass, metav1.GetOptions{})
			if runtimeErr != nil || validateManagedPlatformRuntime(runtimeClass, node) != nil {
				return fmt.Errorf("managed platform sandbox overhead is unavailable or invalid")
			}
		}
		ownership, ownershipErr := capacityOwnership(reservation.Ownership, reservation.Namespaces)
		if ownershipErr != nil {
			return ownershipErr
		}
		cpu, memory, headroomErr := capacityWithHeadroom(reservation.Capacity.CPUMilli, reservation.Capacity.MemoryBytes)
		if headroomErr != nil {
			return headroomErr
		}
		pods, listErr := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name, Limit: 1001})
		if listErr != nil || pods.Continue != "" || len(pods.Items) > 1000 {
			return fmt.Errorf("managed platform node workload inventory is unavailable or exceeds its bound")
		}
		namespaces := map[string]*corev1.Namespace{}
		controllers := map[string]bool{}
		managedUsage := map[string]managedplatform.Capacity{}
		managedEnvelope := map[string]managedplatform.Capacity{}
		pooledUsage := map[string]managedplatform.Capacity{}
		pooledEnvelope := map[string]managedplatform.Capacity{}
		for _, pod := range pods.Items {
			if pod.Spec.NodeName != name || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			inGrant := false
			if strings.HasPrefix(pod.Namespace, "managed-platform-") {
				owned, ok := ownership.platforms[pod.Namespace]
				ns, loaded := namespaces[pod.Namespace]
				if ok && !loaded {
					var namespaceErr error
					ns, namespaceErr = c.kube.CoreV1().Namespaces().Get(ctx, pod.Namespace, metav1.GetOptions{})
					if namespaceErr != nil {
						return fmt.Errorf("managed platform namespace identity is unavailable")
					}
					namespaces[pod.Namespace] = ns
				}
				if ok {
					var ownershipErr error
					var workload string
					var envelope managedplatform.Capacity
					workload, envelope, inGrant, ownershipErr = c.managedPlatformPodReservation(ctx, pod, ns, owned)
					if ownershipErr != nil {
						return fmt.Errorf("managed platform workload identity is unavailable")
					}
					if inGrant {
						key := pod.Namespace + "\x00" + workload
						usedCPU, usedMemory, usageErr := podCapacityUsage(pod.Spec)
						if usageErr != nil {
							return usageErr
						}
						actual := managedUsage[key]
						if usageErr = addNodeCapacityUsage(&actual.CPUMilli, &actual.MemoryBytes, usedCPU, usedMemory); usageErr != nil {
							return usageErr
						}
						managedUsage[key] = actual
						managedEnvelope[key] = envelope
					}
				}
			} else {
				var key string
				var envelope managedplatform.Capacity
				key, envelope, inGrant, err = c.pooledNamespacePod(ctx, pod, namespaces, controllers, ownership)
				if err != nil {
					return err
				}
				if inGrant {
					usedCPU, usedMemory, usageErr := podCapacityUsage(pod.Spec)
					if usageErr != nil {
						return usageErr
					}
					actual := pooledUsage[key]
					if usageErr = addNodeCapacityUsage(&actual.CPUMilli, &actual.MemoryBytes, usedCPU, usedMemory); usageErr != nil {
						return usageErr
					}
					pooledUsage[key] = actual
					pooledEnvelope[key] = envelope
				}
			}
			if inGrant {
				continue
			}
			usedCPU, usedMemory, usageErr := podCapacityUsage(pod.Spec)
			if usageErr != nil {
				return usageErr
			}
			if err = addNodeCapacityUsage(&cpu, &memory, usedCPU, usedMemory); err != nil {
				return err
			}
		}
		if err = addManagedPlatformExcess(&cpu, &memory, managedUsage, managedEnvelope); err != nil {
			return err
		}
		if err = addManagedPlatformExcess(&cpu, &memory, pooledUsage, pooledEnvelope); err != nil {
			return err
		}
		if cpu > node.Status.Allocatable.Cpu().MilliValue() || memory > node.Status.Allocatable.Memory().Value() {
			return fmt.Errorf("managed platform grants and existing workloads exceed node capacity including system headroom")
		}
	}
	return nil
}

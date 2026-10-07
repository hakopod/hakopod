package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func oracleFreeTestPod(t *testing.T, d database.Resource) corev1.Pod {
	t.Helper()
	object, err := oracleFreeObject(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		PodSpec corev1.PodSpec `json:"podSpec"`
	}
	if err = json.Unmarshal([]byte(object.GetAnnotations()[oracleFreePodPolicy]), &envelope); err != nil {
		t.Fatal(err)
	}
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-member", Namespace: DatabaseNamespace(d.ID), UID: "member-uid", ResourceVersion: "13", Labels: map[string]string{databaseOwner: d.ID, managedBy: "hakopod", oracleEnterpriseMemberLabel: "true"}, Annotations: object.GetAnnotations(), OwnerReferences: []metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: "database", UID: "root-uid", Controller: ptr(true)}}}, Spec: envelope.PodSpec}
}

func TestOracleFreeRejectsPodPrivilegeAndExecutionDrift(t *testing.T) {
	d := oracleFixture()
	pod := oracleFreeTestPod(t, d)
	if !oracleFreePodMatches(pod, d) {
		t.Fatal("fixed Free pod did not match")
	}
	changes := map[string]func(*corev1.Pod){
		"argument": func(p *corev1.Pod) { p.Spec.Containers[0].Args = []string{"extra"} },
		"environment": func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "ORACLE_PWD", Value: "unauthorized"}}
		},
		"environment source": func(p *corev1.Pod) {
			p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "operator-token"}}}}
		},
		"lifecycle": func(p *corev1.Pod) {
			p.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{PostStart: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"sh"}}}}
		},
		"readiness":        func(p *corev1.Pod) { p.Spec.Containers[0].ReadinessProbe.Exec.Command = []string{"true"} },
		"plaintext port":   func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].ContainerPort = 1521 },
		"host port":        func(p *corev1.Pod) { p.Spec.Containers[0].Ports[0].HostPort = 2484 },
		"host networking":  func(p *corev1.Pod) { p.Spec.HostNetwork = true },
		"operator account": func(p *corev1.Pod) { p.Spec.ServiceAccountName = "database-oracle-free-operator" },
		"operator account alias": func(p *corev1.Pod) {
			p.Spec.ServiceAccountName = "database-oracle-free-operator"
			p.Spec.DeprecatedServiceAccount = p.Spec.ServiceAccountName
		},
		"token mount":         func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = ptr(true) },
		"secret substitution": func(p *corev1.Pod) { p.Spec.Volumes[0].Secret.SecretName = "foreign" },
		"extra sidecar":       func(p *corev1.Pod) { p.Spec.Containers = append(p.Spec.Containers, p.Spec.Containers[0]) },
		"privileged init":     func(p *corev1.Pod) { p.Spec.InitContainers = []corev1.Container{p.Spec.Containers[0]} },
		"root identity":       func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsUser = ptr(int64(0)) },
		"capability": func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN"}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := pod.DeepCopy()
			change(changed)
			if oracleFreePodMatches(*changed, d) {
				t.Fatal("accepted changed Free execution boundary")
			}
		})
	}
}

func TestOracleFreeStatusRequiresCurrentCompletedGeneration(t *testing.T) {
	object, err := oracleFreeObject(oracleFixture(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	object.SetGeneration(4)
	stamp := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	condition := func(kind string, generation int64, at time.Time) any {
		return map[string]any{"type": kind, "status": "True", "reason": "LastReconcileCycleCompleted", "observedGeneration": generation, "lastTransitionTime": at.Format(time.RFC3339)}
	}
	object.Object["status"] = map[string]any{"status": "Healthy", "replicas": int64(1), "conditions": []any{condition("ReconcileComplete", 4, stamp)}}
	if !oracleFreeStatusReady(object) {
		t.Fatal("current completed SIDB was rejected")
	}
	for _, conditions := range [][]any{
		{condition("ReconcileComplete", 3, stamp)},
		{condition("ReconcileComplete", 4, stamp), condition("ReconcileError", 4, stamp.Add(time.Second))},
		{condition("ReconcileQueued", 4, stamp)},
	} {
		object.Object["status"].(map[string]any)["conditions"] = conditions
		if oracleFreeStatusReady(object) {
			t.Fatal("stale or incomplete SIDB status was accepted")
		}
	}
}

func TestOracleFreeRenewalRequiresOwnershipAndMutationFence(t *testing.T) {
	for _, mode := range []string{"fenced", "foreign owner", "duplicate members", "missing controller", "owned replacement"} {
		t.Run(mode, func(t *testing.T) {
			d := oracleFixture()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
			identity := &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-tls"), Data: map[string][]byte{"tls.crt": []byte("test-leaf"), "ca.crt": []byte("test-issuer")}}
			root, err := oracleFreeObject(d, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			oracleFreeTestStorageSpec(root)
			root.SetUID("root-uid")
			root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
			annotations := root.GetAnnotations()
			annotations[oracleFreeIdentity] = oracleIdentityFingerprint(identity)
			root.SetAnnotations(annotations)
			pod := oracleFreeTestPod(t, d)
			pod.Annotations[oracleFreeIdentity] = "old-identity"
			if mode == "foreign owner" {
				pod.OwnerReferences[0].UID = "foreign-root"
			}
			objects := []runtime.Object{ns, identity, &pod, oracleFreeTestStorageClass()}
			if mode != "missing controller" {
				objects = append(objects, oracleFreeTestControllerObjects(t, d, ns.UID)...)
			}
			if mode == "duplicate members" {
				other := pod.DeepCopy()
				other.Name = "duplicate"
				other.UID = "duplicate-uid"
				objects = append(objects, other)
			}
			kube := fake.NewClientset(objects...)
			c := &Client{kube: kube, dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)}
			blocked := errors.New("authority changed")
			fence := func() error {
				if mode == "fenced" {
					return blocked
				}
				return nil
			}
			err = c.renewOracleFreeWorkload(context.Background(), d, fence)
			if mode == "owned replacement" && err != nil {
				t.Fatal(err)
			}
			if mode != "owned replacement" && err == nil {
				t.Fatal("unsafe replacement accepted")
			}
			deletions := 0
			for _, action := range kube.Actions() {
				if action.GetVerb() == "delete" {
					deletions++
					opts := action.(kubetesting.DeleteAction).GetDeleteOptions()
					if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != pod.UID || opts.Preconditions.ResourceVersion == nil || *opts.Preconditions.ResourceVersion != pod.ResourceVersion {
						t.Fatal("replacement was not fenced to the exact observed pod")
					}
				}
			}
			if mode == "owned replacement" && deletions != 1 || mode != "owned replacement" && deletions != 0 {
				t.Fatal("unexpected replacement mutation")
			}
		})
	}
}

func TestOracleFreeAdmissionAndRBACStayNarrow(t *testing.T) {
	d := oracleFixture()
	if (&Client{}).DatabaseControllerAvailable(context.Background(), d.Spec) == nil {
		t.Fatal("unqualified Oracle Free became available")
	}
	if oracleFreeOperatorImage == "" {
		if (&Client{}).applyOracleFreeDatabase(context.Background(), d, []byte(strings.Repeat("a", 64)), func() error { t.Fatal("unpublished operator reached mutation"); return nil }) == nil {
			t.Fatal("missing image qualification accepted")
		}
	}
	for _, raw := range oracleFreeRoleRules() {
		rule := raw.(map[string]any)
		for _, resource := range rule["resources"].([]any) {
			name := resource.(string)
			if name == "*" || name == "nodes" || name == "namespaces" || name == "dataguardbrokers" || name == "serviceaccounts" {
				t.Fatalf("unexpected operator permission %s", name)
			}
		}
	}
	object, err := oracleFreeObject(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if object.GetKind() != "SingleInstanceDatabase" || object.GetAPIVersion() != "database.oracle.com/v4" {
		t.Fatal("Free must use Oracle's v4 SIDB API")
	}
	for _, field := range []string{"archiveLog", "forceLog", "flashBack"} {
		value, _, _ := unstructured.NestedBool(object.Object, "spec", field)
		if value {
			t.Fatal("Free inherited an unqualified recovery mode")
		}
	}
}

func TestOracleFreeExportCandidate(t *testing.T) {
	root := os.Getenv("HAKOPOD_ORACLE_FREE_CANDIDATE_DIR")
	if root == "" {
		t.Skip("candidate export is only used by the Linux operator build")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	object, err := oracleFreeObject(oracleFixture(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	oracleFreeTestStorageSpec(object)
	annotations := object.GetAnnotations()
	annotations[oracleFreeIdentity] = strings.Repeat("a", 64)
	object.SetAnnotations(annotations)
	encoded, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "database.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func oracleFreeTestStorageClass() *storagev1.StorageClass {
	return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}}
}
func oracleFreeTestStorageSpec(object *unstructured.Unstructured) {
	_ = unstructured.SetNestedField(object.Object, "local-path", "spec", "persistence", "oradata", "storageClass")
	claims, _, _ := unstructured.NestedSlice(object.Object, "spec", "persistence", "additionalPVCs")
	claims[0].(map[string]any)["storageClass"] = "local-path"
	_ = unstructured.SetNestedSlice(object.Object, claims, "spec", "persistence", "additionalPVCs")
}
func TestOracleFreeRequiresAMD64AndBoundedSandbox(t *testing.T) {
	d := oracleFixture()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{corev1.LabelArchStable: "arm64"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	c := &Client{kube: fake.NewClientset(node)}
	if c.validateDatabasePlacementNodes(context.Background(), d.Spec, nil) == nil {
		t.Fatal("Free accepted ARM-only workers")
	}
	node.Labels[corev1.LabelArchStable] = "amd64"
	c = &Client{kube: fake.NewClientset(node)}
	if err := c.validateDatabasePlacementNodes(context.Background(), d.Spec, nil); err != nil {
		t.Fatal(err)
	}
	pod := oracleFreeTestPod(t, d)
	pod.Spec.RuntimeClassName = ptr("runsc")
	pod.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("50Mi")}
	if !oracleFreePodMatches(pod, d) {
		t.Fatal("bounded runsc overhead was rejected")
	}
	pod.Spec.Overhead[corev1.ResourceMemory] = resource.MustParse("51Mi")
	if oracleFreePodMatches(pod, d) {
		t.Fatal("unreserved runsc overhead was accepted")
	}
	operator := oracleFreeControllerObject(d, "namespace-uid")
	arch, _, _ := unstructured.NestedString(operator.Object, "spec", "template", "spec", "nodeSelector", corev1.LabelArchStable)
	if arch != "amd64" {
		t.Fatal("operator omitted architecture placement")
	}
}
func TestOracleFreeResolvesAndRetainsStorageClass(t *testing.T) {
	d := oracleFixture()
	c := &Client{kube: fake.NewClientset(oracleFreeTestStorageClass()), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	object, err := c.databaseObject(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	name, _, _ := unstructured.NestedString(object.Object, "spec", "persistence", "oradata", "pvcName")
	if name != "" {
		t.Fatal("explicit data claim prevents operator creation")
	}
	claims, _, _ := unstructured.NestedSlice(object.Object, "spec", "persistence", "additionalPVCs")
	if claims[0].(map[string]any)["pvcName"] != nil || claims[0].(map[string]any)["storageClass"] != "local-path" {
		t.Fatal("backup claim cannot be created by SIDB")
	}
	other := oracleFreeTestStorageClass()
	other.Name = "ambiguous"
	if _, err = c.kube.StorageV1().StorageClasses().Create(context.Background(), other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.databaseObject(context.Background(), d); err == nil {
		t.Fatal("ambiguous defaults accepted")
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	object.SetUID("root-uid")
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	c = &Client{kube: fake.NewClientset(ns, oracleFreeTestStorageClass(), other), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)}
	if _, err = c.databaseObject(context.Background(), d); err != nil {
		t.Fatal("existing immutable storage did not survive a changed default", err)
	}
	if matched, err := c.DatabaseRevisionApplied(context.Background(), d); err != nil || !matched {
		t.Fatal("accepted SIDB revision rejected", err)
	}
	annotations := object.GetAnnotations()
	annotations[oracleFreePodPolicy] = "changed"
	object.SetAnnotations(annotations)
	c.dynamic = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)
	if matched, err := c.DatabaseRevisionApplied(context.Background(), d); err != nil || matched {
		t.Fatal("changed pod policy counted as applied", err)
	}
	object.SetOwnerReferences(nil)
	c.dynamic = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)
	if _, err := c.DatabaseRevisionApplied(context.Background(), d); err == nil {
		t.Fatal("foreign SIDB counted as applied")
	}
}

func TestOracleFreeRejectsForeignResourcesAndUnsafeService(t *testing.T) {
	d := oracleFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	object, err := oracleFreeObject(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	oracleFreeTestStorageSpec(object)
	object.SetUID("root-uid")
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	c := &Client{kube: fake.NewClientset(ns, oracleFreeTestStorageClass(), &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name}}), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)}
	if c.oracleFreeResourcesPreflight(context.Background(), d) == nil {
		t.Fatal("foreign claim would be adopted")
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: ns.Name, UID: "service-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: "database", UID: object.GetUID(), Controller: ptr(true)}}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.1.2", Selector: map[string]string{"app": "database", databaseOwner: d.ID, oracleEnterpriseMemberLabel: "true"}, Ports: []corev1.ServicePort{{Name: "tcps", Protocol: corev1.ProtocolTCP, Port: 2484, TargetPort: intstr.FromInt(2484)}}}}
	c = &Client{kube: fake.NewClientset(ns, service), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)}
	if err := c.oracleFreeServiceOwned(context.Background(), d, object); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*corev1.Service){
		"foreign owner":  func(s *corev1.Service) { s.OwnerReferences[0].UID = "foreign" },
		"external IP":    func(s *corev1.Service) { s.Spec.ExternalIPs = []string{"203.0.113.1"} },
		"wrong selector": func(s *corev1.Service) { s.Spec.Selector[databaseOwner] = "foreign" },
		"unready route":  func(s *corev1.Service) { s.Spec.PublishNotReadyAddresses = true },
		"plaintext":      func(s *corev1.Service) { s.Spec.Ports[0].Port = 1521 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := service.DeepCopy()
			change(changed)
			c.kube = fake.NewClientset(ns, changed)
			if c.oracleFreeServiceOwned(context.Background(), d, object) == nil {
				t.Fatal("unsafe private route accepted")
			}
		})
	}
}
func TestOracleFreeDeletionKeepsOperatorUntilFinalization(t *testing.T) {
	d := oracleFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	object, err := oracleFreeObject(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	object.SetUID("root-uid")
	object.SetResourceVersion("7")
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	for _, blocked := range []bool{true, false} {
		objects := append([]runtime.Object{ns}, oracleFreeTestControllerObjects(t, d, ns.UID)...)
		kube := fake.NewClientset(objects...)
		dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)
		c := &Client{kube: kube, dynamic: dynamic}
		removed, err := c.DeleteDatabase(context.Background(), d, func() error {
			if blocked {
				return errors.New("lease lost")
			}
			return nil
		})
		if removed || blocked && err == nil || !blocked && err != nil {
			t.Fatal("unexpected delete result", removed, err)
		}
		deletes := 0
		for _, action := range dynamic.Actions() {
			if action.GetVerb() == "delete" {
				deletes++
				options := action.(kubetesting.DeleteAction).GetDeleteOptions()
				if options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground || options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != object.GetUID() || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != "7" {
					t.Fatal("SIDB deletion lacked exact fencing")
				}
			}
		}
		if blocked && deletes != 0 || !blocked && deletes != 1 {
			t.Fatal("unsafe SIDB deletion")
		}
		for _, action := range kube.Actions() {
			if action.GetVerb() == "delete" {
				t.Fatal("namespace deleted before SIDB finalized")
			}
		}
	}
}

func TestOracleFreeControllerReadinessRejectsStaleDeploymentPolicy(t *testing.T) {
	d := oracleFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	for name, change := range map[string]func(*appsv1.Deployment){
		"ready":            func(*appsv1.Deployment) {},
		"missing replicas": func(deployment *appsv1.Deployment) { deployment.Spec.Replicas = nil },
		"scaled down":      func(deployment *appsv1.Deployment) { deployment.Spec.Replicas = ptr(int32(0)) },
		"scaled up":        func(deployment *appsv1.Deployment) { deployment.Spec.Replicas = ptr(int32(2)) },
		"rolling update": func(deployment *appsv1.Deployment) {
			deployment.Spec.Strategy.Type = appsv1.RollingUpdateDeploymentStrategyType
		},
		"foreign selector":      func(deployment *appsv1.Deployment) { deployment.Spec.Selector.MatchLabels[databaseOwner] = "foreign" },
		"unobserved generation": func(deployment *appsv1.Deployment) { deployment.Generation++ },
	} {
		t.Run(name, func(t *testing.T) {
			objects := oracleFreeTestControllerObjects(t, d, ns.UID)
			change(objects[0].(*appsv1.Deployment))
			c := &Client{kube: fake.NewClientset(append([]runtime.Object{ns}, objects...)...)}
			err := c.oracleFreeControllerReady(context.Background(), d)
			if name == "ready" && err != nil || name != "ready" && err == nil {
				t.Fatalf("unexpected controller readiness: %v", err)
			}
		})
	}
}

func TestOracleFreeControllerReadinessHandlesAPIDefaultedServiceAccount(t *testing.T) {
	d := oracleFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	for _, name := range []string{"matching aliases", "conflicting template alias", "conflicting pod alias", "conflicting template default alias", "conflicting pod default alias"} {
		t.Run(name, func(t *testing.T) {
			objects := oracleFreeTestControllerObjects(t, d, ns.UID)
			deployment := objects[0].(*appsv1.Deployment)
			pod := objects[2].(*corev1.Pod)
			deployment.Spec.Template.Spec.DeprecatedServiceAccount = deployment.Spec.Template.Spec.ServiceAccountName
			pod.Spec.DeprecatedServiceAccount = pod.Spec.ServiceAccountName
			if name == "conflicting template alias" {
				deployment.Spec.Template.Spec.DeprecatedServiceAccount = "foreign"
			}
			if name == "conflicting pod alias" {
				pod.Spec.DeprecatedServiceAccount = "foreign"
			}
			if name == "conflicting template default alias" {
				deployment.Spec.Template.Spec.DeprecatedServiceAccount = "default"
			}
			if name == "conflicting pod default alias" {
				pod.Spec.DeprecatedServiceAccount = "default"
			}
			c := &Client{kube: fake.NewClientset(append([]runtime.Object{ns}, objects...)...)}
			err := c.oracleFreeControllerReady(context.Background(), d)
			if name == "matching aliases" && err != nil || name != "matching aliases" && err == nil {
				t.Fatalf("unexpected controller readiness: %v", err)
			}
		})
	}
}

// Explicit unit-test fixtures model Kubernetes controller ownership and its
// standard token projection. They are never served as dashboard observations.
func oracleFreeTestControllerObjects(t *testing.T, d database.Resource, namespaceUID types.UID) []runtime.Object {
	t.Helper()
	raw := oracleFreeControllerObject(d, namespaceUID)
	var deployment appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.UID = "deployment-uid"
	deployment.Generation = 1
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	replica := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "operator-rs", Namespace: deployment.Namespace, UID: "replicaset-uid", Labels: deployment.Spec.Template.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID, Controller: ptr(true)}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "operator-pod", Namespace: deployment.Namespace, UID: "operator-pod-uid", Labels: deployment.Spec.Template.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replica.Name, UID: replica.UID, Controller: ptr(true)}}}, Spec: *deployment.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	pod.Spec.Volumes = []corev1.Volume{{Name: "kube-api-access-fixture", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr(int32(0644)), Sources: []corev1.VolumeProjection{
		{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: ptr(int64(3607))}},
		{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
		{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{Path: "namespace", FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"}}}}},
	}}}}}
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "kube-api-access-fixture", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true}}
	metadata := databaseIdentityMeta(d, namespaceUID, "database-oracle-free-operator")
	metadata.UID = "account-uid"
	account := &corev1.ServiceAccount{ObjectMeta: metadata, AutomountServiceAccountToken: ptr(true)}
	role := &rbacv1.Role{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(map[string]any{"rules": oracleFreeRoleRules()}, role); err != nil {
		t.Fatal(err)
	}
	role.ObjectMeta = *metadata.DeepCopy()
	role.UID = "role-uid"
	binding := &rbacv1.RoleBinding{ObjectMeta: *metadata.DeepCopy(), RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: metadata.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: metadata.Name, Namespace: metadata.Namespace}}}
	binding.UID = "binding-uid"
	return []runtime.Object{&deployment, replica, pod, account, role, binding}
}

func TestOracleFreeControllerReadinessRequiresExactPermissions(t *testing.T) {
	for _, mode := range []string{"ready", "foreign account", "token disabled", "role lost create", "role grants secrets", "foreign binding", "foreign role", "unready probe", "missing probe"} {
		t.Run(mode, func(t *testing.T) {
			d := oracleFixture()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
			objects := oracleFreeTestControllerObjects(t, d, ns.UID)
			account, role, binding := objects[3].(*corev1.ServiceAccount), objects[4].(*rbacv1.Role), objects[5].(*rbacv1.RoleBinding)
			switch mode {
			case "foreign account":
				account.OwnerReferences[0].UID = "foreign-namespace"
			case "token disabled":
				account.AutomountServiceAccountToken = ptr(false)
			case "role lost create":
				role.Rules[0].Verbs = []string{"get", "list", "watch"}
			case "role grants secrets":
				role.Rules[1].Verbs = append(role.Rules[1].Verbs, "update")
			case "foreign binding":
				binding.Subjects[0].Namespace = "another-namespace"
			case "foreign role":
				binding.RoleRef.Name = "another-role"
			case "unready probe":
				objects[2].(*corev1.Pod).Status.Conditions[0].Status = corev1.ConditionFalse
			case "missing probe":
				objects[2].(*corev1.Pod).Spec.Containers[0].ReadinessProbe = nil
			}
			c := &Client{kube: fake.NewClientset(append([]runtime.Object{ns}, objects...)...)}
			err := c.oracleFreeControllerReady(context.Background(), d)
			if mode == "ready" && err != nil || mode != "ready" && err == nil {
				t.Fatal("unsafe controller readiness", err)
			}
		})
	}
}

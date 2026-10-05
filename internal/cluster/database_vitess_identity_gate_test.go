package cluster

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func vitessIdentityGatePod(d database.Resource, name, role, identity string, ready bool) *corev1.Pod {
	containers := []corev1.Container{}
	if role == "control" {
		cpu := resource.MustParse(database.VitessControlCPU)
		memory := resource.MustParse(database.VitessControlMemory)
		containers = []corev1.Container{{Name: "vtctld", Image: vitessServerImage, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}, Limits: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}}}}
	}
	conditions := []corev1.PodCondition{}
	if ready {
		conditions = append(conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: DatabaseNamespace(d.ID), UID: types.UID(name + "-uid"),
			Labels:          map[string]string{vitessComponentLabel: role, databaseOwner: d.ID},
			Annotations:     map[string]string{vitessIdentityAnnotation: identity},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: "database-uid", Controller: ptr(true)}},
		},
		Spec:   corev1.PodSpec{ServiceAccountName: "database-vitess-workload", Containers: containers},
		Status: corev1.PodStatus{Conditions: conditions},
	}
}

func TestVitessIdentityRotationPreservesSharedSocketAnnotations(t *testing.T) {
	d := vitessTestDatabase()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "database-tls", Namespace: ns.Name, Labels: databaseLabels(d), OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}}},
		Data:       map[string][]byte{"tls.crt": []byte("public-certificate-fixture")},
	}
	client := &Client{kube: kubefake.NewSimpleClientset(ns, secret)}
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	mutateVitessComponents(object, func(item map[string]any, role string) {
		if role == "tablet" {
			item["annotations"].(map[string]any)["operator.example/keep"] = "present"
		}
	})
	for _, certificate := range []string{"public-certificate-fixture", "rotated-public-certificate-fixture"} {
		secret.Data["tls.crt"] = []byte(certificate)
		if _, err = client.kube.CoreV1().Secrets(ns.Name).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err = client.applyVitessIdentity(context.Background(), d, object); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(certificate)))
		mutateVitessComponents(object, func(item map[string]any, role string) {
			annotations := item["annotations"].(map[string]any)
			if annotations[vitessIdentityAnnotation] != want {
				t.Errorf("%s identity was not rotated", role)
			}
			if role == "tablet" {
				for key, value := range map[string]string{"dev.gvisor.spec.mount.rundir.share": "pod", "dev.gvisor.spec.mount.rundir.type": "tmpfs", "dev.gvisor.spec.mount.rundir.options": "rw,rprivate,size=16777216", "operator.example/keep": "present"} {
					if annotations[key] != value {
						t.Errorf("rotation removed tablet annotation %s", key)
					}
				}
			} else if len(annotations) != 1 {
				t.Errorf("%s received tablet socket annotations", role)
			}
		})
	}
}

func TestVitessReadyIdentityCountsExcludeStaleRolloutPods(t *testing.T) {
	d := vitessTestDatabase()
	pods := []corev1.Pod{
		*vitessIdentityGatePod(d, "tablet-current", "tablet", "current", true),
		*vitessIdentityGatePod(d, "tablet-stale", "tablet", "stale", true),
		*vitessIdentityGatePod(d, "control-stale", "control", "stale", true),
		*vitessIdentityGatePod(d, "control-current-pending", "control", "current", false),
	}
	tablets, controls := vitessReadyIdentityCounts(pods, "current")
	if tablets != 1 || controls != 0 {
		t.Fatalf("stale or unready rollout Pods passed the identity gate: tablets=%d controls=%d", tablets, controls)
	}
	pods[3].Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	_, controls = vitessReadyIdentityCounts(pods, "current")
	if controls != 1 {
		t.Fatalf("current ready control was not selected: controls=%d", controls)
	}
}

func TestVitessControlSelectionIgnoresStaleReadyOverlap(t *testing.T) {
	d := vitessTestDatabase()
	root, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	root.SetUID("database-uid")
	stale := vitessIdentityGatePod(d, "control-stale", "control", "stale", true)
	current := vitessIdentityGatePod(d, "control-current", "control", "current", true)
	client := &Client{
		kube:    kubefake.NewSimpleClientset(stale, current),
		dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root),
	}
	member, err := client.vitessControlMemberWithIdentity(context.Background(), d, "current")
	if err != nil || member.Name != current.Name || member.UID != string(current.UID) {
		t.Fatal("current control was not selected across a stale ready overlap", member, err)
	}
	current.Status.Conditions = nil
	client.kube = kubefake.NewSimpleClientset(stale, current)
	_, err = client.vitessControlMemberWithIdentity(context.Background(), d, "current")
	if err == nil || !strings.Contains(err.Error(), "waiting for Vitess control readiness") {
		t.Fatal("stale ready control did not leave rollout pending", err)
	}
	for _, pendingFirst := range []bool{false, true} {
		ready := vitessIdentityGatePod(d, "control-ready", "control", "current", true)
		pending := vitessIdentityGatePod(d, "control-pending", "control", "current", false)
		objects := []runtime.Object{ready, pending}
		if pendingFirst {
			objects[0], objects[1] = objects[1], objects[0]
		}
		client.kube = kubefake.NewSimpleClientset(objects...)
		member, err = client.vitessControlMemberWithIdentity(context.Background(), d, "current")
		if err != nil || member.Name != ready.Name {
			t.Fatal("a same-identity pending control blocked the single ready control", pendingFirst, member, err)
		}
	}
}

func TestVitessExecTargetRejectsStaleIdentityAndOldUID(t *testing.T) {
	d := vitessTestDatabase()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	certificate := []byte("fixture-certificate")
	expected := fmt.Sprintf("%x", sha256.Sum256(certificate))
	secret := &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-tls"), Data: map[string][]byte{"tls.crt": certificate}}
	root, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	root.SetUID("database-uid")
	pod := vitessIdentityGatePod(d, "control-current", "control", "stale", true)
	client := &Client{kube: kubefake.NewSimpleClientset(ns, secret, pod), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)}
	member := database.Member{Name: pod.Name, UID: string(pod.UID)}
	if _, _, err = client.vitessExecTarget(context.Background(), d, member); err == nil || !strings.Contains(err.Error(), "execution target changed") {
		t.Fatal("stale certificate identity remained executable", err)
	}
	pod.Annotations[vitessIdentityAnnotation] = expected
	client.kube = kubefake.NewSimpleClientset(ns, secret, pod)
	member.UID = "replaced-pod-uid"
	if _, _, err = client.vitessExecTarget(context.Background(), d, member); err == nil || !strings.Contains(err.Error(), "execution target changed") {
		t.Fatal("replaced Pod UID remained executable", err)
	}
}

func TestVitessPodIdentityGateCoversEveryRuntimeFamily(t *testing.T) {
	d := vitessTestDatabase()
	for _, role := range []string{"tablet", "gateway", "topology", "control", "orchestrator", "operator"} {
		pod := vitessIdentityGatePod(d, role, role, "stale", true)
		if vitessPodReadyWithIdentity(*pod, "current") {
			t.Fatalf("%s Pod passed with the prior certificate identity", role)
		}
		pod.Annotations[vitessIdentityAnnotation] = "current"
		if !vitessPodReadyWithIdentity(*pod, "current") {
			t.Fatalf("%s Pod with the current identity did not pass readiness", role)
		}
	}
}

func TestVitessTopologyRolloutTargetIsSerialAndDeterministic(t *testing.T) {
	d := vitessTestDatabase()
	pods := []corev1.Pod{
		*vitessIdentityGatePod(d, "topology-c", "topology", strings.Repeat("b", 64), true),
		*vitessIdentityGatePod(d, "topology-a", "topology", strings.Repeat("a", 64), true),
		*vitessIdentityGatePod(d, "topology-b", "topology", strings.Repeat("b", 64), true),
	}
	target, complete, err := vitessTopologyRolloutTarget(pods, strings.Repeat("b", 64))
	if err != nil || complete || target.Name != "topology-a" {
		t.Fatal("topology rollout did not select exactly the first stale ready voter", target.Name, complete, err)
	}
	pods[0].Status.Conditions = nil
	if target, complete, err = vitessTopologyRolloutTarget(pods, strings.Repeat("b", 64)); err != nil || complete || target.Name != "" {
		t.Fatal("topology rollout selected a voter while a peer was not ready", target.Name, complete, err)
	}
	pods[0].Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	pods[1].Annotations[vitessIdentityAnnotation] = strings.Repeat("b", 64)
	if target, complete, err = vitessTopologyRolloutTarget(pods, strings.Repeat("b", 64)); err != nil || !complete || target.Name != "" {
		t.Fatal("current topology rollout did not complete", target.Name, complete, err)
	}
}

func TestVitessTopologyRolloutTargetRejectsInvalidIdentity(t *testing.T) {
	d := vitessTestDatabase()
	pods := []corev1.Pod{
		*vitessIdentityGatePod(d, "topology-a", "topology", strings.Repeat("a", 64), true),
		*vitessIdentityGatePod(d, "topology-b", "topology", strings.Repeat("a", 64), true),
		*vitessIdentityGatePod(d, "topology-c", "topology", "not-a-fingerprint", true),
	}
	if _, _, err := vitessTopologyRolloutTarget(pods, strings.Repeat("b", 64)); err == nil {
		t.Fatal("topology rollout accepted an invalid prior identity")
	}
}

func vitessTopologyRolloutFixture(t *testing.T, identities ...string) (*Client, database.Resource, *unstructured.Unstructured, *kubefake.Clientset) {
	t.Helper()
	d := vitessTestDatabase()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
	root, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	root.SetUID("database-uid")
	etcd := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"annotations": map[string]any{vitessIdentityAnnotation: strings.Repeat("b", 64)}}}}
	etcd.SetAPIVersion("planetscale.com/v2")
	etcd.SetKind("EtcdLockserver")
	etcd.SetNamespace(ns.Name)
	etcd.SetName(vitessGeneratedName("database", "etcd"))
	etcd.SetUID("etcd-uid")
	etcd.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: "database", UID: root.GetUID(), Controller: ptr(true)}})
	objects := []runtime.Object{ns}
	for i, identity := range identities {
		cpu := resource.MustParse(database.VitessTopologyCPU)
		memory := resource.MustParse(database.VitessTopologyMemory)
		name := fmt.Sprintf("topology-%c", 'a'+i)
		objects = append(objects, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name, UID: types.UID(name + "-uid"), Labels: map[string]string{databaseOwner: d.ID, managedBy: "hakopod", vitessComponentLabel: "topology"}, Annotations: map[string]string{vitessIdentityAnnotation: identity}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "EtcdLockserver", Name: etcd.GetName(), UID: etcd.GetUID(), Controller: ptr(true)}}},
			Spec:       corev1.PodSpec{ServiceAccountName: "database-vitess-workload", Containers: []corev1.Container{{Name: "etcd", Image: vitessEtcdImage, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}, Limits: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}}}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		})
	}
	kube := kubefake.NewSimpleClientset(objects...)
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root, etcd)
	return &Client{kube: kube, dynamic: dynamic}, d, root, kube
}

func vitessTopologyRolloutPolicyFixture(t *testing.T, c *Client, kube *kubefake.Clientset) *DatabasePolicy {
	t.Helper()
	policy := &DatabasePolicy{NodeNames: []string{"worker-1", "worker-2", "worker-3"}, Pool: "vitess-acceptance", RuntimeClass: "runsc", StorageClass: "block"}
	requirement := func(key, value string) corev1.NodeSelectorRequirement {
		return corev1.NodeSelectorRequirement{Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{value}}
	}
	terms := make([]corev1.NodeSelectorTerm, len(policy.NodeNames))
	for i, name := range policy.NodeNames {
		terms[i] = corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				requirement("hakopod.com/pool", policy.Pool),
				requirement(DatabaseDefaultRuntimeLabel, policy.RuntimeClass),
				requirement(corev1.LabelArchStable, "amd64"),
			},
			MatchFields: []corev1.NodeSelectorRequirement{requirement("metadata.name", name)},
		}
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"hakopod.com/pool": policy.Pool, DatabaseDefaultRuntimeLabel: policy.RuntimeClass}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		if _, err := kube.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := kube.NodeV1().RuntimeClasses().Create(context.Background(), &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: policy.RuntimeClass}, Handler: policy.RuntimeClass}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := kube.StorageV1().StorageClasses().Create(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: policy.StorageClass}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pods, err := kube.CoreV1().Pods(DatabaseNamespace(vitessTestDatabase().ID)).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range pods.Items {
		pod := pods.Items[i].DeepCopy()
		pod.Spec.NodeName = policy.NodeNames[i%len(policy.NodeNames)]
		pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms}}}
		if _, err = kube.CoreV1().Pods(pod.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	c.options.DatabasePolicy = func(context.Context, string, string, database.Spec) (DatabasePolicy, error) { return *policy, nil }
	return policy
}

func TestVitessTopologyRolloutPendingPolicyGate(t *testing.T) {
	c, d, _, kube := vitessTopologyRolloutFixture(t, strings.Repeat("a", 64), strings.Repeat("a", 64), strings.Repeat("a", 64))
	policy := vitessTopologyRolloutPolicyFixture(t, c, kube)
	pod, err := kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(context.Background(), "topology-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Spec.NodeName = ""
	pod.Status.Conditions = nil
	pod.Status.Phase = corev1.PodPending
	if !vitessTopologyPodPolicyMatchesForWait(*pod, policy) {
		t.Fatal("valid unscheduled replacement was rejected")
	}
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Spec.RuntimeClassName = ptr("runc") },
		func(p *corev1.Pod) {
			p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values[0] = "other"
		},
		func(p *corev1.Pod) { p.Spec.NodeName = "worker-4" },
		func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		},
	} {
		changed := pod.DeepCopy()
		mutate(changed)
		if vitessTopologyPodPolicyMatchesForWait(*changed, policy) {
			t.Fatal("invalid pending or scheduled topology policy was accepted")
		}
	}
}

func TestVitessTopologyRolloutHealthFailurePreventsDeletion(t *testing.T) {
	old, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
	c, d, root, kube := vitessTopologyRolloutFixture(t, old, old, old)
	blocked := errors.New("quorum check failed")
	deletes := 0
	kube.Fake.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deletes++
		return true, nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.rollVitessTopologyIdentityWithHealth(ctx, d, root, current, func() error { return nil }, func(_ context.Context, _ database.Resource, _ *unstructured.Unstructured, _ *DatabasePolicy, pod corev1.Pod) error {
		return blocked
	})
	if !errors.Is(err, blocked) || deletes != 0 {
		t.Fatal("failed topology health check did not block deletion", err, deletes)
	}
}

func TestVitessTopologyRolloutRechecksUIDAfterLease(t *testing.T) {
	old, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
	c, d, root, kube := vitessTopologyRolloutFixture(t, old, old, old)
	deletes := 0
	kube.Fake.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deletes++
		return true, nil, nil
	})
	before := func() error {
		pod, err := kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(context.Background(), "topology-a", metav1.GetOptions{})
		if err != nil {
			return err
		}
		pod.UID = "replacement-uid"
		_, err = kube.CoreV1().Pods(pod.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.rollVitessTopologyIdentityWithHealth(ctx, d, root, current, before, func(context.Context, database.Resource, *unstructured.Unstructured, *DatabasePolicy, corev1.Pod) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "ownership changed before deletion") || deletes != 0 {
		t.Fatal("changed topology Pod UID was not fenced", err, deletes)
	}
}

func TestVitessTopologyRolloutRechecksEveryPeerAfterLease(t *testing.T) {
	old, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, test := range []struct {
		name   string
		mutate func(*corev1.Pod)
	}{
		{name: "uid", mutate: func(pod *corev1.Pod) { pod.UID = "replacement-uid" }},
		{name: "readiness", mutate: func(pod *corev1.Pod) { pod.Status.Conditions = nil }},
		{name: "identity", mutate: func(pod *corev1.Pod) { pod.Annotations[vitessIdentityAnnotation] = strings.Repeat("c", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, d, root, kube := vitessTopologyRolloutFixture(t, old, old, old)
			deletes := 0
			kube.Fake.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
				deletes++
				return true, nil, nil
			})
			before := func() error {
				pod, err := kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(context.Background(), "topology-b", metav1.GetOptions{})
				if err != nil {
					return err
				}
				test.mutate(pod)
				_, err = kube.CoreV1().Pods(pod.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := c.rollVitessTopologyIdentityWithHealth(ctx, d, root, current, before, func(context.Context, database.Resource, *unstructured.Unstructured, *DatabasePolicy, corev1.Pod) error {
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "ownership changed before deletion") || deletes != 0 {
				t.Fatal("changed non-target topology Pod was not fenced", err, deletes)
			}
		})
	}
}

func TestVitessTopologyRolloutRechecksHealthAfterLease(t *testing.T) {
	old, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
	c, d, root, kube := vitessTopologyRolloutFixture(t, old, old, old)
	deletes := 0
	kube.Fake.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deletes++
		return true, nil, nil
	})
	leaseAcquired := false
	blocked := errors.New("quorum changed after lease")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.rollVitessTopologyIdentityWithHealth(ctx, d, root, current, func() error {
		leaseAcquired = true
		return nil
	}, func(context.Context, database.Resource, *unstructured.Unstructured, *DatabasePolicy, corev1.Pod) error {
		if leaseAcquired {
			return blocked
		}
		return nil
	})
	if !errors.Is(err, blocked) || deletes != 0 {
		t.Fatal("post-lease quorum transition did not block deletion", err, deletes)
	}
}

func TestVitessTopologyRolloutRechecksRootSpecAfterLease(t *testing.T) {
	old, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
	c, d, root, kube := vitessTopologyRolloutFixture(t, old, old, old)
	deletes := 0
	kube.Fake.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deletes++
		return true, nil, nil
	})
	before := func() error {
		api := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID))
		renewed, err := api.Get(context.Background(), "database", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err = unstructured.SetNestedField(renewed.Object, "renewed", "spec", "concurrentRenewal"); err != nil {
			return err
		}
		_, err = api.Update(context.Background(), renewed, metav1.UpdateOptions{})
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.rollVitessTopologyIdentityWithHealth(ctx, d, root, current, before, func(context.Context, database.Resource, *unstructured.Unstructured, *DatabasePolicy, corev1.Pod) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "root specification changed before deletion") || deletes != 0 {
		t.Fatal("concurrent root specification renewal was not fenced", err, deletes)
	}
}

func TestVitessTopologyRolloutResumesAndReplacesOneAtATime(t *testing.T) {
	old, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
	c, d, root, kube := vitessTopologyRolloutFixture(t, current, old, old)
	vitessTopologyRolloutPolicyFixture(t, c, kube)
	deleted := []string{}
	pending := 0
	pendingNodes := map[string]string{}
	observedPending := false
	podsGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	kube.Fake.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction := action.(k8stesting.ListAction)
		listed, err := kube.Tracker().List(podsGVR, schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, listAction.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		result := listed.(*corev1.PodList).DeepCopy()
		observedPending = false
		for i := range result.Items {
			pod := &result.Items[i]
			node, found := pendingNodes[pod.Name]
			if !found || pod.Spec.NodeName != "" || vitessPodReady(*pod) {
				continue
			}
			pending++
			observedPending = true
			delete(pendingNodes, pod.Name)
			scheduled := pod.DeepCopy()
			scheduled.Spec.NodeName = node
			scheduled.Status.Phase = corev1.PodRunning
			scheduled.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			if err = kube.Tracker().Update(podsGVR, scheduled, scheduled.Namespace); err != nil {
				return true, nil, err
			}
		}
		return true, result, nil
	})
	kube.Fake.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if observedPending {
			return true, nil, errors.New("deleted another voter while its replacement was pending")
		}
		deleteAction := action.(k8stesting.DeleteAction)
		pod, err := kube.Tracker().Get(podsGVR, DatabaseNamespace(d.ID), deleteAction.GetName())
		if err != nil {
			return true, nil, err
		}
		replacement := pod.(*corev1.Pod).DeepCopy()
		if deleteAction.GetDeleteOptions().Preconditions == nil || deleteAction.GetDeleteOptions().Preconditions.UID == nil || *deleteAction.GetDeleteOptions().Preconditions.UID != replacement.UID {
			return true, nil, errors.New("delete omitted the exact Pod UID")
		}
		deleted = append(deleted, replacement.Name)
		replacement.UID = types.UID(replacement.Name + "-replacement")
		replacement.Annotations[vitessIdentityAnnotation] = current
		node := replacement.Spec.NodeName
		replacement.Spec.NodeName = ""
		replacement.Status.Phase = corev1.PodPending
		replacement.Status.Conditions = nil
		if err = kube.Tracker().Update(podsGVR, replacement, replacement.Namespace); err != nil {
			return true, nil, err
		}
		pendingNodes[replacement.Name] = node
		return true, nil, nil
	})
	healthChecks := 0
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := c.rollVitessTopologyIdentityWithHealth(ctx, d, root, current, func() error { return nil }, func(_ context.Context, _ database.Resource, _ *unstructured.Unstructured, _ *DatabasePolicy, pod corev1.Pod) error {
		if observedPending || pod.Spec.NodeName == "" || !vitessPodReady(pod) {
			return errors.New("checked quorum while a replacement was pending")
		}
		healthChecks++
		return nil
	})
	if err != nil || !slices.Equal(deleted, []string{"topology-b", "topology-c"}) || pending != 2 || healthChecks < 6 {
		t.Fatal("interrupted topology rollout did not wait for serially scheduled replacements", deleted, pending, healthChecks, err)
	}
}

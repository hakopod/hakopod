package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClickHouseSandboxAvailabilityAndPlacement(t *testing.T) {
	ctx := context.Background()
	d := clickhouseFixture()
	d.Spec.Placement.NodeNames = []string{"worker"}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{corev1.LabelHostname: "worker", clickhouseRuntimeLabel: clickhouseRuntimeProfile}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	r := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: clickhouseRuntimeClass, Labels: map[string]string{managedBy: "hakopod"}, Annotations: map[string]string{clickhouseRuntimeLabel: clickhouseRuntimeProfile}}, Handler: clickhouseRuntimeClass, Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{clickhouseRuntimeLabel: clickhouseRuntimeProfile}}}
	kube := fake.NewClientset(node)
	c := &Client{kube: kube, options: Options{ClickHouseSandbox: true}}
	if _, err := c.clickhouseRuntime(ctx, d, nil); err == nil {
		t.Fatal("missing sandbox admitted")
	}
	if _, err := kube.NodeV1().RuntimeClasses().Create(ctx, r, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	object, err := c.databaseObject(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	templates, _, _ := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
	pod := templates[0].(map[string]any)["spec"].(map[string]any)
	if pod["runtimeClassName"] != clickhouseRuntimeClass || pod["nodeSelector"].(map[string]any)[clickhouseRuntimeLabel] != clickhouseRuntimeProfile {
		t.Fatal("sandbox scheduling was not applied")
	}
	d.Spec.Placement.NodeNames = []string{"unverified"}
	if _, err = c.clickhouseRuntime(ctx, d, nil); err == nil {
		t.Fatal("unverified worker admitted")
	}
	d.Spec.Placement = database.Placement{Spread: "nodes"}
	d.Spec.Mode = "cluster"
	d.Spec.Replicas = 1
	if _, err = c.clickhouseRuntime(ctx, d, nil); err == nil {
		t.Fatal("Keeper placement domains were not checked")
	}
	d = clickhouseFixture()
	r.Handler = "runc"
	if _, err = kube.NodeV1().RuntimeClasses().Update(ctx, r, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.clickhouseRuntime(ctx, d, nil); err == nil {
		t.Fatal("unsafe runtime handler admitted")
	}
	c.options.ClickHouseSandbox = false
	if runtime, err := c.clickhouseRuntime(ctx, d, nil); err != nil || runtime != "" {
		t.Fatal("ordinary self-hosted runtime unexpectedly requires a sandbox")
	}
	if _, err = c.clickhouseRuntime(ctx, d, &DatabasePolicy{NodeName: "worker"}); err == nil {
		t.Fatal("hosted policy bypassed sandbox verification")
	}
}

func TestClickHouseSandboxPodDrift(t *testing.T) {
	p := &DatabasePolicy{NodeName: "worker", Pool: "paid", RuntimeClass: "runsc", podRuntimeClass: clickhouseRuntimeClass}
	pod := corev1.Pod{Spec: corev1.PodSpec{NodeName: "worker", RuntimeClassName: ptr(clickhouseRuntimeClass), NodeSelector: map[string]string{"hakopod.com/pool": "paid", DatabaseDefaultRuntimeLabel: "runsc", clickhouseRuntimeLabel: clickhouseRuntimeProfile}}}
	if !databasePodPolicyMatches(pod, p) || !clickhousePodRuntimeMatches(pod, clickhouseRuntimeClass) {
		t.Fatal("verified sandbox rejected")
	}
	pod.Spec.RuntimeClassName = nil
	if databasePodPolicyMatches(pod, p) || clickhousePodRuntimeMatches(pod, clickhouseRuntimeClass) {
		t.Fatal("default runtime bypass accepted")
	}
	pod.Spec.RuntimeClassName = ptr("runc")
	if databasePodPolicyMatches(pod, p) || clickhousePodRuntimeMatches(pod, clickhouseRuntimeClass) {
		t.Fatal("native runtime bypass accepted")
	}
}

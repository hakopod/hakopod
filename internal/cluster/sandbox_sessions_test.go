package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func sandboxFixture(t *testing.T) (*Client, sandbox.Record) {
	t.Helper()
	identity := strings.Repeat("a", 32)
	image := "example/worker@sha256:" + strings.Repeat("b", 64)
	app, err := spec.Normalize(spec.Application{Name: "sandbox-app", Services: map[string]spec.Service{"worker": {Image: image, Command: []string{"python", "worker.py"}, RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, ReadOnlyRootFilesystem: true, RuntimeProfile: "sandbox", Session: &spec.SandboxSession{AllowedIdentities: []string{identity}, HelperCommand: []string{"python", "helper.py"}, ReadyCommand: []string{"python", "ready.py"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := sandbox.Record{ID: strings.Repeat("c", 32), Generation: strings.Repeat("d", 32), ApplicationID: "sandbox-app", Project: "demo", Environment: "test", Service: "worker", Revision: 1, IdentityID: identity, OwnerHash: strings.Repeat("e", 64), RuntimeHash: strings.Repeat("f", 64), Image: image, Source: app, CreatedAt: now, ExpiresAt: now.Add(time.Hour), IdleUntil: now.Add(15 * time.Minute)}
	target, svc, err := sandboxTarget(r)
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(r.ApplicationID), UID: "application-ns", Labels: labelsFor(target, "")}}, &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "sandbox"}, Handler: "runsc", Overhead: &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("50Mi")}}})
	client.PrependReactor("create", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		switch obj := a.(ktesting.CreateAction).GetObject().(type) {
		case *corev1.Namespace:
			obj.UID = types.UID("session-ns-" + obj.Name)
		case *corev1.Pod:
			obj.UID = "kernel-one"
			obj.CreationTimestamp = metav1.Now()
		}
		return false, nil, nil
	})
	client.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "configmaps", Namespaced: true, Verbs: metav1.Verbs{"list"}}}}}
	c := &Client{dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "configmaps"}: "ConfigMapList"}), kube: client, options: Options{SessionGuardImage: "example/guard@sha256:" + strings.Repeat("1", 64), RuntimeProfileBindings: []RuntimeProfileBinding{{Name: "sandbox", Project: "demo", Environment: "test", Application: "sandbox-app", Service: "worker", RuntimeClass: "sandbox", Handler: "runsc"}}}}
	if err = c.applySessionTemplate(context.Background(), target, r.Service, svc); err != nil {
		t.Fatal(err)
	}
	return c, r
}
func startSandbox(t *testing.T, c *Client, r sandbox.Record) sandbox.Record {
	t.Helper()
	state, err := c.StartSession(context.Background(), r, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	r.NamespaceUID = state.NamespaceUID
	r.PodUID = state.PodUID
	r.ContainerID = state.ContainerID
	r.ImageID = state.ImageID
	return r
}
func readySandbox(t *testing.T, c *Client, r sandbox.Record) sandbox.Record {
	t.Helper()
	pod, err := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(context.Background(), sandboxPodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", ContainerID: "containerd://kernel-one", ImageID: "docker-pullable://worker@sha256:" + strings.Repeat("2", 64), Ready: true, Started: ptr(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if _, err = c.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	state, err := c.ObserveSession(context.Background(), r)
	if err != nil || !state.Ready {
		t.Fatalf("ready state rejected: %+v %v", state, err)
	}
	r.ContainerID = state.ContainerID
	r.ImageID = state.ImageID
	return r
}
func TestSandboxCreatesOneIsolatedKernelAndNeverReplacesIt(t *testing.T) {
	c, r := sandboxFixture(t)
	r = startSandbox(t, c, r)
	ctx := context.Background()
	p, err := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(ctx, sandboxPodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec.RestartPolicy != corev1.RestartPolicyNever || len(p.OwnerReferences) != 0 || *p.Spec.RuntimeClassName != "sandbox" || *p.Spec.AutomountServiceAccountToken || p.Spec.Containers[0].Command[0] != "/run/hakopod-session/guard" || !p.Spec.Containers[0].VolumeMounts[len(p.Spec.Containers[0].VolumeMounts)-1].ReadOnly {
		t.Fatal("kernel isolation or guarded command is missing")
	}
	if p.Labels[networkKey("default")] != "" {
		t.Fatal("kernel joined an application network")
	}
	if _, err = c.StartSession(ctx, r, func(context.Context) error { return nil }); err != nil {
		t.Fatal("response-loss recovery failed", err)
	}
	r = readySandbox(t, c, r)
	if err = c.kube.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.StartSession(ctx, r, func(context.Context) error { return nil }); err == nil {
		t.Fatal("lost kernel was recreated")
	}
}
func TestSandboxRequiresReadinessAndPinsContainerAndImage(t *testing.T) {
	c, r := sandboxFixture(t)
	r = startSandbox(t, c, r)
	ctx := context.Background()
	state, err := c.ObserveSession(ctx, r)
	if err != nil || state.Ready {
		t.Fatal("pending kernel became ready", err)
	}
	r = readySandbox(t, c, r)
	for _, change := range []func(*corev1.Pod){func(p *corev1.Pod) { p.Status.ContainerStatuses[0].RestartCount = 1 }, func(p *corev1.Pod) { p.Status.ContainerStatuses[0].ContainerID = "containerd://replaced" }, func(p *corev1.Pod) { p.Status.ContainerStatuses[0].ImageID = "sha256:replaced" }} {
		p, _ := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(ctx, sandboxPodName, metav1.GetOptions{})
		original := p.DeepCopy()
		change(p)
		_, _ = c.kube.CoreV1().Pods(p.Namespace).UpdateStatus(ctx, p, metav1.UpdateOptions{})
		if _, err = c.ObserveSession(ctx, r); err == nil {
			t.Fatal("changed kernel generation accepted")
		}
		_, _ = c.kube.CoreV1().Pods(original.Namespace).UpdateStatus(ctx, original, metav1.UpdateOptions{})
	}
}
func TestSandboxRejectsForeignOrModifiedRuntime(t *testing.T) {
	for name, change := range map[string]func(*corev1.Pod){"uid": func(p *corev1.Pod) { p.UID = "other" }, "owner": func(p *corev1.Pod) { p.Labels[sandboxOwner] = "other" }, "image": func(p *corev1.Pod) { p.Spec.Containers[0].Image = "other" }, "guard": func(p *corev1.Pod) { p.Spec.InitContainers[0].Image = "other" }, "network": func(p *corev1.Pod) { p.Spec.HostNetwork = true }, "writable-guard": func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].ReadOnly = false }, "sidecar": func(p *corev1.Pod) { p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "foreign"}) }, "env": func(p *corev1.Pod) {
		p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "EXTRA", Value: "1"})
	}} {
		t.Run(name, func(t *testing.T) {
			c, r := sandboxFixture(t)
			r = startSandbox(t, c, r)
			ctx := context.Background()
			p, _ := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(ctx, sandboxPodName, metav1.GetOptions{})
			change(p)
			_, _ = c.kube.CoreV1().Pods(p.Namespace).Update(ctx, p, metav1.UpdateOptions{})
			if _, err := c.ObserveSession(ctx, r); err == nil {
				t.Fatal("modified runtime accepted")
			}
		})
	}
}
func TestSandboxRejectsAdditionalNetworkPolicy(t *testing.T) {
	c, r := sandboxFixture(t)
	r = startSandbox(t, c, r)
	ctx := context.Background()
	_, err := c.kube.NetworkingV1().NetworkPolicies(SandboxNamespace(r.ID)).Create(ctx, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow", Namespace: SandboxNamespace(r.ID)}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ObserveSession(ctx, r); err == nil {
		t.Fatal("extra network policy accepted")
	}
}
func TestSandboxChecksLeaseBeforeEveryCreate(t *testing.T) {
	for fail := 1; fail <= 5; fail++ {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			c, r := sandboxFixture(t)
			calls := 0
			_, err := c.StartSession(context.Background(), r, func(context.Context) error {
				calls++
				if calls == fail {
					return fmt.Errorf("revoked")
				}
				return nil
			})
			if err == nil {
				t.Fatal("revoked lease accepted")
			}
			pods, _ := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).List(context.Background(), metav1.ListOptions{})
			if len(pods.Items) != 0 {
				t.Fatal("revoked lease created a kernel")
			}
		})
	}
}
func TestSandboxCleanupRejectsReplacementAndWaitsForPodAbsence(t *testing.T) {
	c, r := sandboxFixture(t)
	r = startSandbox(t, c, r)
	ctx := context.Background()
	foreign := r
	foreign.PodUID = "foreign"
	if done, err := c.CleanupSession(ctx, foreign); err == nil || done {
		t.Fatal("foreign Pod cleanup accepted")
	}
	fakeClient := c.kube.(*fake.Clientset)
	sawUID := false
	fakeClient.PrependReactor("delete", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		o := a.(ktesting.DeleteAction).GetDeleteOptions()
		sawUID = o.Preconditions != nil && o.Preconditions.UID != nil && string(*o.Preconditions.UID) == r.PodUID
		return false, nil, nil
	})
	if done, err := c.CleanupSession(ctx, r); err != nil || done {
		t.Fatal("cleanup skipped Pod observation", err)
	}
	if !sawUID {
		t.Fatal("Pod delete lacks exact UID precondition")
	}
	if done, err := c.CleanupSession(ctx, r); err != nil || !done {
		t.Fatal("owned namespace cleanup failed", err)
	}
}
func TestSandboxStreamBounds(t *testing.T) {
	in := &sandboxInputBound{reader: strings.NewReader("four"), remaining: 3}
	if _, err := io.ReadAll(in); err == nil || !in.exceeded.Load() {
		t.Fatal("oversized input accepted")
	}
	var output bytes.Buffer
	budget := &sandboxOutputBudget{remaining: 3}
	out := &sandboxOutputBound{writer: &output, budget: budget}
	if _, err := out.Write([]byte("four")); err == nil || !budget.Exceeded() || output.Len() != 0 {
		t.Fatal("oversized output escaped its bound")
	}
}
func TestSandboxCleanupPreservesForeignResources(t *testing.T) {
	c, r := sandboxFixture(t)
	r = startSandbox(t, c, r)
	ctx := context.Background()
	foreign := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foreign-state", Namespace: SandboxNamespace(r.ID)}, Data: map[string]string{"value": "keep"}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(foreign.Namespace).Create(ctx, &unstructured.Unstructured{Object: raw}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CleanupSession(ctx, r); err != nil {
		t.Fatal(err)
	}
	if done, err := c.CleanupSession(ctx, r); err == nil || done {
		t.Fatal("namespace containing foreign state was deleted")
	}
	if _, err = c.kube.CoreV1().Namespaces().Get(ctx, foreign.Namespace, metav1.GetOptions{}); err != nil {
		t.Fatal("foreign namespace was not preserved")
	}
}
func TestSandboxAcceptsOnlyReservedRuntimeOverhead(t *testing.T) {
	for _, tc := range []struct {
		name, cpu, memory string
		accept            bool
	}{{"bounded", "20m", "50Mi", true}, {"cpu", "21m", "50Mi", false}, {"underreserved", "19m", "50Mi", false}, {"memory", "20m", "51Mi", false}} {
		t.Run(tc.name, func(t *testing.T) {
			c, r := sandboxFixture(t)
			ctx := context.Background()
			class, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, "sandbox", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			class.Overhead = &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(tc.cpu), corev1.ResourceMemory: resource.MustParse(tc.memory)}}
			if _, err = c.kube.NodeV1().RuntimeClasses().Update(ctx, class, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			_, err = c.StartSession(ctx, r, func(context.Context) error { return nil })
			if (err == nil) != tc.accept {
				t.Fatal("unexpected runtime overhead result", err)
			}
			if tc.accept {
				p, e := c.kube.CoreV1().Pods(SandboxNamespace(r.ID)).Get(ctx, sandboxPodName, metav1.GetOptions{})
				if e != nil {
					t.Fatal(e)
				}
				p.Spec.Overhead[corev1.ResourceMemory] = resource.MustParse("51Mi")
				_, _ = c.kube.CoreV1().Pods(p.Namespace).Update(ctx, p, metav1.UpdateOptions{})
				if _, e = c.ObserveSession(ctx, r); e == nil {
					t.Fatal("modified runtime overhead accepted")
				}
			}
		})
	}
}
func TestSandboxCleanupPreservesForeignFieldsOnDefaultResources(t *testing.T) {
	c, r := sandboxFixture(t)
	_ = c
	target, _, err := sandboxTarget(r)
	if err != nil {
		t.Fatal(err)
	}
	ca := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "kube-root-ca.crt"}, "data": map[string]any{"ca.crt": "fixture-ca"}}}
	kind := schema.GroupResource{Resource: "configmaps"}
	if !sandboxCleanupAllowed(ca, kind, target, r) {
		t.Fatal("default CA rejected")
	}
	ca.Object["binaryData"] = map[string]any{"foreign": "a2VlcA=="}
	if sandboxCleanupAllowed(ca, kind, target, r) {
		t.Fatal("foreign binary CA fields would be erased")
	}
	for _, field := range []string{"secrets", "imagePullSecrets"} {
		account := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "default"}, field: "malformed"}}
		if sandboxCleanupAllowed(account, schema.GroupResource{Resource: "serviceaccounts"}, target, r) {
			t.Fatal("malformed default account would be erased")
		}
	}
}

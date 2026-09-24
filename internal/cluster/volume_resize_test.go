package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResizeHelperRejectsChangedExecution(t *testing.T) {
	target := Target{ApplicationID: "test", OperationID: "resize-test", Project: "demo", Environment: "development"}
	plan := spec.VolumeResize{Claim: "db-data", TargetClaim: "resized-data", SizeGiB: 1, User: 1000, Group: 1000, FSGroup: 1000}
	expected := resizeHelperPod(target, plan, &VolumeResizeJournal{SourceUID: "source", TargetUID: "target"})
	defaulted := expected.DeepCopy()
	defaulted.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	defaulted.Spec.Containers[0].TerminationMessagePath = corev1.TerminationMessagePathDefault
	defaulted.Spec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageReadFile
	if err := validateResizeHelper(defaulted, expected); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*corev1.Pod){
		"image":   func(p *corev1.Pod) { p.Spec.Containers[0].Image = "untrusted:latest" },
		"command": func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"sh", "-c", "echo forged"} },
		"environment": func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "PYTHONPATH", Value: "/source"}}
		},
		"writable source": func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].ReadOnly = false },
		"source claim":    func(p *corev1.Pod) { p.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "foreign" },
		"privileged":      func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.Privileged = ptr(true) },
		"root":            func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsUser = ptr(int64(0)) },
		"token":           func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = ptr(true) },
		"init":            func(p *corev1.Pod) { p.Spec.InitContainers = []corev1.Container{{Name: "inject", Image: "untrusted"}} },
		"sidecar":         func(p *corev1.Pod) { p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "inject"}) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := defaulted.DeepCopy()
			change(p)
			if validateResizeHelper(p, expected) == nil {
				t.Fatal("trusted altered execution")
			}
		})
	}
}

func TestResizeClaimGuardsNeverRecreateMissingTarget(t *testing.T) {
	ctx := context.Background()
	target := Target{ApplicationID: "test", Project: "demo", Environment: "development"}
	for _, mode := range []string{"missing", "foreign", "replacement", "deleting", "valid"} {
		t.Run(mode, func(t *testing.T) {
			kube := fake.NewClientset()
			if mode != "missing" {
				pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: Namespace(target.ApplicationID), UID: types.UID("expected"), Labels: labelsFor(target, "")}}
				if mode == "foreign" {
					pvc.Labels = nil
				}
				if mode == "replacement" {
					pvc.UID = "new"
				}
				if mode == "deleting" {
					now := metav1.Now()
					pvc.DeletionTimestamp = &now
				}
				if _, err := kube.CoreV1().PersistentVolumeClaims(pvc.Namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			c := &Client{kube: kube}
			err := c.CheckResizeClaim(ctx, target, "data", "expected", false)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("guard result: %v", err)
			}
			if mode == "missing" && c.CheckResizeClaim(ctx, target, "data", "expected", true) != nil {
				t.Fatal("deletion replay should accept absent claim")
			}
			for _, a := range kube.Actions() {
				if a.GetVerb() == "create" && mode == "missing" {
					t.Fatal("recreated missing data")
				}
			}
		})
	}
}

func TestResizeHelperKeepsHostedPlacementAndBoundedMemory(t *testing.T) {
	target := Target{ApplicationID: "test", OperationID: "operation", policy: &WorkloadPolicy{NodeName: "allocated-worker", Pool: "free", RuntimeClass: "runsc", MemoryRequest: "2Gi"}}
	pod := resizeHelperPod(target, spec.VolumeResize{SizeGiB: 1}, &VolumeResizeJournal{})
	if pod.Spec.NodeSelector["kubernetes.io/hostname"] != "allocated-worker" || pod.Spec.NodeSelector["hakopod.com/pool"] != "free" || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "runsc" || len(pod.Spec.Tolerations) != 1 {
		t.Fatal("helper escaped hosted scheduling policy")
	}
	if pod.Spec.Containers[0].Resources.Requests.Memory().String() != "64Mi" {
		t.Fatal("helper inherited application memory")
	}
	changed := pod.DeepCopy()
	changed.Spec.RuntimeClassName = nil
	if validateResizeHelper(changed, pod) == nil {
		t.Fatal("accepted missing sandbox")
	}
	changed = pod.DeepCopy()
	changed.Spec.NodeSelector["kubernetes.io/hostname"] = "other-node"
	if validateResizeHelper(changed, pod) == nil {
		t.Fatal("accepted changed allocation")
	}
}

func TestResizeQuotaIsBoundedDurableAndRestored(t *testing.T) {
	ctx := context.Background()
	target := Target{ApplicationID: "test", OperationID: "resize", Project: "demo", Environment: "development"}
	q := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-budget", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceRequestsStorage: resource.MustParse("16Gi"), corev1.ResourcePersistentVolumeClaims: resource.MustParse("16"), corev1.ResourceLimitsMemory: resource.MustParse("1Gi")}}}
	kube := fake.NewClientset(q)
	c := &Client{kube: kube}
	for i := 0; i < 3; i++ {
		if ready, err := c.resizeQuota(ctx, target, 8); err != nil || ready {
			t.Fatal(err)
		}
	}
	got, err := kube.CoreV1().ResourceQuotas(q.Namespace).Get(ctx, q.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	storage := got.Spec.Hard[corev1.ResourceRequestsStorage]
	claims := got.Spec.Hard[corev1.ResourcePersistentVolumeClaims]
	memory := got.Spec.Hard[corev1.ResourceLimitsMemory]
	if storage.Value() != 24<<30 || claims.Value() != 17 || memory.Value() != 1<<30 {
		t.Fatal("budget accumulated or compute changed", got.Spec)
	}
	got.Status.Hard = got.Spec.Hard.DeepCopy()
	if _, err = kube.CoreV1().ResourceQuotas(q.Namespace).UpdateStatus(ctx, got, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if ready, err := c.resizeQuota(ctx, target, 8); err != nil || !ready {
		t.Fatal("observed quota not ready", err)
	}
	foreign := target
	foreign.OperationID = "foreign"
	if _, err := c.resizeQuota(ctx, foreign, 8); err == nil || c.restoreResizeQuota(ctx, foreign) == nil {
		t.Fatal("foreign operation took quota")
	}
	if err = c.restoreResizeQuota(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err = c.restoreResizeQuota(ctx, target); err != nil {
		t.Fatal("restore not idempotent", err)
	}
	got, err = kube.CoreV1().ResourceQuotas(q.Namespace).Get(ctx, q.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	storage = got.Spec.Hard[corev1.ResourceRequestsStorage]
	claims = got.Spec.Hard[corev1.ResourcePersistentVolumeClaims]
	if storage.Value() != 16<<30 || claims.Value() != 16 || got.Annotations[resizeQuotaKey] != "" {
		t.Fatal("budget not restored")
	}
}

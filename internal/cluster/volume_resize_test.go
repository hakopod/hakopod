package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
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

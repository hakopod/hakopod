package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"
	utilexec "k8s.io/client-go/util/exec"
)

func TestPodExecOwnershipAndRevocation(t *testing.T) {
	target := Target{ApplicationID: "exec-fixture", Spec: spec.Application{Services: map[string]spec.Service{"web": {}}}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: ns.Name, UID: types.UID("exact-uid"), Labels: labelsFor(target, "web")}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	options := TerminalOptions{Pod: pod.Name, Container: "app", Command: []string{"echo", "fixture"}}
	for _, tc := range []struct {
		name   string
		change func(*corev1.Pod)
		uid    types.UID
	}{
		{"replacement", func(*corev1.Pod) {}, "old-uid"},
		{"foreign owner", func(p *corev1.Pod) { p.Labels[ownerKey] = "foreign" }, pod.UID},
		{"other service", func(p *corev1.Pod) { p.Labels[serviceKey] = "other" }, pod.UID},
		{"stopped", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }, pod.UID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := pod.DeepCopy()
			tc.change(p)
			kube := fake.NewSimpleClientset(ns.DeepCopy(), p)
			client := &Client{kube: kube}
			if client.PodExec(context.Background(), target, "web", options, tc.uid, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context) error { return nil }) == nil {
				t.Fatal("unsafe target accepted")
			}
			for _, action := range kube.Actions() {
				if action.GetSubresource() == "exec" {
					t.Fatal("unsafe exec transport opened")
				}
			}
		})
	}
	kube := fake.NewSimpleClientset(ns, pod)
	denied := errors.New("fixture credential revoked")
	if err := (&Client{kube: kube}).PodExec(context.Background(), target, "web", options, pod.UID, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatal("revocation ignored", err)
	}
	if len(kube.Actions()) != 0 {
		t.Fatal("target read after authorization was denied")
	}
}

func TestLivePodExec(t *testing.T) {
	if os.Getenv("HAKOPOD_TEST_POD_EXEC") != "1" {
		t.Skip("set HAKOPOD_TEST_POD_EXEC=1 and HAKOPOD_TEST_KUBECONFIG for named development-cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("pod exec acceptance requires explicit k3d-hakopod-dev context")
	}
	client, err := New(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	target := Target{ApplicationID: fmt.Sprintf("exec-live-%d", time.Now().UnixNano()), Spec: spec.Application{Services: map[string]spec.Service{"worker": {}}}}
	labels := labelsFor(target, "")
	labels["hakopod.io/acceptance"] = "pod-exec"
	ns, err := client.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		current, err := client.kube.CoreV1().Namespaces().Get(clean, ns.Name, metav1.GetOptions{})
		if err != nil || current.UID != ns.UID || owned(current, target) != nil || current.Labels["hakopod.io/acceptance"] != "pod-exec" {
			t.Error("fixture ownership changed; namespace retained")
			return
		}
		if err := client.kube.CoreV1().Namespaces().Delete(clean, ns.Name, deleteOptions(current)); err != nil {
			t.Error(err)
		}
	})
	pod, err := client.kube.CoreV1().Pods(ns.Name).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: labelsFor(target, "worker")}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0", Command: []string{"sleep", "3600"}}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	options := TerminalOptions{Pod: pod.Name, Container: "app", Command: []string{"sh", "-c", "printf 'stdout'; printf 'stderr' >&2; exit 7"}}
	for {
		if _, err = client.TerminalPod(ctx, target, "worker", options); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture did not start", err)
		case <-time.After(300 * time.Millisecond):
		}
	}
	var stdout, stderr bytes.Buffer
	err = client.PodExec(ctx, target, "worker", options, pod.UID, &stdout, &stderr, func(context.Context) error { return nil })
	var exit utilexec.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 7 || stdout.String() != "stdout" || stderr.String() != "stderr" {
		t.Fatal("real command streams or exit status differ", err, stdout.String(), stderr.String())
	}
	denied := errors.New("fixture grant revoked")
	if err := client.PodExec(ctx, target, "worker", options, pod.UID, &stdout, &stderr, func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatal("revoked real command accepted", err)
	}
	t.Log("real noninteractive exec retained separate output and exit status, and refused revoked authority")
}

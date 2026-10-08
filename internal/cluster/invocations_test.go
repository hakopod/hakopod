package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func invocationTest(t *testing.T) (*Client, invocation.Record, []byte) {
	t.Helper()
	input := []byte(`{"payload":"fixture"}`)
	sum := sha256.Sum256(input)
	app, err := spec.Normalize(spec.Application{Name: "invocation-test", Services: map[string]spec.Service{"report": {Image: "example/job@sha256:" + strings.Repeat("a", 64), Job: &spec.Job{Invocation: &spec.JobInvocation{AllowedIdentities: []string{strings.Repeat("b", 32)}, InputKeys: []string{"payload"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	record := invocation.Record{ID: strings.Repeat("c", 32), ApplicationID: "invocation-test", Project: "demo", Environment: "test", Service: "report", Revision: 1, IdentityID: strings.Repeat("b", 32), OwnerHash: strings.Repeat("d", 64), InputHash: fmt.Sprintf("%x", sum), Source: app}
	target, svc, err := invocationTarget(record)
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(record.ApplicationID), UID: "namespace-one", Labels: labelsFor(target, "")}})
	client.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
		obj := a.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		obj.UID = "job-one"
		return false, nil, nil
	})
	c := &Client{kube: client}
	if err = c.applyInvocationTemplate(context.Background(), target, record.Service, svc); err != nil {
		t.Fatal(err)
	}
	return c, record, input
}
func TestInvocationCreatesFixedJobAndRecoversWithoutRetry(t *testing.T) {
	c, r, input := invocationTest(t)
	ctx := context.Background()
	state, err := c.StartInvocation(ctx, r, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.RuntimeUID = state.RuntimeUID
	r.NamespaceUID = state.NamespaceUID
	job, err := c.kube.BatchV1().Jobs(Namespace(r.ApplicationID)).Get(ctx, invocationJobName(r.ID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *job.Spec.BackoffLimit != 0 || job.Spec.TTLSecondsAfterFinished != nil || job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken || job.Spec.Template.Labels[networkKey("default")] != "true" {
		t.Fatal("job lost its bounds or network isolation")
	}
	found := false
	for _, mount := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == "invocation-input" && mount.ReadOnly && mount.MountPath == "/run/hakopod/invocation" {
			found = true
		}
	}
	if !found {
		t.Fatal("read-only input missing")
	}
	if _, err = c.StartInvocation(ctx, r, input, nil); err != nil {
		t.Fatal("same invocation could not recover", err)
	}
	jobs, _ := c.kube.BatchV1().Jobs(Namespace(r.ApplicationID)).List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatal("invocation created duplicate jobs")
	}
	if err = c.kube.BatchV1().Jobs(Namespace(r.ApplicationID)).Delete(ctx, job.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.StartInvocation(ctx, r, input, nil); err == nil {
		t.Fatal("missing recorded Job was recreated")
	}
}
func TestInvocationRejectsDifferentInputIdentityRevisionAndNamespace(t *testing.T) {
	for name, change := range map[string]func(*invocation.Record, *[]byte){
		"input":     func(_ *invocation.Record, b *[]byte) { *b = []byte(`{"payload":"other"}`) },
		"identity":  func(r *invocation.Record, _ *[]byte) { r.IdentityID = strings.Repeat("e", 32) },
		"revision":  func(r *invocation.Record, _ *[]byte) { r.Revision++ },
		"namespace": func(r *invocation.Record, _ *[]byte) { r.NamespaceUID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			c, r, b := invocationTest(t)
			change(&r, &b)
			if _, err := c.StartInvocation(context.Background(), r, b, nil); err == nil {
				t.Fatal("invalid execution accepted")
			}
			jobs, _ := c.kube.BatchV1().Jobs(Namespace(r.ApplicationID)).List(context.Background(), metav1.ListOptions{})
			if len(jobs.Items) != 0 {
				t.Fatal("invalid request created a job")
			}
		})
	}
}
func TestInvocationCleanupWaitsForOwnedPods(t *testing.T) {
	c, r, b := invocationTest(t)
	ctx := context.Background()
	state, err := c.StartInvocation(ctx, r, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.RuntimeUID = state.RuntimeUID
	r.NamespaceUID = state.NamespaceUID
	target, _, _ := invocationTarget(r)
	labels := labelsFor(target, r.Service)
	labels[invocationLabel] = r.ID
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "invocation-pod", Namespace: Namespace(r.ApplicationID), Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: invocationJobName(r.ID), UID: "job-one", Controller: ptr(true)}}}}
	if _, err = c.kube.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err := c.CleanupInvocation(ctx, r)
	if err != nil || done {
		t.Fatal("cleanup finished while pods exist", err)
	}
	if _, err = c.kube.CoreV1().Secrets(pod.Namespace).Get(ctx, invocationSecretName(r.ID), metav1.GetOptions{}); err != nil {
		t.Fatal("input deleted before pods ended")
	}
	if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err = c.CleanupInvocation(ctx, r)
	if err != nil || !done {
		t.Fatal("cleanup did not finish", err)
	}
	if _, err = c.kube.CoreV1().Secrets(pod.Namespace).Get(ctx, invocationSecretName(r.ID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("input secret retained")
	}
}
func TestInvocationDeploymentCannotRaceActiveJob(t *testing.T) {
	c, r, b := invocationTest(t)
	ctx := context.Background()
	if _, err := c.StartInvocation(ctx, r, b, nil); err != nil {
		t.Fatal(err)
	}
	target, _, _ := invocationTarget(r)
	if err := c.validateActiveInvocations(ctx, target); err == nil {
		t.Fatal("deployment accepted active invocation")
	}
	if err := c.cleanupJobs(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := c.kube.BatchV1().Jobs(Namespace(r.ApplicationID)).Get(ctx, invocationJobName(r.ID), metav1.GetOptions{}); err != nil {
		t.Fatal("deployment cleanup deleted invocation receipt")
	}
}

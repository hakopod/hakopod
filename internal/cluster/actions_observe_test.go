package cluster

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestActionsImagesObserveRunningContainersOnly(t *testing.T) {
	target := runnerTarget(t)
	pod := func(name, image string, running bool) *corev1.Pod {
		p := actionsPod(target, "runner", name, target.Spec.Services["runner"])
		state := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}
		if running {
			state = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "runner", Image: image, ImageID: "docker-pullable://" + image, State: state}}
		return p
	}
	old, next := "ghcr.io/hakopod/actions-runner@sha256:old", "ghcr.io/hakopod/actions-runner@sha256:new"
	draining := pod("old", old, true)
	now := metav1.Now()
	draining.DeletionTimestamp = &now
	pending := pod("pending", "unpulled-image", false)
	finished := pod("finished", "completed-image", false)
	finished.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	client := fake.NewClientset(draining, pod("new", next, true), pending, finished)
	c := &Client{kube: client}
	images, err := c.ActionsPodImages(context.Background(), target, "runner")
	if err != nil || !reflect.DeepEqual(images, []string{next, old}) {
		t.Fatalf("observed %v, %v", images, err)
	}
	if err := client.CoreV1().Pods(Namespace(target.ApplicationID)).Delete(context.Background(), draining.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	images, err = c.ActionsPodImages(context.Background(), target, "runner")
	if err != nil || !reflect.DeepEqual(images, []string{next}) {
		t.Fatalf("deleted pod still reported: %v %v", images, err)
	}
}

func TestActionsImagesRejectUnboundedAndForeignObservations(t *testing.T) {
	for _, which := range []string{"pagination", "foreign", "runtime"} {
		t.Run(which, func(t *testing.T) {
			target := runnerTarget(t)
			p := actionsPod(target, "runner", "fixture", target.Spec.Services["runner"])
			result := &corev1.PodList{Items: []corev1.Pod{*p}}
			if which == "pagination" {
				result.Continue = "more"
			}
			if which == "foreign" {
				result.Items[0].Labels[ownerKey] = "other"
			}
			if which == "runtime" {
				result.Items[0].Spec.RuntimeClassName = nil
			}
			client := fake.NewClientset()
			client.PrependReactor("list", "pods", func(a ktesting.Action) (bool, runtime.Object, error) { return true, result, nil })
			c := &Client{kube: client}
			images, err := c.ActionsPodImages(context.Background(), target, "runner")
			if which == "foreign" {
				// The fake API applies the requested selector even to reactors.
				if err == nil && len(images) != 0 {
					t.Fatal("foreign image leaked", images)
				}
			} else if err == nil {
				t.Fatal("accepted incomplete or unexpected runtime observation")
			}
		})
	}
}

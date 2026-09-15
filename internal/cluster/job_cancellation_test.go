package cluster

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestJobCancellationAfterCommittedCreateLosesResponse(t *testing.T) {
	for _, differentAttempt := range []bool{false, true} {
		t.Run(strconv.FormatBool(differentAttempt), func(t *testing.T) {
			target := testTarget(t)
			s := target.Spec.Services["worker"]
			s.Job = &spec.Job{TimeoutSeconds: 10}
			kube := fake.NewClientset()
			c := &Client{kube: kube, options: Options{RolloutTimeout: time.Second}}
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			kube.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
				job := a.(ktesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
				job.UID = "created-job"
				if job.Annotations[jobCreation] == "" {
					t.Fatal("missing request identity")
				}
				if differentAttempt {
					job.Annotations[jobCreation] = "another-attempt"
				}
				if err := kube.Tracker().Create(batchv1.SchemeGroupVersion.WithResource("jobs"), job, job.Namespace); err != nil {
					t.Fatal(err)
				}
				stop()
				return true, nil, context.Canceled
			})
			if err := c.runJob(ctx, target, "worker", s); !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation", err)
			}
			_, err := kube.BatchV1().Jobs(Namespace(target.ApplicationID)).Get(context.Background(), jobName("worker"), metav1.GetOptions{})
			if differentAttempt && err != nil || !differentAttempt && !apierrors.IsNotFound(err) {
				t.Fatal("cleanup did not respect the create attempt", err)
			}
		})
	}
}

func TestJobCancellationRetriesConflictAndPreservesReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(strconv.FormatBool(replacement), func(t *testing.T) {
			target := testTarget(t)
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName("worker"), Namespace: Namespace(target.ApplicationID), UID: "original", ResourceVersion: "1", Labels: labelsFor(target, "worker"), Annotations: map[string]string{jobRevision: strconv.FormatInt(target.Revision, 10)}}}
			kube := fake.NewClientset(job)
			c := &Client{kube: kube}
			deletes := 0
			kube.PrependReactor("delete", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
				deletes++
				opts := a.(ktesting.DeleteAction).GetDeleteOptions()
				if opts.Preconditions == nil || *opts.Preconditions.UID != "original" || opts.Preconditions.ResourceVersion == nil {
					t.Fatal("missing delete preconditions")
				}
				if deletes > 1 {
					if *opts.Preconditions.ResourceVersion != "2" {
						t.Fatal("did not refresh resource version")
					}
					return false, nil, nil
				}
				latest := job.DeepCopy()
				latest.ResourceVersion = "2"
				if replacement {
					latest.UID = "replacement"
				}
				if err := kube.Tracker().Update(batchv1.SchemeGroupVersion.WithResource("jobs"), latest, latest.Namespace); err != nil {
					t.Fatal(err)
				}
				return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "batch", Resource: "jobs"}, job.Name, errors.New("status changed"))
			})
			c.cancelJob(target, "worker", job.UID, "")
			_, err := kube.BatchV1().Jobs(job.Namespace).Get(context.Background(), job.Name, metav1.GetOptions{})
			if replacement && err != nil || !replacement && !apierrors.IsNotFound(err) {
				t.Fatal("cleanup leaked original or deleted replacement", err)
			}
		})
	}
}

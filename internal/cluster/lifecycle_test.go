package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestJobReconciliationCompletionAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion", true: "failure"}[failed], func(t *testing.T) {
			target := testTarget(t)
			s := target.Spec.Services["worker"]
			s.Job = &spec.Job{TimeoutSeconds: 10, Retries: 1}
			target.Spec.Services = map[string]spec.Service{"worker": s}
			kube := fake.NewClientset()
			c := &Client{kube: kube, options: Options{RolloutTimeout: time.Second}}
			kube.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
				j := a.(ktesting.CreateAction).GetObject().(*batchv1.Job)
				if j.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever || *j.Spec.BackoffLimit != 1 || *j.Spec.ActiveDeadlineSeconds != 10 || *j.Spec.Template.Spec.AutomountServiceAccountToken {
					t.Fatal("job runtime policy incorrect")
				}
				condition := batchv1.JobComplete
				if failed {
					condition = batchv1.JobFailed
				}
				j.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue}}
				return false, nil, nil
			})
			err := c.runJob(context.Background(), target, "worker", s)
			if (err != nil) != failed {
				t.Fatal(err)
			}
			actions := len(kube.Actions())
			err = c.runJob(context.Background(), target, "worker", s)
			if (err != nil) != failed {
				t.Fatal(err)
			}
			for _, a := range kube.Actions()[actions:] {
				if a.GetVerb() != "get" {
					t.Fatal("same revision job was rerun", a)
				}
			}
			observed, err := c.Observe(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			if !failed && (observed.Status != "healthy" || observed.Services[0].Status != "completed") {
				t.Fatal(observed)
			}
			if failed && observed.Status == "healthy" {
				t.Fatal("failed job reported healthy")
			}
			target.Revision = 2
			if err := c.runJob(context.Background(), target, "worker", s); (err != nil) != failed {
				t.Fatal(err)
			}
			j, _ := kube.BatchV1().Jobs(Namespace(target.ApplicationID)).Get(context.Background(), jobName("worker"), metav1.GetOptions{})
			if j.Annotations[jobRevision] != "2" {
				t.Fatal("new revision did not replace job")
			}
		})
	}
}

func TestConfigurationFilesAreImmutableAndScoped(t *testing.T) {
	target := testTarget(t)
	s := target.Spec.Services["worker"]
	content := "upstream: api"
	s.Files = map[string]spec.File{"settings": {MountPath: "/etc/app/config.yaml", Content: &content}, "password": {MountPath: "/app/password", Secret: &spec.SecretRef{Ref: "db-password"}}}
	target.Spec.Services = map[string]spec.Service{"worker": s}
	target.secretValues = map[string]map[string][]byte{"worker": {spec.FileSecretKey("password"): []byte("private-value")}}
	kube := fake.NewClientset()
	c := &Client{kube: kube}
	if err := c.prepareFiles(context.Background(), target, "worker", s); err != nil {
		t.Fatal(err)
	}
	d := deployment(target, "worker", s, time.Minute)
	if len(d.Spec.Template.Spec.Volumes) != 2 || len(d.Spec.Template.Spec.Containers[0].Env) != 0 {
		t.Fatal("file secrets leaked into env or mounts missing")
	}
	for _, m := range d.Spec.Template.Spec.Containers[0].VolumeMounts {
		if !m.ReadOnly || m.SubPath != "file" {
			t.Fatal(m)
		}
	}
	cm, secret := fileObjects(target, "worker", s)
	stored, _ := kube.CoreV1().Secrets(secret.Namespace).Get(context.Background(), secret.Name, metav1.GetOptions{})
	if !*stored.Immutable || string(stored.Data["password"]) != "private-value" {
		t.Fatal("missing secret snapshot")
	}
	oldName := cm.Name
	content = "changed"
	next, _ := fileObjects(target, "worker", s)
	if next.Name == oldName {
		t.Fatal("config change did not change pod template")
	}
	// Failed source reads cannot create a partial set of configuration files.
	target.secretValues = nil
	if err := c.prepareFiles(context.Background(), target, "worker", s); err == nil {
		t.Fatal("missing snapshot accepted")
	}
}
